package skills

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillMeta Skill 元数据（对标 deepagents SkillsMiddleware 的 SkillMetadata）
type SkillMeta struct {
	Name             string `yaml:"name"`
	Description      string `yaml:"description"`
	License          string `yaml:"license,omitempty"`
	AllowImplicit    bool   `yaml:"-"`
	UserInvocable    bool   `yaml:"-"`
	InvocationSource string `yaml:"-"`
	Path             string // SKILL.md 绝对路径
	Dir              string // skill 目录
}

// SkillCatalog Skill 目录索引（渐进式披露：仅元数据注入 prompt）
type SkillCatalog struct {
	Skills []SkillMeta
	// Sources records the effective roots used to build this catalog so a
	// successful marketplace install can refresh the current session in place.
	Sources []string
	// DisabledSkills is a case-insensitive denylist applied after discovery.
	// Keeping it on the shared catalog makes reloads and marketplace installs
	// preserve the user's per-skill policy.
	DisabledSkills []string
}

// LoadCatalog 从多个来源路径加载 Skill 目录
// sources 按优先级排列，后加载的同名 skill 覆盖先加载的。
// 例外：DeepSentry 内置的 FofaMap MCP playbook 不会被市场里带
// scripts/fofa_recon.py 的同名 Skill 覆盖。
func LoadCatalog(sources []string) (*SkillCatalog, error) {
	catalog := &SkillCatalog{}
	seen := make(map[string]int)

	for _, rawSource := range sources {
		src := normalizeSkillSource(rawSource)
		if src == "" {
			continue
		}
		catalog.Sources = append(catalog.Sources, src)
		entries, err := os.ReadDir(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("读取 skill 目录 %s 失败: %w", src, err)
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillDir := filepath.Join(src, entry.Name())
			skillFile := filepath.Join(skillDir, "SKILL.md")
			if _, err := os.Stat(skillFile); err != nil {
				continue
			}

			meta, err := parseSkillMeta(skillFile, skillDir)
			if err != nil {
				continue
			}

			key := strings.ToLower(meta.Name)
			if idx, exists := seen[key]; exists {
				if keepMCPAdapterSkill(catalog.Skills[idx], meta) {
					continue
				}
				catalog.Skills[idx] = meta
			} else {
				seen[key] = len(catalog.Skills)
				catalog.Skills = append(catalog.Skills, meta)
			}
		}
	}
	sort.SliceStable(catalog.Skills, func(i, j int) bool {
		return strings.ToLower(catalog.Skills[i].Name) < strings.ToLower(catalog.Skills[j].Name)
	})

	return catalog, nil
}

// ResolveSources returns stable, de-duplicated catalog roots. The managed
// user directory is always part of the defaults so marketplace installs are
// persistent and discoverable even when the project also declares custom
// roots. Users can still explicitly disable it.
func ResolveSources(configured, disabled []string) []string {
	sources := executableSkillSources()
	if len(configured) == 0 {
		sources = append(sources, "skills")
	} else {
		sources = append(sources, configured...)
	}
	sources = append(sources, defaultManagedSkillDir())
	blocked := make(map[string]bool, len(disabled))
	for _, rawValue := range disabled {
		value := normalizeSkillSource(rawValue)
		if value == "" {
			continue
		}
		blocked[value] = true
	}
	seen := make(map[string]bool, len(sources))
	out := make([]string, 0, len(sources))
	for _, rawSource := range sources {
		source := normalizeSkillSource(rawSource)
		if source == "" {
			continue
		}
		if blocked[source] || seen[source] {
			continue
		}
		seen[source] = true
		out = append(out, source)
	}
	return out
}

// Reload updates a shared catalog pointer without replacing the pointer held
// by SkillsMiddleware and the TUI.
func (c *SkillCatalog) Reload() error {
	return c.ReloadWithDisabled(c.DisabledSkills)
}

// ReloadWithDisabled re-discovers all sources and atomically reapplies the
// per-name denylist. This also restores newly enabled skills without restart.
func (c *SkillCatalog) ReloadWithDisabled(disabled []string) error {
	if c == nil {
		return fmt.Errorf("Skill 目录未初始化")
	}
	fresh, err := LoadCatalog(c.Sources)
	if err != nil {
		return err
	}
	fresh.ApplyDisabledSkills(disabled)
	*c = *fresh
	return nil
}

