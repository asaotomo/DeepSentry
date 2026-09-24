package ui

const robotLogoWidth = 16

// RobotLogoLines TUI 紧凑机器人标识（纯文本，等宽 16 列，双列中轴保证偶数宽文字完全居中）。
func RobotLogoLines() []string {
	if PlainTextMode() {
		return []string{
			"       ()       ",
			"   +---++---+   ",
			" +-+--------+-+ ",
			" |  o      o  | ",
			" |  ~~~~~~~~  | ",
			" |   SENTRY   | ",
			" +-+--------+-+ ",
			"   +---++---+   ",
			"       ||       ",
		}
	}
	return []string{
		"       🔵       ",
		"   ╭───┴┴───╮   ",
		" ╭─┴────────┴─╮ ",
		" │  ●      ●  │ ",
		" │  ≈≈≈≈≈≈≈≈  │ ",
		" │   SENTRY   │ ",
		" ╰─┬────────┬─╯ ",
		"   ╰───┬┬───╯   ",
		"       ││       ",
	}
}

// LogoArt DeepSentry CLI 标识。
//
// 经典 no-tui/stdout 模式常运行在 WebShell、旧终端、字体宽度不可控的环境。
// 这里刻意只使用 ASCII，避免 box drawing / emoji / 宽字符在不同终端里错位。
const LogoArt = `
              |              
      +-------+-------+      
  +---+---------------+---+  
  |       o       o       |  
  |       ~ ~ ~ ~ ~       |  
  |       S E N T R Y     |  
  +---+---------------+---+  
      +-------+-------+      
              |              
`
