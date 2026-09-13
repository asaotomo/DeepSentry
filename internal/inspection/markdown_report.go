package inspection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"ai-edr/internal/config"
)

const minAutoReportBytes = 2048

var (
	mdImageRe      = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)
	savedImageRe   = regexp.MustCompile(`已保存到\s+(\S+?\.(?:png|jpe?g|PNG|JPE?G))`)
	backtickImgRe  = regexp.MustCompile("`([^`\n]+?\\.(?:png|jpe?g|PNG|JPE?G))`")
	artifactPathRe = regexp.MustCompile(`(?:build/)?reports/mcp-artifacts/[^\s)\]'"，。；]+?\.(?:png|jpe?g|PNG|JPE?G)`)
	reportTimeRe   = regexp.MustCompile(`(20\d{2}-\d{2}-\d{2}[ T]\d{2}:\d{2}(?::\d{2})?)`)
)

// CompileSource turns a Markdown audit/report or structured JSON manifest into Word.
func CompileSource(cfg config.Config, source string) (Report, error) {
	resolved, err := ResolveReportSource(source)
	if err != nil {
		return Report{}, err
	}
	switch strings.ToLower(filepath.Ext(resolved)) {
	case ".json":
		return Compile(cfg, resolved)
	case ".md", ".markdown", ".txt":
		return CompileMarkdown(cfg, resolved)
	default:
		raw, readErr := peekReportFile(resolved)
		if readErr != nil {
			return Report{}, readErr
		}
		trim := bytes.TrimSpace(bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}))
		if len(trim) > 0 && (trim[0] == '{' || trim[0] == '[') {
			return Compile(cfg, resolved)
		}
		return CompileMarkdown(cfg, resolved)
	}
}

// ResolveReportSource accepts an explicit path, otherwise picks the current
// session file or the newest inspection-like reports/report_*.md.
func ResolveReportSource(explicit string) (string, error) {
	if source := strings.TrimSpace(explicit); source != "" {
		return source, nil
	}
	if env := strings.TrimSpace(os.Getenv("DEEPSENTRY_REPORT_PATH")); env != "" {
		if scoreSessionReportFile(env) >= 80 {
			return env, nil
		}
	}
	return latestSessionReport()
}

func latestSessionReport() (string, error) {
	var best string
	var bestScore int
	var bestTime time.Time
	found := false
	for _, root := range reportSearchRoots() {
		matches, _ := filepath.Glob(filepath.Join(root, "report_*.md"))
		for _, path := range matches {
			st, err := os.Stat(path)
			if err != nil || !st.Mode().IsRegular() || st.Size() < minAutoReportBytes {
				continue
			}
			score := scoreSessionReportFile(path)
			if !found || score > bestScore || (score == bestScore && st.ModTime().After(bestTime)) {
				found = true
				best = path
				bestScore = score
				bestTime = st.ModTime()
			}
		}
	}
	if !found {
		return "", errors.New("没有找到可编译的巡检报告；请指定 source 指向 .md 或 .json，或先完成巡检")
	}
	return best, nil
}

func reportSearchRoots() []string {
	seen := map[string]bool{}
	var roots []string
	add := func(p string) {
		if strings.TrimSpace(p) == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return
		}
		if seen[abs] {
			return
		}
		st, err := os.Stat(abs)
		if err != nil || !st.IsDir() {
			return
		}
		seen[abs] = true
		roots = append(roots, abs)
	}
	add("reports")
	add("build/reports")
	if cwd, err := os.Getwd(); err == nil {
		add(filepath.Join(cwd, "reports"))
		add(filepath.Join(cwd, "build", "reports"))
		add(filepath.Join(filepath.Dir(cwd), "reports"))
		add(filepath.Join(filepath.Dir(cwd), "build", "reports"))
	}
	return roots
}

