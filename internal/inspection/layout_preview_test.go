package inspection

import (
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReportLayoutPreview(t *testing.T) {
	dir := os.Getenv("DEEPSENTRY_LAYOUT_PREVIEW")
	if dir == "" {
		t.Skip("explicit visual preview only")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	doc := newDocx()
	now := time.Date(2026, 9, 13, 14, 0, 0, 0, time.FixedZone("CST", 8*3600))
	doc.cover("聊天机器人服务巡检", "连接异常与界面证据", now, now.Add(5*time.Minute))
	doc.heading(1, "巡检概览")
	doc.richPara("本次检查聚焦聊天机器人回复中的 MCP 连接异常。截图显示可选工具初始化失败，但服务器版本查询仍然完成。建议修复运行环境，并将连接提示与任务正文分开显示。")
	doc.table([][]string{{"检查项", "结论", "发现与建议"}, {"版本查询", "正常", "任务输出包含 Ubuntu 22.04.5 LTS，基础查询可继续。"}, {"鹰眼连接", "失败", "node 不在 PATH 中，需检查运行环境。"}, {"FofaMap 连接", "失败", "配置的本地工作目录不存在，需修正路径。"}, {"消息显示", "待复核", "连接警告与正文混排，影响阅读。"}}, true)
	doc.heading(1, "异常与证据")
	doc.heading(2, "聊天窗口中的重复连接提示")
	doc.statusLine("failed")
	doc.richPara("下图为用户提供的界面截图，可用于核对连接提示是否与最终答案分离。")
	raw, err := os.ReadFile(os.Getenv("DEEPSENTRY_LAYOUT_IMAGE"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(os.Getenv("DEEPSENTRY_LAYOUT_IMAGE"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	doc.image(raw, format, cfg, "聊天回复中的 MCP 连接异常")
	doc.heading(1, "处置与复核建议")
	doc.bullets([]string{"核对鹰眼运行所需的 Node.js 路径。", "将 FofaMap 工作目录改为当前服务器实际路径。", "同一会话的可选 MCP 连接失败仅独立提示一次；必需工具导致任务失败时保留错误。"})
	if err := doc.save(filepath.Join(dir, "report-preview.docx")); err != nil {
		t.Fatal(err)
	}
}
