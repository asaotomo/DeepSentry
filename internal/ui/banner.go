package ui

import (
	"fmt"
)

// 可通过 -ldflags "-X ai-edr/internal/ui.Version=... -X ai-edr/internal/ui.BuildTime=..." 注入
var (
	Version   = "2.0.5"
	BuildTime = "dev"
)

const (
	StudioName     = "Hx0 Studio"
	StudioURL      = "https://hx0studio.com/"
	StudioSiteLine = "官网 · " + StudioURL
)

// LogoArt DeepSentry ASCII 标识（CLI / TUI 共用）— 定义见 logo.go

func PrintBanner() {
	fmt.Printf("%s\n", TerminalText("\033[1;36m"+LogoArt+"\033[0m"))

	fmt.Printf("%s\n", TerminalText(fmt.Sprintf("\033[1;33m :: DeepSentry %s      :: \033[0m", Version)))
	fmt.Println(TerminalText("\033[0;90m :: 深海哨兵 · AI 驱动的安全应急与智能运维 Agent ::\033[0m"))
	fmt.Println(TerminalText(fmt.Sprintf("\033[1;32m :: Studio             :: \033[0m   %s", StudioName)))
	fmt.Println(TerminalText(fmt.Sprintf("\033[1;32m :: Site               :: \033[0m   %s", StudioURL)))
	fmt.Printf("%s\n", TerminalText("\033[1;32m :: Author             :: \033[0m   asaotomo"))
	fmt.Printf("%s\n", TerminalText(fmt.Sprintf("\033[1;34m :: Build Time         :: \033[0m   %s", BuildTime)))
	fmt.Println(TerminalText("\033[0;90m==============================================================\033[0m"))
}