func scoreSessionReportFile(path string) int {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return -1
	}
	raw, err := peekReportFile(path)
	if err != nil {
		return -1
	}
	text := string(raw)
	score := 0
	if strings.Contains(text, "mcp-artifacts") && (strings.Contains(text, ".png") || strings.Contains(text, ".jpg")) {
		score += 100
	}
	if strings.Contains(text, "巡检") {
		score += 40
	}
	if strings.Contains(text, "风险") && strings.Contains(text, "建议") {
		score += 40
	}
	if strings.Contains(text, "Final Report") {
		score += 30
	}
	age := time.Since(st.ModTime())
	if age < 6*time.Hour {
		score += 10
	}
	return score
}

func peekReportFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 256<<10))
}

func CompileMarkdown(cfg config.Config, markdownPath string) (Report, error) {
	var report Report
	raw, err := readReportMarkdown(markdownPath)
	if err != nil {
		return report, err
	}
	extracted := extractDeliverableMarkdown(string(raw))
	if strings.TrimSpace(stripMarkdownHeadingNoise(extracted)) == "" {
		return report, errors.New("Markdown 报告没有可写入 Word 的正文或截图")
	}
	if report.Started.IsZero() {
		if st, e := os.Stat(markdownPath); e == nil {
			report.Started = st.ModTime()
		} else {
			report.Started = time.Now()
		}
	}
	report.Finished = time.Now()
	root := cfg.Inspection.OutputDir
	if root == "" {
		root = "reports/inspections"
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return report, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return report, err
	}
	dir, err := os.MkdirTemp(root, report.Started.Format("20060102-150405")+"-md-")
	if err != nil {
		return report, err
	}
	report.Markdown = filepath.Join(dir, "report.md")
	report.Word = filepath.Join(dir, "report.docx")
	report.Manifest = filepath.Join(dir, "manifest.json")

	blocks := parseMarkdownBlocks(extracted)
	images, err := materializeMarkdownImages(dir, markdownPath, extracted, &blocks)
	if err != nil {
		return report, err
	}
	if t, ok := parseHeaderTime(extracted); ok {
		report.Started = t
	}
	if err = writeMarkdownDocx(report.Word, blocks, report.Started, report.Finished); err != nil {
		return report, err
	}
	if err = os.WriteFile(report.Markdown, []byte(extracted), 0600); err != nil {
		return report, err
	}
	meta := map[string]any{
		"source":      markdownPath,
		"started":     report.Started,
		"finished":    report.Finished,
		"markdown":    report.Markdown,
		"word":        report.Word,
		"images":      images,
		"extracted":   true,
		"session_log": isSessionAudit(string(raw)),
	}
	rawMeta, _ := json.MarshalIndent(meta, "", "  ")
	err = os.WriteFile(report.Manifest, rawMeta, 0600)
	return report, err
}

func readReportMarkdown(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	f.Close()
	if err != nil || len(raw) > 4<<20 {
		return nil, errors.New("报告 Markdown 超过 4 MiB 或读取失败")
	}
	return bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}), nil
}

func isSessionAudit(md string) bool {
	return strings.Contains(md, "### [") && (strings.Contains(md, "AI Thought") || strings.Contains(md, "AI Action") || strings.Contains(md, "**执行结果**"))
}

func extractDeliverableMarkdown(md string) string {
	if !isSessionAudit(md) {
		return organizeInspectionMarkdown(strings.TrimSpace(md)+"\n", collectEvidenceImages(md))
	}
	header, sections := splitAuditSections(md)
	body := lastReportLikeSection(sections)
	images := collectEvidenceImages(md)
	var b strings.Builder
	header = strings.TrimSpace(header)
	if header != "" {
		b.WriteString(header)
		b.WriteString("\n\n")
	}
	if body != "" {
		b.WriteString(strings.TrimSpace(body))
		b.WriteString("\n\n")
	}
	out := organizeInspectionMarkdown(b.String(), images)
	if strings.TrimSpace(stripMarkdownHeadingNoise(out)) == "" {
		return strings.TrimSpace(md) + "\n"
	}
	return out
}

