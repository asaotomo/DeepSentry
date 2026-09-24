package skills

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- required only to reproduce Git SHA-1 blob object IDs
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	clawHubBaseURL        = "https://clawhub.ai"
	skillsSearchURL       = "https://skills.sh/api/search"
	githubAPIBaseURL      = "https://api.github.com"
	maxMarketplaceBytes   = 24 << 20
	maxSkillFileBytes     = 4 << 20
	maxSkillTotalBytes    = 24 << 20
	maxGitHubArchiveBytes = 64 << 20
	maxSkillFiles         = 512
	maxUnauthBlobFallback = 40
	marketplaceLockFile   = ".deepsentry-market.json"
	marketplaceUserAgent  = "DeepSentry/2.0 skill-market"
)

var (
	safeSlugPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	repoPattern      = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	githubSHAPattern = regexp.MustCompile(`^[a-fA-F0-9]{7,64}$`)
	riskyPatterns    = []struct {
		label string
		re    *regexp.Regexp
	}{
		{"管道下载后执行", regexp.MustCompile(`(?i)(curl|wget)[^\n]{0,240}\|\s*(sh|bash|zsh|python|node)`)},
		{"链式安装其他 Skill", regexp.MustCompile(`(?i)\b(npx|bunx|pnpm\s+dlx)\s+[^\n]{0,160}\bskills?\s+(add|install)\b`)},
		{"动态代码执行", regexp.MustCompile(`(?i)\b(eval|exec)\s*\(`)},
		{"递归强制删除", regexp.MustCompile(`(?i)\brm\s+-[^\n]*r[^\n]*f|Remove-Item[^\n]*-Recurse[^\n]*-Force`)},
		{"读取常见凭据环境变量", regexp.MustCompile(`(?i)(API_KEY|ACCESS_TOKEN|SECRET_KEY|AWS_SECRET_ACCESS_KEY|GITHUB_TOKEN|OPENAI_API_KEY)`)},
		{"修改系统持久化位置", regexp.MustCompile(`(?i)(/etc/(cron|systemd)|LaunchAgents|schtasks\b|crontab\b)`)},
	}
)

var marketplaceHTTPClient = &http.Client{
	Timeout: 25 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("重定向次数过多")
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("市场下载拒绝非 HTTPS 重定向")
		}
		return nil
	},
}

type MarketSearchResult struct {
	Market      string
	Ref         string
	Name        string
	Description string
	Author      string
	Downloads   int
	Installs    int
	Score       float64
}

type SkillAudit struct {
	Name        string
	Description string
	Files       int
	Bytes       int64
	Warnings    []string
}

type installFile struct {
	Path string
	Data []byte
}

type managedSkill struct {
	Source      string          `json:"source"`
	Market      string          `json:"market"`
	Version     string          `json:"version,omitempty"`
	Digest      string          `json:"digest"`
	InstalledAt string          `json:"installed_at"`
	Warnings    []string        `json:"warnings,omitempty"`
	Pinned      bool            `json:"pinned,omitempty"`
	Removed     bool            `json:"removed,omitempty"`
	Backups     []managedBackup `json:"backups,omitempty"`
}

type managedBackup struct {
	Path      string `json:"path"`
	Source    string `json:"source,omitempty"`
	Market    string `json:"market,omitempty"`
	Version   string `json:"version,omitempty"`
	Digest    string `json:"digest,omitempty"`
	CreatedAt string `json:"created_at"`
}

type managedSkillLock struct {
	Version int                     `json:"version"`
	Skills  map[string]managedSkill `json:"skills"`
}

