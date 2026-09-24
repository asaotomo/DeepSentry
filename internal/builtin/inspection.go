package builtin

import (
	"ai-edr/internal/config"
	"ai-edr/internal/inspection"
	"context"
	"fmt"
)

func InspectionRun(args map[string]string) (string, error) {
	action := arg(args, "action")
	if action == "" || action == "inventory" {
		return inspection.Inventory(config.GlobalConfig, arg(args, "selector"))
	}
	var report inspection.Report
	var err error
	switch action {
	case "collect":
		report, err = inspection.Run(context.Background(), config.GlobalConfig, arg(args, "selector"))
	case "report":
		source := arg(args, "source", "markdown_path", "manifest_path")
		report, err = inspection.CompileSource(config.GlobalConfig, source)
	default:
		return "", fmt.Errorf("action 仅支持 inventory/collect/report")
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Markdown: %s\nWord: %s\n证据清单: %s\n可用 chat_send_file 发送 Word；Markdown 引用的图片与原始证据保存在同目录。不必手写 JSON 或安装 pandoc。", report.Markdown, report.Word, report.Manifest), nil
}
