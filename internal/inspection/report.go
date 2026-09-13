package inspection

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
)

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' && r != '\r' {
			return -1
		}
		return r
	}, s)))
	return b.String()
}

func writeReports(dir string, report Report) error {
	var md strings.Builder
	title := "DeepSentry 多设备巡检报告"
	md.WriteString("# " + title + "\n\n")
	counts := map[string]int{}
	for _, r := range report.Results {
		counts[r.Status]++
	}
	summary := fmt.Sprintf("采集开始：%s；结束：%s。正常 %d，告警 %d，失败 %d，待复核 %d。", report.Started.Format("2006-01-02 15:04:05 -0700"), report.Finished.Format("15:04:05 -0700"), counts["pass"], counts["alert"], counts["failed"], counts["observed"])
	md.WriteString(summary + "\n\n检查范围与日志时间范围以配置及页面实际内容为准；未采集、无权限和超时不计为正常。\n\n")

	coverTitle := title
	if device := singleReportDevice(report.Results); device != "" {
		coverTitle = device
	}
	coverTitle, coverSub := cleanCoverTitle(coverTitle)
	if coverSub == "" {
		coverSub = "设备运行状态、安全告警与证据汇总"
	}
	doc := newDocx()
	doc.cover(coverTitle, coverSub, report.Started, report.Finished)
	doc.heading(1, "巡检概览")
	doc.richPara(summary)
	doc.table([][]string{
		{"结论", "数量", "说明"},
		{"正常", fmt.Sprintf("%d", counts["pass"]), "本项检查通过，且具备实际采集证据"},
		{"告警", fmt.Sprintf("%d", counts["alert"]), "发现风险或不符合基线，需处置"},
		{"失败", fmt.Sprintf("%d", counts["failed"]), "未采集到有效证据或检查执行失败"},
		{"待复核", fmt.Sprintf("%d", counts["observed"]), "已观察但不足以判定通过或告警"},
	}, true)
	index := [][]string{{"序号", "设备", "检查项", "结论", "采集时间"}}
	var alerts []Result
	for i, r := range report.Results {
		index = append(index, []string{
			fmt.Sprintf("%d", i+1),
			r.Device,
			r.Check,
			statusLabel(r.Status),
			reportTime(r.CollectedAt),
		})
		if r.Status == "alert" || r.Status == "failed" {
			alerts = append(alerts, r)
		}
	}
	doc.table(index, true)
	md.WriteString("## 巡检概览\n\n| 设备 | 检查项 | 结论 | 采集时间 |\n| --- | --- | --- | --- |\n")
	for _, r := range report.Results {
		fmt.Fprintf(&md, "| %s | %s | %s | %s |\n", mdCell(r.Device), mdCell(r.Check), statusLabel(r.Status), reportTime(r.CollectedAt))
	}
	md.WriteString("\n")

	if len(alerts) > 0 {
		doc.heading(2, "异常清单")
		md.WriteString("## 异常清单\n\n")
		var items []string
		for _, r := range alerts {
			fmt.Fprintf(&md, "- **%s / %s**（%s）：%s\n", mdCell(r.Device), mdCell(r.Check), statusLabel(r.Status), mdCell(r.Detail))
			items = append(items, "**"+r.Device+" / "+r.Check+"**（"+statusLabel(r.Status)+"）"+r.Detail)
		}
		doc.bullets(items)
		md.WriteString("\n")
	}

	doc.heading(1, "巡检详情")
	currentDevice := ""
	for _, r := range report.Results {
		label := fmt.Sprintf("%s / %s — %s", r.Device, r.Check, statusLabel(r.Status))
		md.WriteString("## " + strings.ReplaceAll(label, "\n", " ") + "\n\n")
		if r.Device != currentDevice {
			currentDevice = r.Device
			doc.heading(2, r.Device)
		}
		doc.heading(3, r.Check)
		doc.statusLine(r.Status)
		doc.richPara(r.Detail)
		doc.richPara("采集时间：" + reportTime(r.CollectedAt))
		if r.BaselineAvailable {
			if r.Changed {
				doc.richPara("与上次采集相比：**发生变化**")
			} else {
				doc.richPara("与上次采集相比：无变化")
			}
		}

		detail := r.Detail + "；采集时间：" + reportTime(r.CollectedAt)
		if r.BaselineAvailable {
			detail += fmt.Sprintf("；与上次采集不同：%t", r.Changed)
		} else {
			detail += "；无历史基线"
		}
		md.WriteString(detail + "\n\n")
		text := r.Text
		if utf8Len(text) > 1200 {
			text = string([]rune(text)[:1200]) + "\n[报告摘要截断，完整内容见文本证据]"
		}
		md.WriteString("~~~~text\n" + strings.ReplaceAll(text, "~~~~", "~ ~ ~ ~") + "\n~~~~\n\n")
		if strings.TrimSpace(text) != "" {
			doc.caption("原始采集摘要")
			doc.quote(text)
		}
		for _, ev := range r.Evidence {
			md.WriteString(fmt.Sprintf("证据：[%s](%s)，SHA256 `%s`\n\n", ev.Path, ev.Path, ev.SHA256))
			if !strings.HasSuffix(ev.Path, ".png") && !strings.HasSuffix(ev.Path, ".jpg") && !strings.HasSuffix(ev.Path, ".jpeg") {
				continue
			}
			png, e := os.ReadFile(filepath.Join(dir, ev.Path))
			if e != nil {
				return e
			}
			size, format, e := image.DecodeConfig(bytes.NewReader(png))
			if e != nil || (format != "png" && format != "jpeg") || size.Width <= 0 || size.Height <= 0 {
				return fmt.Errorf("无效 PNG 截图证据 %s", ev.Path)
			}
			doc.image(png, format, size, r.Check)
			md.WriteString("![截图证据](" + ev.Path + ")\n\n")
		}
	}
	doc.heading(1, "处置与复核建议")
	if len(alerts) > 0 {
		doc.richPara("本轮共发现 **" + fmt.Sprintf("%d", len(alerts)) + "** 项告警或失败，建议按异常清单逐项处置，再复核待观察项。")
	} else {
		doc.richPara("本轮未发现告警或失败项。建议保持现有基线，并按计划复检。")
	}
	doc.richPara("未采集、无权限和超时不计为正常。检查范围与日志时间范围以配置及页面实际内容为准。截图均来自对应检查项的实际页面。")
	if err := doc.save(report.Word); err != nil {
		return err
	}
	return os.WriteFile(report.Markdown, []byte(md.String()), 0600)
}

func singleReportDevice(results []Result) string {
	name := ""
	for _, r := range results {
		if r.Device == "" {
			continue
		}
		if name == "" {
			name = r.Device
			continue
		}
		if r.Device != name {
			return ""
		}
	}
	return name
}

func mdCell(s string) string { return strings.NewReplacer("|", "\\|", "\n", " ", "\r", " ").Replace(s) }