// ManageMarketplace provides a native discovery/install surface for Agent
// Skills. Search and inspection are read-only. Installation never executes
// downloaded code and requires an explicit confirmation flag.
func ManageMarketplace(args map[string]string) (string, error) {
	action := strings.ToLower(strings.TrimSpace(args["action"]))
	if action == "" {
		action = "search"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	switch action {
	case "search", "find":
		query := strings.TrimSpace(firstValue(args, "query", "text", "keyword"))
		if query == "" {
			return "", fmt.Errorf("search 需要 query")
		}
		limit := boundedInt(args["limit"], 8, 1, 30)
		results, err := SearchMarkets(ctx, query, strings.TrimSpace(args["market"]), limit)
		if err != nil {
			return "", err
		}
		return FormatSearchResults(query, results), nil
	case "inspect", "info":
		ref := strings.TrimSpace(firstValue(args, "source", "ref", "name"))
		if ref == "" {
			return "", fmt.Errorf("inspect 需要 source")
		}
		return InspectMarketSkill(ctx, ref)
	case "install", "add", "import":
		if !truthy(args["confirm_install"]) {
			return "", fmt.Errorf("安装第三方 Skill 前必须显式传 confirm_install=true")
		}
		ref := strings.TrimSpace(firstValue(args, "source", "ref", "name"))
		if ref == "" {
			return "", fmt.Errorf("install 需要 source，例如 /path/demo.skill、clawhub:security-audit 或 skills:owner/repo@skill")
		}
		dest := strings.TrimSpace(args["dest"])
		if dest == "" {
			dest = defaultManagedSkillDir()
		}
		return InstallMarketSkill(ctx, ref, dest, truthy(args["force"]), truthy(args["acknowledge_risk"]))
	case "managed", "installed":
		dest := strings.TrimSpace(args["dest"])
		if dest == "" {
			dest = defaultManagedSkillDir()
		}
		return FormatManagedSkills(dest)
	case "check_updates", "updates", "outdated":
		dest := strings.TrimSpace(args["dest"])
		if dest == "" {
			dest = defaultManagedSkillDir()
		}
		return CheckManagedUpdates(ctx, dest, strings.TrimSpace(args["name"]))
	case "update", "upgrade":
		if !truthy(firstValue(args, "confirm_update", "confirm_install")) {
			return "", fmt.Errorf("更新第三方 Skill 前必须显式传 confirm_update=true")
		}
		dest := strings.TrimSpace(args["dest"])
		if dest == "" {
			dest = defaultManagedSkillDir()
		}
		return UpdateManagedSkills(ctx, dest, strings.TrimSpace(args["name"]), truthy(args["acknowledge_risk"]))
	case "pin", "unpin":
		dest := strings.TrimSpace(args["dest"])
		if dest == "" {
			dest = defaultManagedSkillDir()
		}
		return SetManagedSkillPinned(dest, strings.TrimSpace(args["name"]), action == "pin")
	case "uninstall", "remove":
		if !truthy(firstValue(args, "confirm_remove", "confirm_uninstall")) {
			return "", fmt.Errorf("卸载第三方 Skill 前必须显式传 confirm_remove=true")
		}
		dest := strings.TrimSpace(args["dest"])
		if dest == "" {
			dest = defaultManagedSkillDir()
		}
		return UninstallManagedSkill(dest, strings.TrimSpace(args["name"]))
	case "rollback", "restore":
		if !truthy(args["confirm_rollback"]) {
			return "", fmt.Errorf("回滚 Skill 前必须显式传 confirm_rollback=true")
		}
		dest := strings.TrimSpace(args["dest"])
		if dest == "" {
			dest = defaultManagedSkillDir()
		}
		return RollbackManagedSkill(dest, strings.TrimSpace(args["name"]), strings.TrimSpace(firstValue(args, "version", "digest")))
	case "audit", "check":
		dest := strings.TrimSpace(args["dest"])
		if dest == "" {
			dest = defaultManagedSkillDir()
		}
		return AuditSkillRoot(dest)
	default:
		return "", fmt.Errorf("未知 skill_market action: %s；支持 search/inspect/install/import/managed/check_updates/update/pin/unpin/uninstall/rollback/audit", action)
	}
}

func SearchMarkets(ctx context.Context, query, market string, limit int) ([]MarketSearchResult, error) {
	market = strings.ToLower(strings.TrimSpace(market))
	if market == "" {
		market = "all"
	}
	type response struct {
		items []MarketSearchResult
		err   error
	}
	var searches []func() response
	if market == "all" || market == "clawhub" || market == "claw" {
		searches = append(searches, func() response {
			items, err := searchClawHub(ctx, query, limit)
			return response{items: items, err: err}
		})
	}
	if market == "all" || market == "skills" || market == "skills.sh" {
		searches = append(searches, func() response {
			items, err := searchSkillsSH(ctx, query, limit)
			return response{items: items, err: err}
		})
	}
	if len(searches) == 0 {
		return nil, fmt.Errorf("未知市场 %q；支持 all/clawhub/skills.sh", market)
	}
	ch := make(chan response, len(searches))
	for _, search := range searches {
		go func(fn func() response) { ch <- fn() }(search)
	}
	var out []MarketSearchResult
	var errors []string
	for range searches {
		res := <-ch
		if res.err != nil {
			errors = append(errors, res.err.Error())
			continue
		}
		out = append(out, res.items...)
	}
	if len(out) == 0 && len(errors) > 0 {
		return nil, fmt.Errorf("市场搜索失败: %s", strings.Join(errors, "；"))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Market != out[j].Market {
			return out[i].Market < out[j].Market
		}
		if out[i].Downloads+out[i].Installs != out[j].Downloads+out[j].Installs {
			return out[i].Downloads+out[i].Installs > out[j].Downloads+out[j].Installs
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func searchClawHub(ctx context.Context, query string, limit int) ([]MarketSearchResult, error) {
	endpoint := clawHubBaseURL + "/api/v1/search?q=" + url.QueryEscape(query) + "&limit=" + strconv.Itoa(limit)
	var payload struct {
		Results []struct {
			Score       float64 `json:"score"`
			Slug        string  `json:"slug"`
			DisplayName string  `json:"displayName"`
			Summary     string  `json:"summary"`
			Downloads   int     `json:"downloads"`
			OwnerHandle string  `json:"ownerHandle"`
		} `json:"results"`
	}
	if err := getJSON(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("ClawHub: %w", err)
	}
	out := make([]MarketSearchResult, 0, len(payload.Results))
	for _, item := range payload.Results {
		out = append(out, MarketSearchResult{Market: "clawhub", Ref: "clawhub:" + item.Slug, Name: valueOr(item.DisplayName, item.Slug), Description: item.Summary, Author: item.OwnerHandle, Downloads: item.Downloads, Score: item.Score})
	}
	return out, nil
}

func searchSkillsSH(ctx context.Context, query string, limit int) ([]MarketSearchResult, error) {
	endpoint := skillsSearchURL + "?q=" + url.QueryEscape(query)
	var payload struct {
		Skills []struct {
			ID        string `json:"id"`
			SkillID   string `json:"skillId"`
			Name      string `json:"name"`
			Source    string `json:"source"`
			Installs  int    `json:"installs"`
			Duplicate bool   `json:"isDuplicate"`
		} `json:"skills"`
	}
	if err := getJSON(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("skills.sh: %w", err)
	}
	out := make([]MarketSearchResult, 0, minInt(limit, len(payload.Skills)))
	for _, item := range payload.Skills {
		if item.Duplicate || !repoPattern.MatchString(item.Source) || !safeSlugPattern.MatchString(item.SkillID) {
			continue
		}
		out = append(out, MarketSearchResult{Market: "skills.sh", Ref: "skills:" + item.Source + "@" + item.SkillID, Name: valueOr(item.Name, item.SkillID), Author: strings.SplitN(item.Source, "/", 2)[0], Installs: item.Installs, Description: "GitHub: " + item.Source})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func FormatSearchResults(query string, results []MarketSearchResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("未找到与 %q 匹配的 Skill。", query)
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Skill 市场搜索：%s\n", query))
	b.WriteString("先 inspect 审查，再 install；搜索不会安装或执行任何第三方代码。\n\n")
	for i, item := range results {
		popularity := ""
		if item.Downloads > 0 {
			popularity = fmt.Sprintf(" · %d downloads", item.Downloads)
		} else if item.Installs > 0 {
			popularity = fmt.Sprintf(" · %d installs", item.Installs)
		}
		b.WriteString(fmt.Sprintf("%d. [%s] %s%s\n   %s\n   source=%s\n", i+1, item.Market, item.Name, popularity, truncateText(item.Description, 180), item.Ref))
	}
	return strings.TrimSpace(b.String())
}

func InspectMarketSkill(ctx context.Context, ref string) (string, error) {
	market, source, err := parseMarketRef(ref)
	if err != nil {
		return "", err
	}
	if market == "local" {
		files, absolutePath, version, err := loadLocalSkillPackage(source)
		if err != nil {
			return "", err
		}
		audit, err := auditSkillFiles(files)
		if err != nil {
			return "", err
		}
		warningText := "无高风险静态模式"
		if len(audit.Warnings) > 0 {
			warningText = strings.Join(audit.Warnings, "、")
		}
		return fmt.Sprintf("本地 Skill 包: %s\nSkill: %s\n简介: %s\n文件: %d，大小: %d bytes\n版本: %s\n静态审查: %s\n提示: .skill 可为 ZIP 容器，也可为单文件 YAML frontmatter + Markdown；检查不会执行包内脚本。", absolutePath, audit.Name, audit.Description, audit.Files, audit.Bytes, version, warningText), nil
	}
	if market == "clawhub" {
		meta, err := fetchClawMeta(ctx, source)
		if err != nil {
			return "", err
		}
		status := "未标记"
		if meta.Moderation.MalwareBlocked {
			status = "已阻止：恶意"
		} else if meta.Moderation.Suspicious {
			status = "可疑：安装需要 acknowledge_risk=true"
		}
		return fmt.Sprintf("ClawHub Skill: %s\n作者: %s\n版本: %s\n下载/安装/Stars: %d/%d/%d\n安全状态: %s\n简介: %s\n\nSKILL.md 预览:\n%s", meta.Skill.DisplayName, meta.Owner.Handle, meta.LatestVersion.Version, meta.Skill.Stats.Downloads, meta.Skill.Stats.Installs, meta.Skill.Stats.Stars, status, meta.Skill.Summary, truncateText(meta.Skill.Description, 4000)), nil
	}
	repo, skillID, _, err := parseGitSkillSource(source)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("skills.sh / GitHub Skill\n仓库: https://github.com/%s\nSkill: %s\n安装引用: skills:%s@%s\n\n安装时 DeepSentry 会只下载该 Skill 目录，并在落盘前检查 SKILL.md、路径逃逸、文件数量/体积和危险指令模式。", repo, skillID, repo, skillID), nil
}

type clawMeta struct {
	Skill struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"displayName"`
		Summary     string `json:"summary"`
		Description string `json:"description"`
		Stats       struct {
			Downloads int `json:"downloads"`
			Installs  int `json:"installs"`
			Stars     int `json:"stars"`
		} `json:"stats"`
	} `json:"skill"`
	LatestVersion struct {
		Version string `json:"version"`
		License string `json:"license"`
	} `json:"latestVersion"`
	Owner struct {
		Handle string `json:"handle"`
	} `json:"owner"`
	Moderation struct {
		MalwareBlocked bool `json:"isMalwareBlocked"`
		Suspicious     bool `json:"isSuspicious"`
	} `json:"moderation"`
}

func fetchClawMeta(ctx context.Context, slug string) (clawMeta, error) {
	var meta clawMeta
	if !safeSlugPattern.MatchString(slug) {
		return meta, fmt.Errorf("无效 ClawHub slug: %q", slug)
	}
	if err := getJSON(ctx, clawHubBaseURL+"/api/v1/skills/"+url.PathEscape(slug), &meta); err != nil {
		return meta, fmt.Errorf("读取 ClawHub 元数据失败: %w", err)
	}
	return meta, nil
}

func InstallMarketSkill(ctx context.Context, ref, destRoot string, force, acknowledgeRisk bool) (string, error) {
	market, source, err := parseMarketRef(ref)
	if err != nil {
		return "", err
	}
	destRoot = expandSourcePath(destRoot)
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return "", fmt.Errorf("创建 Skill 目录失败: %w", err)
	}
	var files []installFile
	var version string
	lockedSource := ref
	if market == "local" {
		var absolutePath string
		files, absolutePath, version, err = loadLocalSkillPackage(source)
		if err != nil {
			return "", err
		}
		lockedSource = "local:" + absolutePath
	} else if market == "clawhub" {
		meta, err := fetchClawMeta(ctx, source)
		if err != nil {
			return "", err
		}
		if meta.Moderation.MalwareBlocked {
			return "", fmt.Errorf("ClawHub 已将 %s 标记为恶意，拒绝安装", source)
		}
		if meta.Moderation.Suspicious && !acknowledgeRisk {
			return "", fmt.Errorf("ClawHub 将 %s 标记为可疑；审查后如仍需安装，请显式传 acknowledge_risk=true", source)
		}
		version = meta.LatestVersion.Version
		if version == "" {
			return "", fmt.Errorf("ClawHub 未返回可安装版本")
		}
		archiveURL := clawHubBaseURL + "/api/v1/download?slug=" + url.QueryEscape(source) + "&version=" + url.QueryEscape(version)
		raw, err := getBytes(ctx, archiveURL, maxMarketplaceBytes)
		if err != nil {
			return "", fmt.Errorf("下载 ClawHub Skill 失败: %w", err)
		}
		files, err = filesFromZip(raw)
		if err != nil {
			return "", err
		}
	} else {
		repo, skillID, refName, err := parseGitSkillSource(source)
		if err != nil {
			return "", err
		}
		files, version, err = fetchGitHubSkill(ctx, repo, skillID, refName)
		if err != nil {
			return "", err
		}
	}

	return installFiles(destRoot, market, lockedSource, version, files, force, acknowledgeRisk)
}

// loadLocalSkillPackage imports a local .skill package without executing it.
// A .skill file may be a ZIP container holding one SKILL.md tree, or a plain
// UTF-8 SKILL.md document stored with the portable .skill suffix. Supporting
// both forms is intentional: the open Agent Skills spec standardizes the
// directory/SKILL.md layout but does not standardize a .skill container.
func loadLocalSkillPackage(source string) ([]installFile, string, string, error) {
	source = strings.TrimSpace(source)
	source = strings.TrimPrefix(source, "local:")
	source = strings.TrimPrefix(source, "file:")
	source = strings.TrimPrefix(source, "//")
	if source == "" {
		return nil, "", "", fmt.Errorf("本地 Skill 路径为空")
	}
	absolutePath, err := filepath.Abs(expandSourcePath(source))
	if err != nil {
		return nil, "", "", fmt.Errorf("解析本地 Skill 路径失败: %w", err)
	}
	absolutePath = filepath.Clean(absolutePath)
	info, err := os.Lstat(absolutePath)
	if err != nil {
		return nil, "", "", fmt.Errorf("读取本地 Skill 包失败: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, "", "", fmt.Errorf("本地 Skill 来源必须是普通文件，不接受符号链接或目录: %s", absolutePath)
	}
	if info.Size() > maxMarketplaceBytes {
		return nil, "", "", fmt.Errorf("本地 Skill 包超过 %d MiB 限制", maxMarketplaceBytes>>20)
	}
	file, err := os.Open(absolutePath)
	if err != nil {
		return nil, "", "", err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxMarketplaceBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, "", "", readErr
	}
	if closeErr != nil {
		return nil, "", "", closeErr
	}
	if len(raw) > maxMarketplaceBytes {
		return nil, "", "", fmt.Errorf("本地 Skill 包超过 %d MiB 限制", maxMarketplaceBytes>>20)
	}

	var files []installFile
	if bytes.HasPrefix(raw, []byte{'P', 'K', 3, 4}) || bytes.HasPrefix(raw, []byte{'P', 'K', 5, 6}) {
		files, err = filesFromZip(raw)
	} else {
		lowerExt := strings.ToLower(filepath.Ext(absolutePath))
		if lowerExt == ".zip" {
			return nil, "", "", fmt.Errorf("无效 Skill zip: 文件缺少 ZIP 头")
		}
		if bytes.IndexByte(raw, 0) >= 0 || !strings.HasPrefix(strings.TrimSpace(string(raw)), "---") {
			return nil, "", "", fmt.Errorf("本地 .skill 既不是有效 ZIP，也不是带 YAML frontmatter 的文本 Skill")
		}
		files = []installFile{{Path: "SKILL.md", Data: raw}}
	}
	if err != nil {
		return nil, "", "", err
	}
	digest := digestFiles(files)
	version := "sha256:" + digest[:12]
	return files, absolutePath, version, nil
}

func auditSkillFiles(files []installFile) (SkillAudit, error) {
	stage, err := os.MkdirTemp("", ".deepsentry-skill-inspect-")
	if err != nil {
		return SkillAudit{}, err
	}
	defer os.RemoveAll(stage)
	for _, file := range files {
		clean, err := cleanRelativePath(file.Path)
		if err != nil {
			return SkillAudit{}, err
		}
		path := filepath.Join(stage, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return SkillAudit{}, err
		}
		if err := os.WriteFile(path, file.Data, 0o600); err != nil {
			return SkillAudit{}, err
		}
	}
	return AuditSkillDir(stage)
}

func filesFromZip(raw []byte) ([]installFile, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("无效 Skill zip: %w", err)
	}
	if len(zr.File) > maxSkillFiles {
		return nil, fmt.Errorf("Skill 文件过多: %d > %d", len(zr.File), maxSkillFiles)
	}
	var files []installFile
	var total int64
	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("Skill 包含符号链接，拒绝安装: %s", entry.Name)
		}
		path, err := cleanRelativePath(entry.Name)
		if err != nil {
			return nil, err
		}
		if entry.UncompressedSize64 > maxSkillFileBytes {
			return nil, fmt.Errorf("Skill 文件过大: %s", path)
		}
		rc, err := entry.Open()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, maxSkillFileBytes+1))
		_ = rc.Close()
		if readErr != nil {
			return nil, readErr
		}
		total += int64(len(data))
		if total > maxSkillTotalBytes {
			return nil, fmt.Errorf("Skill 解压后总大小超过 %d MiB", maxSkillTotalBytes>>20)
		}
		files = append(files, installFile{Path: path, Data: data})
	}
	return normalizeSkillFiles(files)
}

type githubRepo struct {
	DefaultBranch string `json:"default_branch"`
}

type githubTree struct {
	SHA       string            `json:"sha"`
	Truncated bool              `json:"truncated"`
	Tree      []githubTreeEntry `json:"tree"`
}

type githubTreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Mode string `json:"mode"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

type githubBlob struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	Size     int64  `json:"size"`
}

func fetchGitHubSkill(ctx context.Context, repo, skillID, refName string) ([]installFile, string, error) {
	if !repoPattern.MatchString(repo) || !safeSlugPattern.MatchString(skillID) {
		return nil, "", fmt.Errorf("无效 GitHub Skill 引用")
	}
	if refName == "" {
		var info githubRepo
		if err := getJSON(ctx, githubAPIBaseURL+"/repos/"+repo, &info); err != nil {
			return nil, "", fmt.Errorf("读取 GitHub 仓库失败: %w", err)
		}
		refName = info.DefaultBranch
	}
	if refName == "" || strings.ContainsAny(refName, "\x00\r\n") {
		return nil, "", fmt.Errorf("无效 Git ref")
	}
	var tree githubTree
	endpoint := githubAPIBaseURL + "/repos/" + repo + "/git/trees/" + url.PathEscape(refName) + "?recursive=1"
	if err := getJSON(ctx, endpoint, &tree); err != nil {
		return nil, "", fmt.Errorf("读取 GitHub 文件树失败: %w", err)
	}
	if tree.Truncated {
		return nil, "", fmt.Errorf("GitHub 仓库文件树过大且被截断；请使用更小的 Skill 仓库")
	}
	skillDir, err := selectGitHubSkillDir(repo, skillID, tree.Tree)
	if err != nil {
		return nil, "", err
	}
	selected := selectedGitHubBlobCount(skillDir, tree.Tree)
	var blobErr error
	if githubToken() != "" || selected <= maxUnauthBlobFallback {
		files, err := fetchGitHubSkillBlobs(ctx, repo, skillDir, tree.Tree)
		if err == nil {
			return files, valueOr(tree.SHA, refName), nil
		}
		blobErr = err
	}
	archiveEndpoint := githubAPIBaseURL + "/repos/" + repo + "/zipball/" + url.PathEscape(refName)
	archive, archiveErr := getBytes(ctx, archiveEndpoint, maxGitHubArchiveBytes)
	if archiveErr == nil {
		files, err := filesFromGitHubArchive(archive, skillDir)
		if err != nil {
			return nil, "", fmt.Errorf("解析 GitHub Skill 归档失败: %w", err)
		}
		if err := verifyGitHubArchiveFiles(skillDir, tree.Tree, files); err != nil {
			return nil, "", fmt.Errorf("GitHub Skill 归档完整性校验失败: %w", err)
		}
		return files, valueOr(tree.SHA, refName), nil
	}
	if blobErr == nil && githubToken() == "" && selected > maxUnauthBlobFallback {
		return nil, "", fmt.Errorf("下载 GitHub 归档失败: %v；目标 Skill 有 %d 个文件，为避免匿名 API 限流已停止逐文件回退。可配置 GITHUB_TOKEN/GH_TOKEN 后重试", archiveErr, selected)
	}
	if blobErr != nil {
		return nil, "", fmt.Errorf("GitHub Blob API 下载失败: %v；归档回退也失败: %w", blobErr, archiveErr)
	}
	return nil, "", fmt.Errorf("下载 GitHub Skill 失败: %w", archiveErr)
}

func fetchGitHubSkillBlobs(ctx context.Context, repo, skillDir string, entries []githubTreeEntry) ([]installFile, error) {
	prefix := ""
	if skillDir != "." {
		prefix = strings.TrimSuffix(skillDir, "/") + "/"
	}
	var files []installFile
	var total int64
	for _, entry := range entries {
		if entry.Type != "blob" || !strings.HasPrefix(entry.Path, prefix) {
			continue
		}
		rel := strings.TrimPrefix(entry.Path, prefix)
		if entry.Mode == "120000" {
			return nil, fmt.Errorf("Skill 包含符号链接，拒绝安装: %s", rel)
		}
		if rel == "" || entry.Size > maxSkillFileBytes {
			if entry.Size > maxSkillFileBytes {
				return nil, fmt.Errorf("Skill 文件过大: %s", rel)
			}
			continue
		}
		clean, err := cleanRelativePath(rel)
		if err != nil {
			return nil, err
		}
		if len(files) >= maxSkillFiles || total+entry.Size > maxSkillTotalBytes {
			return nil, fmt.Errorf("Skill 超过文件数量或总大小限制")
		}
		data, err := fetchGitHubBlob(ctx, repo, entry.SHA, maxSkillFileBytes)
		if err != nil {
			return nil, fmt.Errorf("下载 %s 失败: %w", entry.Path, err)
		}
		total += int64(len(data))
		if total > maxSkillTotalBytes {
			return nil, fmt.Errorf("Skill 解码后总大小超过 %d MiB", maxSkillTotalBytes>>20)
		}
		files = append(files, installFile{Path: clean, Data: data})
	}
	return normalizeSkillFiles(files)
}

func selectedGitHubBlobCount(skillDir string, entries []githubTreeEntry) int {
	prefix := ""
	if skillDir != "." {
		prefix = strings.TrimSuffix(skillDir, "/") + "/"
	}
	count := 0
	for _, entry := range entries {
		if entry.Type == "blob" && strings.HasPrefix(entry.Path, prefix) {
			count++
		}
	}
	return count
}

func filesFromGitHubArchive(raw []byte, skillDir string) ([]installFile, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("无效 GitHub zip: %w", err)
	}
	prefix := ""
	if skillDir != "." {
		prefix = strings.TrimSuffix(filepath.ToSlash(skillDir), "/") + "/"
	}
	files := make([]installFile, 0, minInt(len(zr.File), maxSkillFiles))
	var total int64
	for _, entry := range zr.File {
		archivePath, err := cleanRelativePath(entry.Name)
		if err != nil {
			return nil, err
		}
		_, repoPath, ok := strings.Cut(archivePath, "/")
		if !ok || repoPath == "" || (!strings.HasPrefix(repoPath, prefix) && prefix != "") {
			continue
		}
		rel := strings.TrimPrefix(repoPath, prefix)
		if rel == "" || entry.FileInfo().IsDir() {
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("Skill 包含符号链接，拒绝安装: %s", rel)
		}
		clean, err := cleanRelativePath(rel)
		if err != nil {
			return nil, err
		}
		if entry.UncompressedSize64 > maxSkillFileBytes {
			return nil, fmt.Errorf("Skill 文件过大: %s", clean)
		}
		if len(files) >= maxSkillFiles || total+int64(entry.UncompressedSize64) > maxSkillTotalBytes {
			return nil, fmt.Errorf("Skill 超过文件数量或总大小限制")
		}
		rc, err := entry.Open()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, maxSkillFileBytes+1))
		_ = rc.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(data) > maxSkillFileBytes {
			return nil, fmt.Errorf("Skill 文件过大: %s", clean)
		}
		total += int64(len(data))
		files = append(files, installFile{Path: clean, Data: data})
	}
	return normalizeSkillFiles(files)
}

func verifyGitHubArchiveFiles(skillDir string, entries []githubTreeEntry, files []installFile) error {
	prefix := ""
	if skillDir != "." {
		prefix = strings.TrimSuffix(skillDir, "/") + "/"
	}
	expected := make(map[string]githubTreeEntry)
	for _, entry := range entries {
		if entry.Type != "blob" || !strings.HasPrefix(entry.Path, prefix) {
			continue
		}
		rel := strings.TrimPrefix(entry.Path, prefix)
		clean, err := cleanRelativePath(rel)
		if err != nil {
			return err
		}
		expected[clean] = entry
	}
	if len(files) != len(expected) {
		return fmt.Errorf("文件数不一致: 文件树 %d，归档 %d", len(expected), len(files))
	}
	for _, file := range files {
		entry, ok := expected[filepath.ToSlash(file.Path)]
		if !ok {
			return fmt.Errorf("归档包含文件树中不存在的文件: %s", file.Path)
		}
		if entry.Size > 0 && entry.Size != int64(len(file.Data)) {
			return fmt.Errorf("文件大小不一致: %s", file.Path)
		}
		if len(entry.SHA) == 40 {
			// #nosec G401 -- this reproduces Git's blob object identifier for
			// transport integrity; it is not used for a security signature.
			h := sha1.New()
			_, _ = fmt.Fprintf(h, "blob %d%c", len(file.Data), byte(0))
			_, _ = h.Write(file.Data)
			if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), entry.SHA) {
				return fmt.Errorf("Git blob 摘要不一致: %s", file.Path)
			}
		}
	}
	return nil
}

func selectGitHubSkillDir(repo, skillID string, entries []githubTreeEntry) (string, error) {
	var candidates []string
	available := make(map[string]struct{})
	repoName := filepath.Base(repo)
	for _, entry := range entries {
		if entry.Type != "blob" || !strings.EqualFold(filepath.Base(entry.Path), "SKILL.md") {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(entry.Path))
		base := filepath.Base(dir)
		if dir == "." {
			base = repoName
		}
		if safeSlugPattern.MatchString(base) {
			available[base] = struct{}{}
		}
		if strings.EqualFold(base, skillID) {
			candidates = append(candidates, dir)
		}
	}
	if len(candidates) == 0 {
		names := make([]string, 0, len(available))
		for name := range available {
			if !strings.EqualFold(name, "skills") && !strings.EqualFold(name, "skill") {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		hint := ""
		if len(names) > 0 {
			if len(names) > 12 {
				names = names[:12]
			}
			hint = "；仓库当前可用 Skill 目录: " + strings.Join(names, ", ") + "。该市场索引可能已过期，请改用仓库当前的 Skill 名"
		}
		return "", fmt.Errorf("仓库 %s 中未找到 Skill %s%s", repo, skillID, hint)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		si, sj := githubSkillDirScore(candidates[i]), githubSkillDirScore(candidates[j])
		if si != sj {
			return si < sj
		}
		return candidates[i] < candidates[j]
	})
	return candidates[0], nil
}

func githubSkillDirScore(dir string) int {
	dir = strings.ToLower(filepath.ToSlash(dir))
	switch {
	case strings.HasPrefix(dir, ".agents/skills/"):
		return 0 // OpenAI/Codex-compatible generated Skill.
	case strings.HasPrefix(dir, "skills/"):
		return 10
	case strings.HasPrefix(dir, ".claude/skills/"):
		return 20
	case dir == ".":
		return 30
	default:
		return 40 + strings.Count(dir, "/")
	}
}

func fetchGitHubBlob(ctx context.Context, repo, sha string, limit int64) ([]byte, error) {
	if !repoPattern.MatchString(repo) || !githubSHAPattern.MatchString(sha) {
		return nil, fmt.Errorf("无效 GitHub blob 引用")
	}
	var blob githubBlob
	if err := getJSON(ctx, githubAPIBaseURL+"/repos/"+repo+"/git/blobs/"+sha, &blob); err != nil {
		return nil, err
	}
	if blob.Size > limit {
		return nil, fmt.Errorf("blob 超过大小限制 %d bytes", limit)
	}
	if !strings.EqualFold(blob.Encoding, "base64") {
		return nil, fmt.Errorf("不支持的 GitHub blob 编码: %s", blob.Encoding)
	}
	data, err := base64.StdEncoding.DecodeString(blob.Content)
	if err != nil {
		return nil, fmt.Errorf("GitHub blob base64 无效: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("blob 超过大小限制 %d bytes", limit)
	}
	if blob.Size > 0 && blob.Size != int64(len(data)) {
		return nil, fmt.Errorf("GitHub blob 大小校验失败: 声明 %d，实际 %d", blob.Size, len(data))
	}
	return data, nil
}

func normalizeSkillFiles(files []installFile) ([]installFile, error) {
	root := ""
	for _, file := range files {
		if strings.EqualFold(file.Path, "SKILL.md") {
			root = "."
			break
		}
		if strings.EqualFold(filepath.Base(file.Path), "SKILL.md") {
			dir := filepath.ToSlash(filepath.Dir(file.Path))
			if root != "" && root != dir {
				return nil, fmt.Errorf("Skill 包含多个 SKILL.md，无法确定安装根目录")
			}
			root = dir
		}
	}
	if root == "" {
		return nil, fmt.Errorf("Skill 包中缺少 SKILL.md")
	}
	if root == "." {
		return files, nil
	}
	prefix := strings.TrimSuffix(root, "/") + "/"
	out := make([]installFile, 0, len(files))
	for _, file := range files {
		if strings.HasPrefix(file.Path, prefix) {
			file.Path = strings.TrimPrefix(file.Path, prefix)
			out = append(out, file)
		}
	}
	return out, nil
}

func installFiles(destRoot, market, source, version string, files []installFile, force, acknowledgeRisk bool) (string, error) {
	stage, err := os.MkdirTemp(destRoot, ".deepsentry-skill-stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	for _, file := range files {
		clean, err := cleanRelativePath(file.Path)
		if err != nil {
			return "", err
		}
		path := filepath.Join(stage, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := writeDurableFile(path, file.Data, 0o644); err != nil {
			return "", err
		}
	}
	if err := syncTreeDirectories(stage); err != nil {
		return "", fmt.Errorf("同步 Skill 暂存目录失败: %w", err)
	}
	audit, err := AuditSkillDir(stage)
	if err != nil {
		return "", fmt.Errorf("安装前审查失败: %w", err)
	}
	if len(audit.Warnings) > 0 && !acknowledgeRisk {
		return "", fmt.Errorf("Skill 静态审查发现需要人工复核的风险: %s；审查后如仍需安装，请显式传 acknowledge_risk=true", strings.Join(audit.Warnings, "、"))
	}
	target := filepath.Join(destRoot, audit.Name)
	lock, err := readManagedLock(destRoot)
	if err != nil {
		return "", err
	}
	previous := lock.Skills[audit.Name]
	var backupPath string
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("目标 Skill 路径是符号链接，拒绝覆盖: %s", target)
		}
		if !force {
			return "", fmt.Errorf("Skill 已存在: %s；确认来源后可使用 force=true 覆盖", target)
		}
		backupPath, err = newManagedBackupPath(destRoot, audit.Name, "version")
		if err != nil {
			return "", err
		}
		if err := os.Rename(target, backupPath); err != nil {
			return "", fmt.Errorf("备份旧 Skill 失败: %w", err)
		}
		if err := os.Rename(stage, target); err != nil {
			_ = os.Rename(backupPath, target)
			return "", fmt.Errorf("安装新 Skill 失败: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	} else if err := os.Rename(stage, target); err != nil {
		return "", fmt.Errorf("安装 Skill 失败: %w", err)
	}
	if err := syncDirectory(destRoot); err != nil {
		_ = os.RemoveAll(target)
		if backupPath != "" {
			_ = os.Rename(backupPath, target)
		}
		return "", fmt.Errorf("Skill 目录落盘失败，已回退: %w", err)
	}
	landedAudit, err := AuditSkillDir(target)
	if err != nil || landedAudit.Name != audit.Name || landedAudit.Files != audit.Files || landedAudit.Bytes != audit.Bytes {
		_ = os.RemoveAll(target)
		if backupPath != "" {
			_ = os.Rename(backupPath, target)
		}
		_ = syncDirectory(destRoot)
		if err != nil {
			return "", fmt.Errorf("落盘后复核失败，已回退: %w", err)
		}
		return "", fmt.Errorf("落盘后复核不一致，已回退")
	}

	digest := digestFiles(files)
	backups := append([]managedBackup(nil), previous.Backups...)
	if backupPath != "" {
		rel, _ := filepath.Rel(destRoot, backupPath)
		backups = append(backups, managedBackup{
			Path: filepath.ToSlash(rel), Source: previous.Source, Market: previous.Market,
			Version: previous.Version, Digest: previous.Digest, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		})
	}
	lock.Skills[audit.Name] = managedSkill{
		Source: source, Market: market, Version: version, Digest: digest,
		InstalledAt: time.Now().UTC().Format(time.RFC3339), Warnings: audit.Warnings,
		Pinned: previous.Pinned, Backups: backups,
	}
	if err := writeManagedLock(destRoot, lock); err != nil {
		_ = os.RemoveAll(target)
		if backupPath != "" {
			_ = os.Rename(backupPath, target)
		}
		_ = syncDirectory(destRoot)
		return "", fmt.Errorf("写入来源锁失败，已回退安装: %w", err)
	}
	warningText := "无高风险静态模式"
	if len(audit.Warnings) > 0 {
		warningText = strings.Join(audit.Warnings, "；")
	}
	backupText := ""
	if backupPath != "" {
		backupText = "\n旧版本备份: " + backupPath
	}
	return fmt.Sprintf("已安装 Skill: %s\n来源: %s\n版本/提交: %s\n文件: %d，大小: %d bytes\nSHA256: %s\n静态审查: %s\n原子落盘: 已同步并复核\n路径: %s%s\n提示: 安装程序应立即热刷新当前会话 Catalog。", audit.Name, source, version, audit.Files, audit.Bytes, digest, warningText, target, backupText), nil
}

func writeDurableFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func syncTreeDirectories(root string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	var dirs []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if err := syncDirectory(dir); err != nil {
			return err
		}
	}
	return nil
}

func newManagedBackupPath(root, name, label string) (string, error) {
	if !safeSlugPattern.MatchString(name) {
		return "", fmt.Errorf("无效 Skill name: %q", name)
	}
	dir := filepath.Join(root, ".deepsentry-backups", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建 Skill 备份目录失败: %w", err)
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%s", label, time.Now().UTC().Format("20060102T150405.000000000Z"))), nil
}

func AuditSkillDir(dir string) (SkillAudit, error) {
	var audit SkillAudit
	root, err := os.OpenRoot(dir)
	if err != nil {
		return audit, fmt.Errorf("打开 Skill 根目录失败: %w", err)
	}
	defer root.Close()
	data, err := root.ReadFile("SKILL.md")
	if err != nil {
		return audit, fmt.Errorf("缺少可读 SKILL.md: %w", err)
	}
	if len(data) > 1<<20 {
		return audit, fmt.Errorf("SKILL.md 超过 1 MiB")
	}
	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return audit, fmt.Errorf("SKILL.md 缺少 YAML frontmatter")
	}
	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 || yaml.Unmarshal([]byte(parts[1]), &meta) != nil {
		return audit, fmt.Errorf("SKILL.md frontmatter 无效")
	}
	meta.Name = strings.TrimSpace(meta.Name)
	meta.Description = strings.TrimSpace(meta.Description)
	if !safeSlugPattern.MatchString(meta.Name) {
		return audit, fmt.Errorf("Skill name 无效: %q", meta.Name)
	}
	if meta.Description == "" || len([]rune(meta.Description)) > 1024 {
		return audit, fmt.Errorf("Skill description 必须存在且不超过 1024 字符")
	}
	audit.Name, audit.Description = meta.Name, meta.Description
	warnings := make(map[string]struct{})
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Skill 包含符号链接: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		audit.Files++
		if audit.Files > maxSkillFiles {
			return fmt.Errorf("Skill 文件数量超过 %d", maxSkillFiles)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		audit.Bytes += info.Size()
		if info.Size() > maxSkillFileBytes || audit.Bytes > maxSkillTotalBytes {
			return fmt.Errorf("Skill 超过文件大小限制")
		}
		if info.Size() > 1<<20 {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		// Root-scoped access prevents a local symlink swap from escaping the
		// staged Skill directory between WalkDir and the content read.
		body, err := root.ReadFile(rel)
		if err != nil || bytes.IndexByte(body, 0) >= 0 {
			return nil
		}
		for _, pattern := range riskyPatterns {
			if pattern.re.Match(body) {
				warnings[pattern.label] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		return audit, err
	}
	for warning := range warnings {
		audit.Warnings = append(audit.Warnings, warning)
	}
	sort.Strings(audit.Warnings)
	return audit, nil
}

func AuditSkillRoot(root string) (string, error) {
	root = expandSourcePath(root)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "未安装用户级 Skill。", nil
		}
		return "", err
	}
	var b strings.Builder
	valid, invalid := 0, 0
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		audit, err := AuditSkillDir(filepath.Join(root, entry.Name()))
		if err != nil {
			invalid++
			b.WriteString(fmt.Sprintf("✗ %s: %v\n", entry.Name(), err))
			continue
		}
		valid++
		status := "通过"
		if len(audit.Warnings) > 0 {
			status = "需人工复核: " + strings.Join(audit.Warnings, "、")
		}
		b.WriteString(fmt.Sprintf("✓ %s: %s (%d files, %d bytes)\n", audit.Name, status, audit.Files, audit.Bytes))
	}
	if valid+invalid == 0 {
		return "未发现可审查的用户级 Skill。", nil
	}
	return fmt.Sprintf("Skill 审查：%d 有效，%d 无效\n%s", valid, invalid, strings.TrimSpace(b.String())), nil
}

func FormatManagedSkills(root string) (string, error) {
	lock, err := readManagedLock(expandSourcePath(root))
	if err != nil {
		return "", err
	}
	if len(lock.Skills) == 0 {
		return "没有由 DeepSentry 市场管理的 Skill。", nil
	}
	names := make([]string, 0, len(lock.Skills))
	for name := range lock.Skills {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		item := lock.Skills[name]
		state := "active"
		if item.Removed {
			state = "removed/recoverable"
		} else if item.Pinned {
			state = "pinned"
		}
		b.WriteString(fmt.Sprintf("- %s [%s] %s · %s · %s · backups=%d\n", name, item.Market, item.Version, state, item.Source, len(item.Backups)))
	}
	return "市场管理的 Skills:\n" + strings.TrimSpace(b.String()), nil
}

func CheckManagedUpdates(ctx context.Context, root, onlyName string) (string, error) {
	root = expandSourcePath(root)
	lock, err := readManagedLock(root)
	if err != nil {
		return "", err
	}
	names, err := selectedManagedSkillNames(lock, onlyName)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "没有由 DeepSentry 市场管理的 Skill。", nil
	}
	var b strings.Builder
	updates := 0
	for _, name := range names {
		item := lock.Skills[name]
		if item.Removed {
			fmt.Fprintf(&b, "- %s: 已卸载（可回滚）\n", name)
			continue
		}
		remote, err := managedRemoteVersion(ctx, item)
		if err != nil {
			fmt.Fprintf(&b, "- %s: 检查失败: %v\n", name, err)
			continue
		}
		state := "最新"
		if remote != "" && remote != item.Version {
			state = "可更新"
			updates++
		}
		if item.Pinned {
			state += "（已冻结，不自动更新）"
		}
		fmt.Fprintf(&b, "- %s: %s · current=%s · remote=%s\n", name, state, item.Version, remote)
	}
	return fmt.Sprintf("Skill 更新检查：%d 个可更新\n%s", updates, strings.TrimSpace(b.String())), nil
}

func UpdateManagedSkills(ctx context.Context, root, onlyName string, acknowledgeRisk bool) (string, error) {
	root = expandSourcePath(root)
	lock, err := readManagedLock(root)
	if err != nil {
		return "", err
	}
	names, err := selectedManagedSkillNames(lock, onlyName)
	if err != nil {
		return "", err
	}
	var outputs []string
	for _, name := range names {
		item := lock.Skills[name]
		if item.Removed {
			outputs = append(outputs, fmt.Sprintf("- %s: 已卸载，跳过；可先 rollback", name))
			continue
		}
		if item.Pinned {
			outputs = append(outputs, fmt.Sprintf("- %s: 已冻结，跳过", name))
			continue
		}
		remote, err := managedRemoteVersion(ctx, item)
		if err != nil {
			outputs = append(outputs, fmt.Sprintf("- %s: 检查失败: %v", name, err))
			continue
		}
		if remote == item.Version {
			outputs = append(outputs, fmt.Sprintf("- %s: 已是最新版本 %s", name, item.Version))
			continue
		}
		out, err := InstallMarketSkill(ctx, item.Source, root, true, acknowledgeRisk)
		if err != nil {
			outputs = append(outputs, fmt.Sprintf("- %s: 更新失败: %v", name, err))
			continue
		}
		outputs = append(outputs, out)
	}
	if len(outputs) == 0 {
		return "没有可更新的市场管理 Skill。", nil
	}
	return strings.Join(outputs, "\n\n"), nil
}

func managedRemoteVersion(ctx context.Context, item managedSkill) (string, error) {
	market, source, err := parseMarketRef(item.Source)
	if err != nil {
		return "", err
	}
	if market == "clawhub" {
		meta, err := fetchClawMeta(ctx, source)
		if err != nil {
			return "", err
		}
		return meta.LatestVersion.Version, nil
	}
	if market == "local" {
		_, _, version, err := loadLocalSkillPackage(source)
		return version, err
	}
	repo, _, refName, err := parseGitSkillSource(source)
	if err != nil {
		return "", err
	}
	if refName == "" {
		var info githubRepo
		if err := getJSON(ctx, githubAPIBaseURL+"/repos/"+repo, &info); err != nil {
			return "", err
		}
		refName = info.DefaultBranch
	}
	var tree githubTree
	if err := getJSON(ctx, githubAPIBaseURL+"/repos/"+repo+"/git/trees/"+url.PathEscape(refName)+"?recursive=1", &tree); err != nil {
		return "", err
	}
	return valueOr(tree.SHA, refName), nil
}

func SetManagedSkillPinned(root, name string, pinned bool) (string, error) {
	root = expandSourcePath(root)
	if !safeSlugPattern.MatchString(name) {
		return "", fmt.Errorf("pin/unpin 需要有效 name")
	}
	lock, err := readManagedLock(root)
	if err != nil {
		return "", err
	}
	item, ok := lock.Skills[name]
	if !ok {
		return "", fmt.Errorf("未找到市场管理的 Skill: %s", name)
	}
	item.Pinned = pinned
	lock.Skills[name] = item
	if err := writeManagedLock(root, lock); err != nil {
		return "", err
	}
	return fmt.Sprintf("已%s Skill %s 在版本/提交 %s", mapSkillBool(pinned, "冻结", "解除冻结"), name, item.Version), nil
}

func UninstallManagedSkill(root, name string) (string, error) {
	root = expandSourcePath(root)
	if !safeSlugPattern.MatchString(name) {
		return "", fmt.Errorf("uninstall 需要有效 name")
	}
	lock, err := readManagedLock(root)
	if err != nil {
		return "", err
	}
	item, ok := lock.Skills[name]
	if !ok {
		return "", fmt.Errorf("仅允许卸载来源锁中由 DeepSentry 管理的 Skill: %s", name)
	}
	if item.Removed {
		return "", fmt.Errorf("Skill 已卸载: %s", name)
	}
	target := filepath.Join(root, name)
	info, err := os.Lstat(target)
	if err != nil {
		return "", fmt.Errorf("读取 Skill 失败: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("Skill 目标不是安全目录，拒绝卸载: %s", target)
	}
	backup, err := newManagedBackupPath(root, name, "removed")
	if err != nil {
		return "", err
	}
	if err := os.Rename(target, backup); err != nil {
		return "", fmt.Errorf("移动 Skill 到可恢复备份失败: %w", err)
	}
	rel, _ := filepath.Rel(root, backup)
	item.Backups = append(item.Backups, managedBackup{Path: filepath.ToSlash(rel), Source: item.Source, Market: item.Market, Version: item.Version, Digest: item.Digest, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	item.Removed = true
	lock.Skills[name] = item
	if err := writeManagedLock(root, lock); err != nil {
		_ = os.Rename(backup, target)
		return "", fmt.Errorf("写入来源锁失败，已恢复 Skill: %w", err)
	}
	return fmt.Sprintf("已卸载 Skill: %s\n未永久删除，备份位于: %s\n恢复: skill_market action=rollback name=%s confirm_rollback=true", name, backup, name), nil
}

func RollbackManagedSkill(root, name, selector string) (string, error) {
	root = expandSourcePath(root)
	if !safeSlugPattern.MatchString(name) {
		return "", fmt.Errorf("rollback 需要有效 name")
	}
	lock, err := readManagedLock(root)
	if err != nil {
		return "", err
	}
	item, ok := lock.Skills[name]
	if !ok || len(item.Backups) == 0 {
		return "", fmt.Errorf("Skill %s 没有可回滚备份", name)
	}
	index := -1
	for i := len(item.Backups) - 1; i >= 0; i-- {
		candidate := item.Backups[i]
		if selector == "" || candidate.Version == selector || strings.HasPrefix(candidate.Digest, selector) {
			index = i
			break
		}
	}
	if index < 0 {
		return "", fmt.Errorf("没有匹配版本/摘要 %q 的备份", selector)
	}
	chosen := item.Backups[index]
	backup, err := managedBackupAbsolutePath(root, chosen.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(backup)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("回滚备份不存在或不安全: %s", backup)
	}
	target := filepath.Join(root, name)
	var currentBackup string
	if currentInfo, statErr := os.Lstat(target); statErr == nil {
		if currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.IsDir() {
			return "", fmt.Errorf("当前 Skill 目标不安全: %s", target)
		}
		currentBackup, err = newManagedBackupPath(root, name, "rollback")
		if err != nil {
			return "", err
		}
		if err := os.Rename(target, currentBackup); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(statErr) {
		return "", statErr
	}
	if err := os.Rename(backup, target); err != nil {
		if currentBackup != "" {
			_ = os.Rename(currentBackup, target)
		}
		return "", fmt.Errorf("恢复备份失败: %w", err)
	}
	item.Backups = append(item.Backups[:index], item.Backups[index+1:]...)
	if currentBackup != "" {
		rel, _ := filepath.Rel(root, currentBackup)
		item.Backups = append(item.Backups, managedBackup{Path: filepath.ToSlash(rel), Source: item.Source, Market: item.Market, Version: item.Version, Digest: item.Digest, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	}
	if chosen.Source != "" {
		item.Source = chosen.Source
	}
	if chosen.Market != "" {
		item.Market = chosen.Market
	}
	item.Version, item.Digest, item.Removed = chosen.Version, chosen.Digest, false
	item.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	lock.Skills[name] = item
	if err := writeManagedLock(root, lock); err != nil {
		_ = os.Rename(target, backup)
		if currentBackup != "" {
			_ = os.Rename(currentBackup, target)
		}
		return "", fmt.Errorf("写入来源锁失败，已撤销回滚: %w", err)
	}
	return fmt.Sprintf("已回滚 Skill: %s\n版本/提交: %s\n路径: %s", name, item.Version, target), nil
}

func selectedManagedSkillNames(lock managedSkillLock, only string) ([]string, error) {
	if only != "" {
		if _, ok := lock.Skills[only]; !ok {
			return nil, fmt.Errorf("未找到市场管理的 Skill: %s", only)
		}
		return []string{only}, nil
	}
	names := make([]string, 0, len(lock.Skills))
	for name := range lock.Skills {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func managedBackupAbsolutePath(root, relative string) (string, error) {
	clean, err := cleanRelativePath(relative)
	if err != nil || !strings.HasPrefix(filepath.ToSlash(clean), ".deepsentry-backups/") {
		return "", fmt.Errorf("来源锁包含不安全备份路径")
	}
	abs := filepath.Join(root, filepath.FromSlash(clean))
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("备份路径逃逸 Skill 根目录")
	}
	return abs, nil
}

func mapSkillBool(value bool, yes, no string) string {
	if value {
		return yes
	}
	return no
}

func parseMarketRef(ref string) (market, source string, err error) {
	ref = strings.TrimSpace(ref)
	switch {
	case strings.HasPrefix(ref, "local:"):
		market, source = "local", strings.TrimPrefix(ref, "local:")
	case strings.HasPrefix(ref, "file:"):
		market, source = "local", strings.TrimPrefix(ref, "file:")
	case strings.HasPrefix(ref, "clawhub:"):
		market, source = "clawhub", strings.TrimPrefix(ref, "clawhub:")
	case strings.HasPrefix(ref, "skills:"):
		market, source = "skills.sh", strings.TrimPrefix(ref, "skills:")
	case strings.HasPrefix(ref, "github:"):
		market, source = "skills.sh", strings.TrimPrefix(ref, "github:")
	case looksLikeLocalSkillPath(ref):
		market, source = "local", ref
	case safeSlugPattern.MatchString(ref):
		market, source = "clawhub", ref
	default:
		return "", "", fmt.Errorf("无法识别 Skill 来源 %q；使用 /path/demo.skill、clawhub:<slug> 或 skills:<owner/repo>@<skill>", ref)
	}
	if strings.TrimSpace(source) == "" {
		return "", "", fmt.Errorf("Skill 来源为空")
	}
	return market, source, nil
}

func looksLikeLocalSkillPath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	return filepath.IsAbs(value) || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") ||
		strings.HasPrefix(value, "~/") || strings.ContainsAny(value, `/\\`) ||
		strings.HasSuffix(lower, ".skill") || strings.HasSuffix(lower, ".zip") || strings.EqualFold(filepath.Base(value), "SKILL.md")
}

func parseGitSkillSource(source string) (repo, skillID, refName string, err error) {
	main := source
	if hash := strings.LastIndex(main, "#"); hash >= 0 {
		refName = strings.TrimSpace(main[hash+1:])
		main = main[:hash]
	}
	at := strings.LastIndex(main, "@")
	if at <= 0 || at == len(main)-1 {
		return "", "", "", fmt.Errorf("GitHub Skill 引用格式应为 owner/repo@skill，可选 #ref")
	}
	repo, skillID = main[:at], main[at+1:]
	if !repoPattern.MatchString(repo) || !safeSlugPattern.MatchString(skillID) {
		return "", "", "", fmt.Errorf("无效 GitHub Skill 引用: %q", source)
	}
	return repo, skillID, refName, nil
}

func cleanRelativePath(path string) (string, error) {
	path = filepath.ToSlash(strings.TrimSpace(path))
	path = strings.TrimPrefix(path, "./")
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || strings.Contains(clean, "\\") {
		return "", fmt.Errorf("Skill 包含不安全路径: %q", path)
	}
	return clean, nil
}

func getJSON(ctx context.Context, endpoint string, dest interface{}) error {
	data, err := getBytes(ctx, endpoint, maxMarketplaceBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("响应 JSON 无效: %w", err)
	}
	return nil
}

func getBytes(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", marketplaceUserAgent)
	if strings.EqualFold(req.URL.Hostname(), "api.github.com") {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		token := githubToken()
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	} else {
		req.Header.Set("Accept", "application/json, application/zip;q=0.9, text/plain;q=0.8")
	}
	resp, err := marketplaceHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("响应超过大小限制 %d bytes", limit)
	}
	return data, nil
}

func githubToken() string {
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("GH_TOKEN"))
}

func readManagedLock(root string) (managedSkillLock, error) {
	lock := managedSkillLock{Version: 1, Skills: map[string]managedSkill{}}
	data, err := os.ReadFile(filepath.Join(root, marketplaceLockFile))
	if os.IsNotExist(err) {
		return lock, nil
	}
	if err != nil {
		return lock, err
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return lock, fmt.Errorf("市场锁文件损坏: %w", err)
	}
	if lock.Skills == nil {
		lock.Skills = map[string]managedSkill{}
	}
	return lock, nil
}

func writeManagedLock(root string, lock managedSkillLock) error {
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(root, ".market-lock-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(root, marketplaceLockFile)); err != nil {
		return err
	}
	return syncDirectory(root)
}

func digestFiles(files []installFile) string {
	sorted := append([]installFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, file := range sorted {
		_, _ = io.WriteString(h, filepath.ToSlash(file.Path))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(file.Data)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func defaultManagedSkillDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".deepsentry", "skills")
	}
	return filepath.Join(".deepsentry", "skills")
}

func firstValue(args map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(args[key]); value != "" {
			return value
		}
	}
	return ""
}

func boundedInt(raw string, fallback, minValue, maxValue int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func truthy(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func truncateText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