type reportChapter struct {
	title string
	body  string
	kind  string
}

func organizeInspectionMarkdown(md string, images []string) string {
	title, meta, chapters := splitReportChapters(md)
	title, subtitle := cleanCoverTitle(title)
	if isGenericReportTitle(title) || title == "" {
		for _, ch := range chapters {
			if t, sub := cleanCoverTitle(ch.title); t != "" && !isGenericReportTitle(t) && !strings.HasPrefix(stripChapterIndex(ch.title), "总体") {
				title, subtitle = t, sub
				break
			}
		}
	}
	var leftover []string
	var dumpHint strings.Builder
	filtered := chapters[:0]
	for _, ch := range chapters {
		if isScreenshotDumpTitle(ch.title) {
			leftover = append(leftover, collectEvidenceImages(ch.body)...)
			dumpHint.WriteString(ch.body)
			dumpHint.WriteByte('\n')
			continue
		}
		if strings.TrimSpace(ch.body) == "" && (strings.HasPrefix(ch.title, "巡检报告") || strings.Contains(ch.title, "http")) {
			continue
		}
		filtered = append(filtered, ch)
	}
	chapters = filtered
	placed := map[string]bool{}
	for _, ch := range chapters {
		for _, img := range collectEvidenceImages(ch.body) {
			if strings.Contains(ch.body, "![]("+img+")") || strings.Contains(ch.body, "![]("+filepath.Base(img)+")") {
				placed[img] = true
				placed[imageKey(img)] = true
			}
		}
	}
	for i := range chapters {
		chapters[i].body, placed = attachImagesToText(chapters[i].title, chapters[i].body, images, placed)
	}
	var conclusion, detail, summary []reportChapter
	for _, ch := range chapters {
		switch chapterKind(ch.title, ch.body) {
		case "conclusion":
			conclusion = append(conclusion, ch)
		case "summary":
			summary = append(summary, ch)
		default:
			detail = append(detail, ch)
		}
	}
	placeRemainingImages(detail, append(append([]string{}, images...), leftover...), placed, dumpHint.String())
	var b strings.Builder
	if title != "" {
		b.WriteString("# " + title + "\n\n")
	} else {
		b.WriteString("# DeepSentry 巡检报告\n\n")
	}
	if subtitle != "" {
		b.WriteString(subtitle + "\n\n")
	}
	if meta != "" {
		b.WriteString(meta + "\n\n")
	}
	writeChapterGroup(&b, "一、总体结论", conclusion)
	writeChapterGroup(&b, "二、巡检详情", detail)
	if len(summary) == 0 && len(conclusion) > 0 {
		summary = append(summary, reportChapter{title: "后续建议", body: "以上结论均来自本轮只读巡检的实际页面与采集证据。优先处置总体结论中的高风险项，再复核待观察项。"})
	}
	writeChapterGroup(&b, "三、总结与建议", summary)
	return strings.TrimSpace(b.String()) + "\n"
}

func writeChapterGroup(b *strings.Builder, heading string, chapters []reportChapter) {
	if len(chapters) == 0 {
		return
	}
	b.WriteString("## " + heading + "\n\n")
	for _, ch := range chapters {
		title := stripChapterIndex(ch.title)
		if title != "" && !isGenericReportTitle(title) && title != heading {
			b.WriteString("### " + title + "\n\n")
		}
		if strings.TrimSpace(ch.body) != "" {
			b.WriteString(strings.TrimSpace(ch.body) + "\n\n")
		}
	}
}

func splitReportChapters(md string) (title, meta string, chapters []reportChapter) {
	lines := strings.Split(md, "\n")
	var metaLines []string
	current := -1
	for _, line := range lines {
		if heading, ok := markdownHeading(line); ok {
			text := strings.TrimSpace(heading.text)
			if heading.kind == "h1" && title == "" {
				title = text
				continue
			}
			chapters = append(chapters, reportChapter{title: text})
			current = len(chapters) - 1
			continue
		}
		if current < 0 {
			if strings.TrimSpace(line) == "" || strings.TrimSpace(line) == "---" {
				continue
			}
			metaLines = append(metaLines, line)
			continue
		}
		chapters[current].body += line + "\n"
	}
	return title, strings.TrimSpace(strings.Join(metaLines, "\n")), chapters
}

