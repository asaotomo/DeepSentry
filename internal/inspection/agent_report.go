package inspection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ai-edr/internal/config"
)

// Inventory intentionally does not require CSS selectors: an interactive Agent
// can use the existing HawkEye browser and ask for missing inputs in chat.
func Inventory(cfg config.Config, selector string) (string, error) {
	var devices []map[string]any
	for _, d := range cfg.Inspection.Devices {
		if selected(d, selector) {
			devices = append(devices, map[string]any{"name": d.Name, "url": d.URL, "target": d.Target, "tags": d.Tags, "username_env": d.UsernameEnv, "password_env": d.PasswordEnv})
		}
	}
	if len(devices) == 0 {
		return "", errors.New("没有配置匹配的设备；可在聊天提供设备地址和巡检项，或配置 inspection.devices 的 name/url/target")
	}
	raw, _ := json.MarshalIndent(devices, "", "  ")
	return string(raw) + `
流程：复用 HawkEye 浏览器登录并检查用户指定的运行状态、安全告警和日志时间范围；只读检查，不做设备变更。缺少验证码/MFA/关键输入时 ask_user 等待聊天回复，在同一浏览器继续。保留真实截图路径和采集时间。巡检结束后直接调用 inspection_run action=report（不要手写 JSON、不要装 pandoc）；系统会从当前或最新会话报告提取结论和截图，生成 Word/Markdown。需要指定文件时用 source=路径.md。最后 chat_send_file 发送 Word。
没有实际采集证据不得伪造截图、日期或结论。不要把验证码、密码写进报告。`, nil
}
func Compile(cfg config.Config, manifestPath string) (Report, error) {
	var report Report
	f, err := os.Open(manifestPath)
	if err != nil {
		return report, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	f.Close()
	if err != nil || len(raw) > 2<<20 {
		return report, errors.New("报告输入超过 2 MiB 或读取失败")
	}
	if err = json.Unmarshal(raw, &report); err != nil {
		return report, errors.New("报告 JSON 无效")
	}
	if len(report.Results) == 0 || len(report.Results) > 200 {
		return report, errors.New("报告需要 1–200 项检查结果")
	}
	if report.Started.IsZero() {
		report.Started = time.Now()
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
	dir, err := os.MkdirTemp(root, report.Started.Format("20060102-150405")+"-agent-")
	if err != nil {
		return report, err
	}
	report.Markdown = filepath.Join(dir, "report.md")
	report.Word = filepath.Join(dir, "report.docx")
	report.Manifest = filepath.Join(dir, "manifest.json")
	totalEvidence := int64(0)
	for i := range report.Results {
		r := &report.Results[i]
		if r.Device == "" || r.Check == "" {
			return report, errors.New("检查结果缺少设备或检查项名称")
		}
		switch r.Status {
		case "pass", "alert", "failed", "observed":
		default:
			return report, errors.New("报告状态必须为 pass/alert/failed/observed")
		}
		if r.CollectedAt.IsZero() {
			r.Status = "failed"
			r.Detail = "缺少实际采集时间；" + r.Detail
		}
		if len(r.Evidence) > 8 {
			return report, errors.New("每项最多 8 个证据文件")
		}
		for j := range r.Evidence {
			ev := &r.Evidence[j]
			path := ev.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(filepath.Dir(manifestPath), path)
			}
			source, e := os.Open(path)
			if e != nil {
				r.Status = "failed"
				r.Detail += "；证据文件不可读取"
				ev.Path = ""
				continue
			}
			stat, e := source.Stat()
			if e != nil || !stat.Mode().IsRegular() || stat.Size() > maxEvidenceBytes {
				source.Close()
				return report, errors.New("证据必须为不超过 20 MiB 的普通文件")
			}
			b, e := io.ReadAll(io.LimitReader(source, maxEvidenceBytes+1))
			source.Close()
			if e != nil || len(b) > maxEvidenceBytes {
				return report, errors.New("证据文件读取失败")
			}
			totalEvidence += int64(len(b))
			if totalEvidence > 100<<20 {
				return report, errors.New("单份报告证据总量超过 100 MiB，请分批生成")
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".txt" && ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
				return report, errors.New("证据仅支持 PNG/JPEG 截图或 TXT 原始输出")
			}
			if ext != ".txt" {
				size, kind, e := image.DecodeConfig(bytes.NewReader(b))
				if e != nil || (kind != "png" && kind != "jpeg") || size.Width <= 0 || size.Height <= 0 {
					return report, errors.New("截图证据无效")
				}
				if kind == "jpeg" {
					ext = ".jpg"
				} else {
					ext = ".png"
				}
			}
			if ev.SHA256 != "" && ev.SHA256 != digest(b) {
				return report, errors.New("证据校验值不匹配")
			}
			saved, e := saveEvidence(dir, fmt.Sprintf("%03d-%02d%s", i, j, ext), b)
			if e != nil {
				return report, e
			}
			*ev = saved
		}
		var valid []Evidence
		for _, ev := range r.Evidence {
			if ev.Path != "" {
				valid = append(valid, ev)
			}
		}
		r.Evidence = valid
		if len(valid) == 0 && (r.Status == "pass" || r.Status == "alert") {
			r.Status = "failed"
			r.Detail = "没有证据，不能标记检查通过；" + r.Detail
		}
		for _, d := range cfg.Inspection.Devices {
			if d.Name == r.Device {
				r.Text = redact(r.Text, d, cfg)
				r.Detail = redact(r.Detail, d, cfg)
			}
		}
	}
	if err = writeReports(dir, report); err != nil {
		return report, err
	}
	raw, _ = json.MarshalIndent(report, "", "  ")
	err = os.WriteFile(report.Manifest, raw, 0600)
	return report, err
}

const maxEvidenceBytes = 20 << 20
