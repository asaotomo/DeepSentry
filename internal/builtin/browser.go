package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"ai-edr/internal/config"
)

func HeadlessBrowser(rt Runtime, rawURL, mode string, waitMs, maxText int, selector string, wantScreenshot bool) (string, error) {
	u, err := normalizeURL(rawURL)
	if err != nil {
		return "", err
	}
	mode = normalizeBrowserMode(mode)
	if waitMs <= 0 {
		waitMs = 1500
	}
	if waitMs > 10000 {
		waitMs = 10000
	}
	if maxText <= 0 {
		maxText = 20000
	}
	if maxText > 100000 {
		maxText = 100000
	}

	bin, why := findBrowserBinary()
	if bin == "" {
		out, fallbackErr := staticBrowserFallback(rt, u, mode, why, maxText)
		if fallbackErr != nil {
			return "", fallbackErr
		}
		return out + "\n\n" + browserInstallGuide(), nil
	}

	dom, runErr := dumpRenderedDOM(bin, u, waitMs, maxText)
	if runErr != nil {
		return staticBrowserFallback(rt, u, mode, runErr.Error(), maxText)
	}

	var screenshotPath string
	if wantScreenshot || mode == "screenshot" {
		if path, err := captureBrowserScreenshot(bin, u, waitMs); err == nil {
			screenshotPath = path
		} else if mode == "screenshot" {
			return staticBrowserFallback(rt, u, mode, "screenshot failed: "+err.Error(), maxText)
		}
	}

	if strings.TrimSpace(selector) != "" {
		if selected := selectRenderedFragment(dom, selector); selected != "" {
			dom = selected
		}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s Headless Browser\nURL: %s\nRuntime: %s\nMode: %s\n", rt.tag(), u, bin, mode))
	if screenshotPath != "" {
		b.WriteString("Screenshot: " + screenshotPath + "\n")
	}
	b.WriteString("Title: " + firstMatch(dom, `(?is)<title[^>]*>(.*?)</title>`) + "\n")

	switch mode {
	case "text":
		b.WriteString("\nText:\n" + renderedText(dom, maxText) + "\n")
	case "forms":
		b.WriteString("\nForms:\n")
		writeForms(&b, dom)
	case "links":
		b.WriteString("\nLinks:\n")
		writeLinks(&b, dom)
	case "screenshot":
		b.WriteString("\nText:\n" + renderedText(dom, min(maxText, 12000)) + "\n")
	default:
		b.WriteString("\nForms:\n")
		writeForms(&b, dom)
		b.WriteString("\nLinks:\n")
		writeLinks(&b, dom)
		b.WriteString("\nText:\n" + renderedText(dom, maxText) + "\n")
	}
	return b.String(), nil
}

func normalizeBrowserMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "snapshot":
		return "snapshot"
	case "text":
		return "text"
	case "forms":
		return "forms"
	case "links":
		return "links"
	case "screenshot":
		return "screenshot"
	default:
		return "snapshot"
	}
}

func staticBrowserFallback(rt Runtime, u, mode, reason string, maxText int) (string, error) {
	out, err := WebSnapshot(rt, u, maxText)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s Headless Browser\nURL: %s\nRuntime: static fallback\nReason: %s\nMode: %s\n\n%s", rt.tag(), u, browserDefault(reason, "browser unavailable"), mode, out), nil
}

func findBrowserBinary() (string, string) {
	candidates := []string{}
	if s := strings.TrimSpace(config.GlobalConfig.BrowserBinary); s != "" {
		candidates = append(candidates, s)
	}
	if s := strings.TrimSpace(os.Getenv("DEEPSENTRY_BROWSER_BINARY")); s != "" {
		candidates = append(candidates, s)
	}
	candidates = append(candidates, "chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome")
	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		)
	}
	for _, cand := range candidates {
		if cand == "" {
			continue
		}
		if strings.Contains(cand, string(os.PathSeparator)) {
			// #nosec G703 -- 候选路径只来自本机管理员设置的 DEEPSENTRY_BROWSER_BINARY 或程序内置安装路径，此处仅做存在性检查。
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				return cand, ""
			}
			continue
		}
		if path, err := exec.LookPath(cand); err == nil {
			return path, ""
		}
	}
	return "", "Chrome/Chromium binary not found"
}

func dumpRenderedDOM(bin, u string, waitMs, maxText int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), browserTimeout())
	defer cancel()
	profile, err := os.MkdirTemp("", "deepsentry-browser-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(profile)
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-dev-shm-usage",
		"--hide-scrollbars",
		"--user-data-dir=" + profile,
		fmt.Sprintf("--virtual-time-budget=%d", waitMs),
		"--dump-dom",
	}
	args = appendBrowserProxyArg(args)
	args = append(args, u)
	cmd := exec.CommandContext(ctx, bin, args...)
	stdout := &limitedStringBuffer{max: browserDOMLimit(maxText)}
	stderr := &limitedStringBuffer{max: 16 * 1024}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("browser timeout after %s", browserTimeout())
	}
	if err != nil {
		detail := stderr.String()
		if strings.TrimSpace(detail) == "" {
			detail = stdout.String()
		}
		return "", fmt.Errorf("%v: %s", err, truncateOneLine(strings.ToValidUTF8(detail, ""), 400))
	}
	dom := strings.ToValidUTF8(stdout.String(), "")
	if stdout.truncated {
		dom += "\n<!-- DeepSentry: rendered DOM truncated -->"
	}
	return dom, nil
}