func chapterKind(title, body string) string {
	if isScreenshotDumpTitle(title) {
		return "detail"
	}
	if strings.Contains(title, "风险") || strings.Contains(title, "总体结论") {
		return "conclusion"
	}
	if strings.Contains(title, "结论") && !strings.Contains(title, "详情") {
		return "conclusion"
	}
	if strings.Contains(title, "说明") || strings.Contains(title, "下一步") || strings.Contains(title, "总结") {
		return "summary"
	}
	if strings.Contains(title, "建议") && !strings.Contains(title, "风险") {
		return "summary"
	}
	return "detail"
}

func isScreenshotDumpTitle(title string) bool {
	return strings.Contains(title, "截图") && (strings.Contains(title, "证据") || strings.Contains(title, "附件") || strings.Contains(title, "汇总"))
}

func stripChapterIndex(title string) string {
	title = strings.TrimSpace(title)
	title = strings.TrimLeft(title, "一二三四五六七八九十、. ")
	return strings.TrimSpace(title)
}

func attachImagesToText(title, text string, images []string, placed map[string]bool) (string, map[string]bool) {
	lines := strings.Split(text, "\n")
	var out []string
	for _, line := range lines {
		out = append(out, line)
		if strings.HasPrefix(strings.TrimSpace(line), "![") {
			continue
		}
		for _, img := range images {
			key := imageKey(img)
			if placed[img] || placed[key] {
				continue
			}
			base := filepath.Base(img)
			if base == "" || !(strings.Contains(line, img) || strings.Contains(line, base) || strings.Contains(line, strings.TrimSuffix(base, filepath.Ext(base)))) {
				continue
			}
			if strings.Contains(line, "总览") && strings.Contains(title, "登录") {
				continue
			}
			out = append(out, "", "![]("+img+")")
			placed[img] = true
			placed[key] = true
		}
	}
	return strings.Join(out, "\n"), placed
}

func placeRemainingImages(detail []reportChapter, images []string, placed map[string]bool, dumpHint string) {
	if len(detail) == 0 {
		return
	}
	var unused []string
	seen := map[string]bool{}
	for _, img := range images {
		key := imageKey(img)
		if placed[img] || placed[key] || seen[key] || key == "" {
			continue
		}
		seen[key] = true
		unused = append(unused, img)
	}
	loginIdx, overviewIdx := -1, -1
	for i, ch := range detail {
		if loginIdx < 0 && strings.Contains(ch.title, "登录") {
			loginIdx = i
		}
		if overviewIdx < 0 && (strings.Contains(ch.title, "巡检结果") || strings.Contains(ch.title, "总览")) {
			overviewIdx = i
		}
	}
	loginUsed := 0
	for _, img := range unused {
		target := -1
		switch {
		case mentionsOverviewShot(detail, img, dumpHint) && overviewIdx >= 0:
			target = overviewIdx
		default:
			target = bestChapterForImage(detail, img)
		}
		if target < 0 {
			switch {
			case loginIdx >= 0 && loginUsed < 2:
				target = loginIdx
			case overviewIdx >= 0:
				target = overviewIdx
			case loginIdx >= 0:
				target = loginIdx
			default:
				target = len(detail) - 1
			}
		}
		if target == loginIdx {
			loginUsed++
		}
		detail[target].body = strings.TrimSpace(detail[target].body) + "\n\n![](" + img + ")\n"
		placed[img] = true
		placed[imageKey(img)] = true
	}
}

func imageKey(img string) string {
	return strings.ToLower(filepath.Base(strings.TrimSpace(img)))
}