// ApplyDisabledSkills filters a freshly loaded catalog by name. Names are
// matched case-insensitively and retained so subsequent Reload calls keep the
// same policy.
func (c *SkillCatalog) ApplyDisabledSkills(disabled []string) {
	if c == nil {
		return
	}
	disabled = append([]string(nil), disabled...)
	seen := make(map[string]bool, len(disabled))
	c.DisabledSkills = c.DisabledSkills[:0]
	for _, name := range disabled {
		name = strings.TrimSpace(name)
		key := strings.ToLower(name)
		if name == "" || seen[key] {
			continue
		}
		seen[key] = true
		c.DisabledSkills = append(c.DisabledSkills, name)
	}
	if len(seen) == 0 {
		return
	}
	active := c.Skills[:0]
	if seen["*"] {
		c.Skills = active
		return
	}
	for _, meta := range c.Skills {
		if !seen[strings.ToLower(strings.TrimSpace(meta.Name))] {
			active = append(active, meta)
		}
	}
	c.Skills = active
}

func (c *SkillCatalog) IsDisabled(name string) bool {
	if c == nil {
		return false
	}
	for _, disabled := range c.DisabledSkills {
		if strings.TrimSpace(disabled) == "*" || strings.EqualFold(strings.TrimSpace(disabled), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// LoadSkillContent 加载完整 Skill 内容（按需披露）
func LoadSkillContent(meta SkillMeta) (string, error) {
	file, err := os.Open(meta.Path)
	if err != nil {
		return "", fmt.Errorf("读取 skill %s 失败: %w", meta.Name, err)
	}
	defer file.Close()
	const maxSkillInstructions = 4 << 20
	data, err := io.ReadAll(io.LimitReader(file, maxSkillInstructions+1))
	if err != nil {
		return "", fmt.Errorf("读取 skill %s 失败: %w", meta.Name, err)
	}
	if len(data) > maxSkillInstructions {
		return "", fmt.Errorf("skill %s 的 SKILL.md 超过 %d MiB 限制", meta.Name, maxSkillInstructions>>20)
	}
	return string(data), nil
}

// FindSkill 按名称查找 Skill
func (c *SkillCatalog) FindSkill(name string) (*SkillMeta, bool) {
	for i := range c.Skills {
		if strings.EqualFold(c.Skills[i].Name, strings.TrimSpace(name)) {
			return &c.Skills[i], true
		}
	}
	return nil, false
}

// FormatCatalogPrompt 生成 Skill 目录 prompt 片段
func (c *SkillCatalog) FormatCatalogPrompt() string {
	if len(c.Skills) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n【可用 Skills】\n")
	b.WriteString("目录只有名称和描述。任务明显匹配某个 name/description 时，先调用 skill(name=精确名) 或 action=load_skill，再动手；不要先 pwd/ls 或盲目点页面。运行器也可能已按匹配自动注入，见到【已加载 Skills】就按其 playbook 执行。\n\n")
	const maxCatalogChars = 8000
	included, hidden, omitted := 0, 0, 0
	for _, s := range c.Skills {
		if !s.AllowImplicit {
			hidden++
			continue
		}
		line := fmt.Sprintf("- **%s**: %s\n", s.Name, s.Description)
		if b.Len()+len(line) > maxCatalogChars {
			omitted++
			continue
		}
		b.WriteString(line)
		included++
	}
	if omitted > 0 {
		b.WriteString(fmt.Sprintf("\n… 另有 %d 个 Skill 因目录预算未列出；用户可用 /skill list 浏览并显式加载。\n", omitted))
	}
	if hidden > 0 {
		b.WriteString(fmt.Sprintf("%d 个 Skill 仅允许用户显式调用，未向模型暴露。\n", hidden))
	}
	if included == 0 && hidden > 0 {
		return ""
	}
	return b.String()
}

func parseSkillMeta(skillFile, skillDir string) (SkillMeta, error) {
	file, err := os.Open(skillFile)
	if err != nil {
		return SkillMeta{}, err
	}
	defer file.Close()
	const maxSkillMetadataFile = 4 << 20
	data, err := io.ReadAll(io.LimitReader(file, maxSkillMetadataFile+1))
	if err != nil {
		return SkillMeta{}, err
	}
	if len(data) > maxSkillMetadataFile {
		return SkillMeta{}, fmt.Errorf("SKILL.md 超过 %d MiB 限制", maxSkillMetadataFile>>20)
	}

	content := strings.TrimPrefix(string(data), "\ufeff")
	meta := SkillMeta{
		Path:          skillFile,
		Dir:           skillDir,
		AllowImplicit: true,
		UserInvocable: true,
	}

	frontmatter, err := skillFrontmatter(content)
	if err != nil {
		return SkillMeta{}, err
	}
	var raw struct {
		Name                   string `yaml:"name"`
		Description            string `yaml:"description"`
		License                string `yaml:"license"`
		DisableModelInvocation bool   `yaml:"disable-model-invocation"`
		UserInvocable          *bool  `yaml:"user-invocable"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &raw); err != nil {
		return SkillMeta{}, err
	}
	meta.Name, meta.Description, meta.License = raw.Name, raw.Description, raw.License
	if raw.DisableModelInvocation {
		meta.AllowImplicit = false
		meta.InvocationSource = "SKILL.md disable-model-invocation"
	}
	if raw.UserInvocable != nil {
		meta.UserInvocable = *raw.UserInvocable
	}
	meta.Name = strings.TrimSpace(meta.Name)
	meta.Description = strings.Join(strings.Fields(meta.Description), " ")
	if meta.Name == "" || len([]rune(meta.Name)) > 128 || strings.ContainsAny(meta.Name, "\r\n\x00") {
		return SkillMeta{}, fmt.Errorf("skill name 无效")
	}
	if meta.Description == "" {
		return SkillMeta{}, fmt.Errorf("skill description 不能为空")
	}
	if len([]rune(meta.Description)) > 1024 {
		meta.Description = string([]rune(meta.Description)[:1024])
	}

	var openAI struct {
		Policy struct {
			AllowImplicit *bool `yaml:"allow_implicit_invocation"`
		} `yaml:"policy"`
	}
	if data, err := os.ReadFile(filepath.Join(skillDir, "agents", "openai.yaml")); err == nil && yaml.Unmarshal(data, &openAI) == nil && openAI.Policy.AllowImplicit != nil {
		// An explicit opt-out in SKILL.md must not be reversed by a secondary
		// metadata file. Either source may restrict model invocation.
		if meta.AllowImplicit || !*openAI.Policy.AllowImplicit {
			meta.AllowImplicit = *openAI.Policy.AllowImplicit
			meta.InvocationSource = "agents/openai.yaml"
		}
	}

	return meta, nil
}

// skillFrontmatter recognizes delimiters only on their own lines, so a
// description containing "---" does not prematurely terminate the YAML.
func skillFrontmatter(content string) (string, error) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", fmt.Errorf("SKILL.md 缺少 YAML frontmatter")
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[1:i], "\n"), nil
		}
	}
	return "", fmt.Errorf("SKILL.md frontmatter 未闭合")
}

// keepMCPAdapterSkill prevents ClawHub's python fofa_recon.py wrapper from
// replacing DeepSentry's bundled FofaMap MCP playbook when both are named
// "fofamap". A later MCP-oriented skill (for example the official 2.0.1
// agent-kit copy) can still overwrite the bundled one.
func keepMCPAdapterSkill(existing, incoming SkillMeta) bool {
	if !strings.EqualFold(strings.TrimSpace(existing.Name), "fofamap") {
		return false
	}
	if isPythonFOFAPlaybook(incoming.Dir) && isMCPFofaMapPlaybook(existing) {
		return true
	}
	return false
}

func isPythonFOFAPlaybook(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "scripts", "fofa_recon.py"))
	return err == nil
}

func isMCPFofaMapPlaybook(meta SkillMeta) bool {
	if isPythonFOFAPlaybook(meta.Dir) {
		return false
	}
	blob := strings.ToLower(meta.Description + "\n" + meta.Path)
	if data, err := os.ReadFile(meta.Path); err == nil {
		blob += "\n" + strings.ToLower(string(data))
	}
	return strings.Contains(blob, "mcp") &&
		(strings.Contains(blob, "fofa_account") || strings.Contains(blob, "fofa_search") || strings.Contains(blob, "fofa_rules"))
}

func expandSourcePath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// DefaultSources 返回默认 Skill 来源路径
func DefaultSources() []string {
	sources := executableSkillSources()
	sources = append(sources, "skills")
	if home, err := os.UserHomeDir(); err == nil {
		sources = append(sources, filepath.Join(home, ".deepsentry", "skills"))
	}
	return sources
}

func executableSkillSources() []string {
	// Installed binaries may run from an arbitrary working directory. Release
	// archives keep the bundled playbooks beside the executable, while build/bin
	// aliases find them one directory above.
	var sources []string
	if executable, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(executable); err == nil {
			executable = resolved
		}
		dir := filepath.Dir(executable)
		sources = append(sources,
			filepath.Join(dir, "..", "bundled-skills"),
			filepath.Join(dir, "..", "skills"),
			filepath.Join(dir, "bundled-skills"),
			filepath.Join(dir, "skills"),
		)
	}
	return sources
}

func normalizeSkillSource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	source = filepath.Clean(expandSourcePath(source))
	if absolute, err := filepath.Abs(source); err == nil {
		source = absolute
	}
	if resolved, err := filepath.EvalSymlinks(source); err == nil {
		source = resolved
	}
	return source
}