type limitedStringBuffer struct {
	b         strings.Builder
	max       int
	truncated bool
}

func (l *limitedStringBuffer) Write(p []byte) (int, error) {
	if l.max <= 0 {
		return len(p), nil
	}
	remaining := l.max - l.b.Len()
	if remaining <= 0 {
		l.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		l.b.Write(p[:remaining])
		l.truncated = true
		return len(p), nil
	}
	l.b.Write(p)
	return len(p), nil
}

func (l *limitedStringBuffer) String() string {
	return l.b.String()
}

func browserDOMLimit(maxText int) int {
	limit := maxText * 8
	if limit < 512*1024 {
		limit = 512 * 1024
	}
	if limit > 2*1024*1024 {
		limit = 2 * 1024 * 1024
	}
	return limit
}

func captureBrowserScreenshot(bin, u string, waitMs int) (string, error) {
	dir := strings.TrimSpace(config.GlobalConfig.BrowserArtifactDir)
	if dir == "" {
		dir = "reports/browser"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(u + time.Now().Format(time.RFC3339Nano)))
	path := filepath.Join(dir, "browser_"+hex.EncodeToString(sum[:])[:12]+".png")
	ctx, cancel := context.WithTimeout(context.Background(), browserTimeout())
	defer cancel()
	profile, err := os.MkdirTemp("", "deepsentry-browser-shot-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(profile)
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-dev-shm-usage",
		"--hide-scrollbars",
		"--window-size=1365,768",
		"--user-data-dir=" + profile,
		fmt.Sprintf("--virtual-time-budget=%d", waitMs),
		"--screenshot=" + path,
	}
	args = appendBrowserProxyArg(args)
	args = append(args, u)
	cmd := exec.CommandContext(ctx, bin, args...)
	stdout := &limitedStringBuffer{max: 16 * 1024}
	stderr := &limitedStringBuffer{max: 16 * 1024}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("browser timeout after %s", browserTimeout())
	}
	if err != nil {
		detail := stderr.String()
		if strings.TrimSpace(detail) == "" {
			detail = stdout.String()
		}
		return "", fmt.Errorf("%v: %s", err, truncateOneLine(strings.ToValidUTF8(detail, ""), 400))
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("收紧浏览器截图权限失败: %w", err)
	}
	return path, nil
}

func appendBrowserProxyArg(args []string) []string {
	if proxy := strings.TrimSpace(config.GlobalConfig.ControllerProxy); proxy != "" {
		return append(args, "--proxy-server="+proxy)
	}
	return args
}

func browserTimeout() time.Duration {
	sec := config.GlobalConfig.BrowserTimeoutSec
	if sec <= 0 {
		sec = 20
	}
	if sec > 120 {
		sec = 120
	}
	return time.Duration(sec) * time.Second
}

func renderedText(html string, maxText int) string {
	text := stripTags(html)
	text = strings.Join(strings.Fields(text), " ")
	return truncate(text, maxText)
}

func writeForms(b *strings.Builder, html string) {
	forms := allMatches(html, `(?is)<form\b[^>]*>.*?</form>`, 20)
	if len(forms) == 0 {
		forms = allMatches(html, `(?is)<form\b[^>]*>`, 20)
	}
	if len(forms) == 0 {
		b.WriteString("  (none)\n")
		return
	}
	for _, f := range forms {
		b.WriteString("  - " + compactHTML(f) + "\n")
	}
}

func writeLinks(b *strings.Builder, html string) {
	links := allMatches(html, `(?is)<a[^>]+href=["']([^"']+)`, 40)
	if len(links) == 0 {
		b.WriteString("  (none)\n")
		return
	}
	for _, l := range links {
		b.WriteString("  - " + l + "\n")
	}
}

func selectRenderedFragment(html, selector string) string {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return ""
	}
	var pattern string
	switch {
	case strings.HasPrefix(selector, "#"):
		id := regexp.QuoteMeta(strings.TrimPrefix(selector, "#"))
		pattern = `(?is)<([a-z0-9:-]+)\b[^>]*\bid=["']` + id + `["'][^>]*>`
	case strings.HasPrefix(selector, "."):
		class := regexp.QuoteMeta(strings.TrimPrefix(selector, "."))
		pattern = `(?is)<([a-z0-9:-]+)\b[^>]*\bclass=["'][^"']*\b` + class + `\b[^"']*["'][^>]*>`
	default:
		tag := regexp.QuoteMeta(selector)
		pattern = `(?is)<` + tag + `\b[^>]*>.*?</` + tag + `>`
		re := regexp.MustCompile(pattern)
		return re.FindString(html)
	}
	re := regexp.MustCompile(pattern)
	loc := re.FindStringSubmatchIndex(html)
	if len(loc) < 4 {
		return ""
	}
	start := loc[0]
	openEnd := loc[1]
	tag := strings.ToLower(html[loc[2]:loc[3]])
	closeNeedle := "</" + tag + ">"
	rest := strings.ToLower(html[openEnd:])
	closeRel := strings.Index(rest, closeNeedle)
	if closeRel < 0 {
		return html[start:openEnd]
	}
	return html[start : openEnd+closeRel+len(closeNeedle)]
}

func browserDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