func mentionsOverviewShot(chapters []reportChapter, img string, dumpHint string) bool {
	base := filepath.Base(img)
	if base == "" {
		return false
	}
	blobs := []string{dumpHint}
	for _, ch := range chapters {
		blobs = append(blobs, ch.body)
	}
	for _, blob := range blobs {
		for _, line := range strings.Split(blob, "\n") {
			if (strings.Contains(line, img) || strings.Contains(line, base)) && strings.Contains(line, "总览") {
				return true
			}
		}
	}
	return false
}

func bestChapterForImage(chapters []reportChapter, img string) int {
	base := strings.ToLower(filepath.Base(img))
	best, score := -1, 0
	for i, ch := range chapters {
		s := 0
		blob := ch.title + "\n" + ch.body
		if filepath.Base(img) != "" && strings.Contains(blob, filepath.Base(img)) {
			s += 20
		}
		if strings.Contains(ch.title, "登录") || strings.Contains(ch.title, "验证") {
			s += 4
		}
		if strings.Contains(ch.title, "总览") || strings.Contains(ch.title, "巡检结果") {
			s += 4
		}
		if strings.Contains(base, "login") && strings.Contains(ch.title, "登录") {
			s += 6
		}
		if s > score {
			score = s
			best = i
		}
	}
	if score < 10 {
		return -1
	}
	return best
}

func cleanCoverTitle(s string) (string, string) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "巡检报告：")
	s = strings.TrimPrefix(s, "巡检报告:")
	s = strings.TrimPrefix(s, "巡检结论")
	s = strings.Trim(s, "：: ")
	subtitle := ""
	for _, sep := range []string{"（http", "(http"} {
		if i := strings.Index(s, sep); i >= 0 {
			subtitle = strings.Trim(s[i:], "（）() ")
			s = strings.TrimSpace(s[:i])
			break
		}
	}
	return s, subtitle
}

