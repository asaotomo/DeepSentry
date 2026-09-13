package inspection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-edr/internal/config"
	"ai-edr/internal/executor"
)

type Evidence struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type Result struct {
	Device            string     `json:"device"`
	Check             string     `json:"check"`
	Status            string     `json:"status"`
	Detail            string     `json:"detail"`
	CollectedAt       time.Time  `json:"collected_at"`
	Changed           bool       `json:"changed"`
	BaselineAvailable bool       `json:"baseline_available"`
	Evidence          []Evidence `json:"evidence"`
	Text              string     `json:"text"`
}
type Report struct {
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Results  []Result  `json:"results"`
	Markdown string    `json:"markdown"`
	Word     string    `json:"word"`
	Manifest string    `json:"manifest"`
}
type Collector func(context.Context, config.InspectionDevice, config.InspectionCheck) (string, []byte, error)

func Validate(cfg config.Config) error {
	seen := map[string]bool{}
	for _, d := range cfg.Inspection.Devices {
		if d.Name == "" || seen[d.Name] || len(d.Checks) == 0 {
			return fmt.Errorf("巡检设备名称必须非空且唯一，检查项不能为空：%s", d.Name)
		}
		seen[d.Name] = true
		if (d.Target == "") == (d.URL == "") {
			return fmt.Errorf("%s 必须且只能指定 target 或 url", d.Name)
		}
		if d.URL != "" {
			if err := validateWebDevice(d); err != nil {
				return err
			}
		} else {
			found := false
			for _, t := range cfg.Targets {
				if t.Name == d.Target {
					found = true
					if t.Protocol != "ssh" && t.Protocol != "telnet" {
						return fmt.Errorf("%s 巡检目标必须为 SSH/Telnet", d.Name)
					}
				}
			}
			if !found {
				return fmt.Errorf("未找到巡检目标 %s", d.Target)
			}
		}
		names := map[string]bool{}
		for _, c := range d.Checks {
			if c.Name == "" || names[c.Name] {
				return fmt.Errorf("%s 检查项名称必须非空且唯一", d.Name)
			}
			names[c.Name] = true
			if d.Target != "" && !readCommand(c.Command) {
				return fmt.Errorf("%s/%s 不支持此只读命令，请使用明确的 show/display/get 或系统状态命令", d.Name, c.Name)
			}
			if d.Target != "" && c.Screenshot {
				return fmt.Errorf("%s/%s SSH 检查保存原始文本证据，截图只适用于网页", d.Name, c.Name)
			}
			if d.URL != "" && (c.Selector == "" || c.Command != "") {
				return fmt.Errorf("%s/%s 网页检查需要 selector，不能带 command", d.Name, c.Name)
			}
			for _, pattern := range []string{c.ExpectRegex, c.AlertRegex, c.NumericRegex} {
				if pattern != "" {
					if _, err := regexp.Compile(pattern); err != nil {
						return fmt.Errorf("%s/%s 正则表达式无效", d.Name, c.Name)
					}
				}
			}
			if c.MaxValue != nil && (c.NumericRegex == "" || regexp.MustCompile(c.NumericRegex).NumSubexp() != 1) {
				return fmt.Errorf("%s/%s 数值阈值需要恰好一个捕获组", d.Name, c.Name)
			}
		}
	}
	return nil
}
func readCommand(s string) bool {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "\n\r;&|`$><\\\x00") {
		return false
	}
	fields := strings.Fields(strings.ToLower(s))
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "show", "display", "get":
		return len(fields) > 1
	case "uptime", "free", "df", "uname", "hostname", "date", "ss":
		return true
	case "cat":
		return len(fields) == 2 && strings.HasPrefix(fields[1], "/proc/")
	}
	return false
}
func selected(d config.InspectionDevice, selector string) bool {
	if selector == "" || selector == "all" {
		return true
	}
	for _, p := range strings.Split(selector, ",") {
		p = strings.TrimSpace(p)
		if p == d.Name || p == d.Target {
			return true
		}
		for _, tag := range d.Tags {
			if p == tag || p == "tag:"+tag {
				return true
			}
		}
	}
	return false
}
func Run(ctx context.Context, cfg config.Config, selector string) (Report, error) {
	return RunWithCollector(ctx, cfg, selector, nil)
}
func RunWithCollector(ctx context.Context, cfg config.Config, selector string, collect Collector) (Report, error) {
	report := Report{Started: time.Now()}
	if err := Validate(cfg); err != nil {
		return report, err
	}
	var devices []config.InspectionDevice
	for _, d := range cfg.Inspection.Devices {
		if selected(d, selector) {
			devices = append(devices, d)
		}
	}
	if len(devices) == 0 {
		return report, errors.New("没有匹配的巡检设备，请配置 inspection.devices 或检查 selector")
	}
	root := cfg.Inspection.OutputDir
	if root == "" {
		root = "reports/inspections"
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return report, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return report, err
	}
	dir, err := os.MkdirTemp(root, report.Started.Format("20060102-150405")+"-")
	if err != nil {
		return report, err
	}
	// Exclusive baseline lock across CLI and scheduler processes. Never overwrite
	// a concurrent run's baseline or silently discard failures.
	lock, err := os.OpenFile(filepath.Join(root, ".run.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return report, errors.New("已有巡检占用此输出目录；若上次进程异常退出，请确认无任务后删除 .run.lock")
	}
	lock.Close()
	defer os.Remove(filepath.Join(root, ".run.lock"))
	baseline := map[string]string{}
	raw, _ := os.ReadFile(filepath.Join(root, "baseline.json"))
	_ = json.Unmarshal(raw, &baseline)
	var mu sync.Mutex
	var wg sync.WaitGroup
	concurrency := cfg.Inspection.Concurrency
	if concurrency <= 0 {
		concurrency = 3
	}
	concurrency = min(concurrency, 10)
	sem := make(chan struct{}, concurrency)
	timeout := cfg.Inspection.DeviceTimeoutSec
	if timeout <= 0 {
		timeout = 180
	}
	for index, device := range devices {
		wg.Add(1)
		go func(index int, d config.InspectionDevice) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				for _, c := range d.Checks {
					report.Results = append(report.Results, Result{Device: d.Name, Check: c.Name, Status: "failed", Detail: "巡检已取消"})
				}
				mu.Unlock()
				return
			}
			defer func() { <-sem }()
			deviceCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
			defer cancel()
			collector := collect
			cleanup := func() {}
			var initErr error
			if collector == nil {
				if d.URL != "" {
					collector, cleanup, initErr = newWebCollector(deviceCtx, cfg.BrowserBinary, d)
				} else {
					collector = func(c context.Context, _ config.InspectionDevice, check config.InspectionCheck) (string, []byte, error) {
						for _, t := range cfg.Targets {
							if t.Name == d.Target {
								out, e := executor.RunOnTargetWithStop(t, check.Command, c.Done())
								return out, nil, e
							}
						}
						return "", nil, errors.New("设备目标已不存在")
					}
				}
			}
			defer cleanup()
			for i, c := range d.Checks {
				item := Result{Device: d.Name, Check: c.Name, CollectedAt: time.Now(), Status: "failed"}
				var text string
				var screenshot []byte
				err := initErr
				if err == nil {
					checkCtx, stop := context.WithTimeout(deviceCtx, 45*time.Second)
					text, screenshot, err = collector(checkCtx, d, c)
					stop()
				}
				text = redact(text, d, cfg)
				if len(text) > 128<<10 {
					text = text[:128<<10]
					err = errors.New("采集内容超过 128 KiB，结果不完整；请缩小检查范围")
				}
				item.Text = text
				if err != nil {
					item.Detail = redact(err.Error(), d, cfg)
				} else {
					item.Status, item.Detail = evaluate(c, text)
					if c.Screenshot && len(screenshot) == 0 {
						item.Status = "failed"
						item.Detail = "未取得要求的截图证据"
					}
				}
				prefix := fmt.Sprintf("%03d-%03d", index, i)
				if text != "" {
					ev, e := saveEvidence(dir, prefix+".txt", []byte(text))
					if e != nil {
						item.Status = "failed"
						item.Detail = "证据保存失败"
					} else {
						item.Evidence = append(item.Evidence, ev)
					}
				}
				if len(screenshot) > 0 {
					ev, e := saveEvidence(dir, prefix+".png", screenshot)
					if e != nil {
						item.Status = "failed"
						item.Detail = "截图保存失败"
					} else {
						item.Evidence = append(item.Evidence, ev)
					}
				}
				definition, _ := json.Marshal(c)
				key := d.Name + "\x00" + c.Name + "\x00" + digest(append([]byte(d.Target+"|"+d.URL), definition...))
				hash := digest([]byte(text))
				mu.Lock()
				old, ok := baseline[key]
				item.BaselineAvailable = ok
				item.Changed = ok && old != hash
				if item.Status != "failed" {
					baseline[key] = hash
				}
				report.Results = append(report.Results, item)
				mu.Unlock()
			}
		}(index, device)
	}
	wg.Wait()
	report.Finished = time.Now()
	sort.Slice(report.Results, func(i, j int) bool {
		a, b := report.Results[i], report.Results[j]
		if a.Device == b.Device {
			return a.Check < b.Check
		}
		return a.Device < b.Device
	})
	report.Markdown = filepath.Join(dir, "report.md")
	report.Word = filepath.Join(dir, "report.docx")
	report.Manifest = filepath.Join(dir, "manifest.json")
	if err = writeReports(dir, report); err != nil {
		return report, err
	}
	raw, _ = json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(report.Manifest, raw, 0600); err != nil {
		return report, err
	}
	raw, _ = json.MarshalIndent(baseline, "", "  ")
	tmp := filepath.Join(dir, "baseline.tmp")
	if err = os.WriteFile(tmp, raw, 0600); err == nil {
		err = os.Rename(tmp, filepath.Join(root, "baseline.json"))
	}
	if err != nil {
		return report, err
	}
	failed := 0
	for _, r := range report.Results {
		if r.Status == "failed" {
			failed++
		}
	}
	if failed > 0 {
		return report, fmt.Errorf("%d 项未完成，已生成包含失败项的报告", failed)
	}
	return report, nil
}
func evaluate(c config.InspectionCheck, text string) (string, string) {
	if strings.TrimSpace(text) == "" {
		return "failed", "未取得检查内容"
	}
	if c.AlertRegex != "" && regexp.MustCompile(c.AlertRegex).MatchString(text) {
		return "alert", "命中告警规则"
	}
	if c.ExpectRegex != "" && !regexp.MustCompile(c.ExpectRegex).MatchString(text) {
		return "alert", "未满足预期状态"
	}
	if c.MaxValue != nil {
		m := regexp.MustCompile(c.NumericRegex).FindStringSubmatch(text)
		if len(m) != 2 {
			return "failed", "未能提取阈值数值"
		}
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return "failed", "阈值数值无法解析"
		}
		if n > *c.MaxValue {
			return "alert", fmt.Sprintf("数值 %.2f 超过阈值 %.2f", n, *c.MaxValue)
		}
	}
	if c.ExpectRegex != "" || c.AlertRegex != "" || c.MaxValue != nil {
		return "pass", "满足已配置规则（不等于设备全面无风险）"
	}
	return "observed", "已采集；未配置判定规则，需人工复核"
}
func redact(s string, d config.InspectionDevice, cfg config.Config) string {
	secrets := []string{os.Getenv(d.PasswordEnv), os.Getenv(d.UsernameEnv)}
	for _, t := range cfg.Targets {
		if t.Name == d.Target {
			secrets = append(secrets, t.Password, t.KeyPassphrase, t.EnablePassword)
		}
	}
	for _, v := range secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, "[REDACTED]")
		}
	}
	return s
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func saveEvidence(dir, name string, b []byte) (Evidence, error) {
	if len(b) > 20<<20 {
		return Evidence{}, errors.New("证据超过 20 MiB")
	}
	err := os.WriteFile(filepath.Join(dir, name), b, 0600)
	return Evidence{Path: name, SHA256: digest(b)}, err
}