func parseHeaderTime(md string) (time.Time, bool) {
	m := reportTimeRe.FindStringSubmatch(md)
	if len(m) < 2 {
		return time.Time{}, false
	}
	raw := m[1]
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

type auditSection struct {
	title string
	body  string
}

func splitAuditSections(md string) (string, []auditSection) {
	lines := strings.Split(md, "\n")
	var header []string
	var sections []auditSection
	current := -1
	for _, line := range lines {
		if strings.HasPrefix(line, "### [") {
			title := strings.TrimSpace(strings.TrimPrefix(line, "### "))
			sections = append(sections, auditSection{title: title})
			current = len(sections) - 1
			continue
		}
		if current < 0 {
			header = append(header, line)
			continue
		}
		sections[current].body += line + "\n"
	}
	return strings.TrimSpace(strings.Join(header, "\n")), sections
}

func lastReportLikeSection(sections []auditSection) string {
	var best string
	var bestScore int
	var fallback string
	var final string
	var finalScore int
	foundFinal := false
	for _, s := range sections {
		body := strings.TrimSpace(s.body)
		if body == "" {
			continue
		}
		cleaned := stripThoughtMeta(body)
		score := reportBodyScore(cleaned)
		if strings.Contains(s.title, "Final Report") {
			if !foundFinal || score > finalScore {
				foundFinal = true
				finalScore = score
				final = cleaned
			}
			continue
		}
		if score > bestScore {
			bestScore = score
			best = cleaned
		}
		if fallback == "" && (strings.Contains(s.title, "AI Thought") || strings.Contains(s.title, "Final")) && utf8.RuneCountInString(cleaned) >= 120 {
			fallback = cleaned
		}
	}
	if foundFinal && (finalScore > 0 || best == "") {
		return final
	}
	if best != "" {
		return best
	}
	return fallback
}

func reportLikeBody(body string) bool {
	return reportBodyScore(body) >= 20
}

func reportBodyScore(body string) int {
	if utf8.RuneCountInString(body) < 180 {
		return 0
	}
	score := 0
	for _, key := range []string{"风险", "建议", "巡检", "结论", "证据", "## ", "告警", "登录", "检查项"} {
		if strings.Contains(body, key) {
			score += 10
		}
	}
	if strings.Contains(body, "|") && (strings.Contains(body, "级别") || strings.Contains(body, "建议")) {
		score += 40
	}
	if strings.Contains(body, "Hx0") || strings.Contains(body, "平台画像") || strings.Contains(body, "已具备的安全能力") {
		score += 20
	}
	for _, noise := range []string{"pandoc", "manifest_path", "inspection-evidence", "python-docx", "守卫还锁着", "写不了文件", "checkpoint", "brew install"} {
		if strings.Contains(body, noise) {
			score -= 60
		}
	}
	return score
}

func stripThoughtMeta(body string) string {
	lines := strings.Split(body, "\n")
	var kept []string
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "Idea:") || strings.HasPrefix(trim, "Action:") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func collectEvidenceImages(md string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		path := sanitizeImagePath(raw)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, m := range mdImageRe.FindAllStringSubmatch(md, -1) {
		add(m[1])
	}
	for _, m := range savedImageRe.FindAllStringSubmatch(md, -1) {
		add(m[1])
	}
	for _, m := range backtickImgRe.FindAllStringSubmatch(md, -1) {
		add(m[1])
	}
	for _, m := range artifactPathRe.FindAllString(md, -1) {
		add(m)
	}
	return out
}

func sanitizeImagePath(raw string) string {
	path := strings.TrimSpace(raw)
	path = strings.Trim(path, "`\"'<>")
	path = strings.TrimRight(path, "。；，,)")
	if i := strings.IndexAny(path, " \t"); i >= 0 {
		path = path[:i]
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
		return ""
	}
	return path
}

type mdBlock struct {
	kind      string
	text      string
	imagePath string
	image     []byte
	format    string
	width     int
	height    int
	rows      [][]string
	items     []string
}

func parseMarkdownBlocks(md string) []mdBlock {
	lines := strings.Split(md, "\n")
	var blocks []mdBlock
	var para []string
	flush := func() {
		text := strings.TrimSpace(strings.Join(para, "\n"))
		para = nil
		if text == "" {
			return
		}
		blocks = append(blocks, splitParagraphImages(text)...)
	}
	inFence := false
	var fence []string
	flushFence := func() {
		text := strings.TrimRight(strings.Join(fence, "\n"), "\n")
		fence = nil
		if strings.TrimSpace(text) == "" {
			return
		}
		if utf8.RuneCountInString(text) > 4000 {
			text = string([]rune(text)[:4000]) + "\n[报告摘要截断]"
		}
		blocks = append(blocks, mdBlock{kind: "quote", text: text})
	}
	var table []string
	flushTable := func() {
		if len(table) == 0 {
			return
		}
		if rows := parseMarkdownTable(table); len(rows) > 0 {
			blocks = append(blocks, mdBlock{kind: "table", rows: rows})
		}
		table = nil
	}
	var list []string
	flushList := func() {
		if len(list) == 0 {
			return
		}
		blocks = append(blocks, mdBlock{kind: "list", items: append([]string(nil), list...)})
		list = nil
	}
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~~") {
			flush()
			flushTable()
			flushList()
			if inFence {
				flushFence()
				inFence = false
			} else {
				inFence = true
			}
			continue
		}
		if inFence {
			fence = append(fence, line)
			continue
		}
		if strings.HasPrefix(trim, "|") {
			flush()
			flushList()
			table = append(table, trim)
			continue
		}
		if item, ok := markdownListItem(trim); ok {
			flush()
			flushTable()
			list = append(list, item)
			continue
		}
		if trim == "" || trim == "---" {
			flush()
			flushTable()
			flushList()
			continue
		}
		if heading, ok := markdownHeading(line); ok {
			flush()
			flushTable()
			flushList()
			blocks = append(blocks, heading)
			continue
		}
		flushTable()
		flushList()
		para = append(para, line)
		if len(blocks)+len(para) > 2000 {
			break
		}
	}
	if inFence {
		flushFence()
	}
	flush()
	flushTable()
	flushList()
	return blocks
}

func markdownListItem(trim string) (string, bool) {
	for _, prefix := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(trim, prefix) {
			return strings.TrimSpace(trim[len(prefix):]), true
		}
	}
	if len(trim) >= 3 && trim[0] >= '1' && trim[0] <= '9' {
		i := 1
		for i < len(trim) && trim[i] >= '0' && trim[i] <= '9' {
			i++
		}
		if i+1 < len(trim) && trim[i] == '.' && trim[i+1] == ' ' {
			return strings.TrimSpace(trim[i+2:]), true
		}
	}
	return "", false
}

func parseMarkdownTable(lines []string) [][]string {
	var rows [][]string
	for _, line := range lines {
		cols := splitMarkdownRow(line)
		if len(cols) == 0 || isTableSeparator(cols) {
			continue
		}
		rows = append(rows, cols)
	}
	return rows
}

func splitMarkdownRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func isTableSeparator(cols []string) bool {
	if len(cols) == 0 {
		return false
	}
	for _, c := range cols {
		c = strings.TrimSpace(c)
		c = strings.Trim(c, ":")
		if c == "" || strings.Trim(c, "-") != "" {
			return false
		}
	}
	return true
}

func markdownHeading(line string) (mdBlock, bool) {
	for i := 3; i >= 1; i-- {
		prefix := strings.Repeat("#", i) + " "
		if strings.HasPrefix(line, prefix) {
			return mdBlock{kind: fmt.Sprintf("h%d", i), text: strings.TrimSpace(strings.TrimPrefix(line, prefix))}, true
		}
	}
	return mdBlock{}, false
}

func splitParagraphImages(text string) []mdBlock {
	if mdImageRe.MatchString(text) && strings.TrimSpace(mdImageRe.ReplaceAllString(text, "")) == "" {
		m := mdImageRe.FindStringSubmatch(text)
		return []mdBlock{{kind: "img", imagePath: sanitizeImagePath(m[1]), text: text}}
	}
	return []mdBlock{{kind: "p", text: text}}
}

func materializeMarkdownImages(dir, markdownPath, extracted string, blocks *[]mdBlock) (int, error) {
	seen := map[string]bool{}
	count := 0
	total := int64(0)
	addImage := func(src string) (mdBlock, bool, error) {
		resolved := resolveLocalImage(src, markdownPath)
		if resolved == "" || seen[resolved] {
			return mdBlock{}, false, nil
		}
		data, size, format, err := readLocalImage(resolved)
		if err != nil {
			return mdBlock{kind: "p", text: "截图无法读取：" + src}, true, nil
		}
		total += int64(len(data))
		if total > 100<<20 {
			return mdBlock{}, false, errors.New("单份报告证据总量超过 100 MiB，请分批生成")
		}
		ext := ".png"
		if format == "jpeg" {
			ext = ".jpg"
		}
		saved, err := saveEvidence(dir, fmt.Sprintf("%03d%s", count+1, ext), data)
		if err != nil {
			return mdBlock{}, false, err
		}
		seen[resolved] = true
		count++
		return mdBlock{
			kind:      "img",
			text:      saved.Path,
			imagePath: saved.Path,
			image:     data,
			format:    format,
			width:     size.Width,
			height:    size.Height,
		}, true, nil
	}
	var next []mdBlock
	for _, block := range *blocks {
		if block.kind != "img" {
			next = append(next, block)
			continue
		}
		img, ok, err := addImage(block.imagePath)
		if err != nil {
			return count, err
		}
		if ok {
			next = append(next, img)
		}
	}
	if count == 0 {
		for _, src := range collectEvidenceImages(extracted) {
			img, ok, err := addImage(src)
			if err != nil {
				return count, err
			}
			if ok {
				next = append(next, img)
			}
		}
	}
	*blocks = next
	return count, nil
}

func resolveLocalImage(src, markdownPath string) string {
	src = sanitizeImagePath(src)
	if src == "" {
		return ""
	}
	var candidates []string
	if filepath.IsAbs(src) {
		candidates = append(candidates, src)
	} else {
		candidates = append(candidates, filepath.Join(filepath.Dir(markdownPath), src), src)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() {
			abs, err := filepath.Abs(c)
			if err == nil {
				return abs
			}
			return c
		}
	}
	return ""
}

func readLocalImage(path string) ([]byte, image.Config, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, image.Config{}, "", err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxEvidenceBytes+1))
	f.Close()
	if err != nil || len(data) > maxEvidenceBytes {
		return nil, image.Config{}, "", errors.New("证据必须为不超过 20 MiB 的普通文件")
	}
	size, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") || size.Width <= 0 || size.Height <= 0 {
		return nil, image.Config{}, "", errors.New("截图证据无效")
	}
	return data, size, format, nil
}

func stripMarkdownHeadingNoise(md string) string {
	s := strings.TrimSpace(md)
	s = strings.TrimPrefix(s, "# DeepSentry 巡检报告")
	return strings.TrimSpace(s)
}

func writeMarkdownDocx(path string, blocks []mdBlock, started, finished time.Time) error {
	doc := newDocx()
	title := "DeepSentry 巡检报告"
	subtitle := ""
	for _, block := range blocks {
		if block.kind != "h1" && block.kind != "h2" {
			continue
		}
		if isGenericReportTitle(block.text) || strings.HasPrefix(block.text, "一、") || strings.HasPrefix(block.text, "二、") || strings.HasPrefix(block.text, "三、") {
			continue
		}
		clean, extra := cleanCoverTitle(block.text)
		if clean != "" {
			title = clean
		}
		if extra != "" {
			subtitle = extra
		}
		break
	}
	doc.cover(title, subtitle, started, finished)
	caption := "巡检截图"
	for _, block := range blocks {
		switch block.kind {
		case "h1":
			if isGenericReportTitle(block.text) || strings.TrimSpace(block.text) == title {
				continue
			}
			caption = stripChapterIndex(block.text)
			doc.heading(1, block.text)
		case "h2":
			if strings.TrimSpace(block.text) == title {
				continue
			}
			caption = stripChapterIndex(block.text)
			doc.heading(1, block.text)
		case "h3":
			caption = stripChapterIndex(block.text)
			doc.heading(2, block.text)
		case "table":
			doc.table(block.rows, true)
		case "list":
			if lookLikeMetaList(block.items) {
				continue
			}
			doc.bullets(block.items)
		case "quote":
			doc.quote(block.text)
		case "img":
			if len(block.image) == 0 || block.width <= 0 {
				continue
			}
			doc.image(block.image, block.format, image.Config{Width: block.width, Height: block.height}, caption)
		default:
			if lookLikeMetaLine(block.text) {
				continue
			}
			doc.richPara(block.text)
		}
	}
	return doc.save(path)
}

func lookLikeMetaList(items []string) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if !lookLikeMetaLine(item) {
			return false
		}
	}
	return true
}

func isGenericReportTitle(s string) bool {
	s = strings.TrimSpace(s)
	return s == "可以安全排查报告" || s == "DeepSentry 安全排查报告" || s == "DeepSentry 巡检报告"
}

func lookLikeMetaLine(s string) bool {
	s = strings.TrimSpace(strings.TrimPrefix(s, "- "))
	return strings.HasPrefix(s, "**启动时间**") || strings.HasPrefix(s, "**操作员**") || strings.HasPrefix(s, "**工具版本**") || strings.HasPrefix(s, "启动时间")
}

func parseReportMetaTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = strings.TrimSpace(s[i+2:])
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local)
	return t, err == nil
}
