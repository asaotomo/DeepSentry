package harness

import (
	"strings"

	"ai-edr/internal/analyzer"
)

const multiTurnFollowUpPrompt = `
【多轮会话 · 追问模式】
用户在同一 Session 内延续上一轮对话（类似 Claude Code 连续追问）。
- 结合上文结论、命令输出与 final_report，勿从零重复已完成工作。
- 不要把本次追问当成新会话开场：禁止再次自我介绍、闪亮登场或重复报能力清单。
- 直接回应用户最新一句话；只有用户明确问“你是谁/你叫什么”时才介绍自己。
- 若用户仅追问细节/解释，可基于已有证据直接 finish。
- 若需新取证，继续 execute/tool；finish 仅表示本回合答复完毕，Session 仍可继续。
`

const chatReplyAlwaysPrompt = `
【手机 IM】
你正在通过微信/飞书/QQ/企微私聊回复。气泡要短、好扫、像熟人发微信。
- final_report 只写发给用户的正文，不要会话 ID、步骤、系统状态、工具日志。
- 不要使用 Markdown 标题、表格或 **加粗**；用短句和换行。
- 闲聊（你好/在吗/你是谁）一两句即可，不要自我介绍或能力清单，除非用户明确要你介绍。
- 以最新用户消息为本轮任务。问候和询问是否在线不代表继续历史任务：直接回答，不调用工具、不恢复之前的浏览器/文件操作或等待确认的动作。只有用户明确要求继续旧任务时才恢复执行。
- 登录巡检优先复用已连接的 HawkEye 工具。图片验证码先截图并调用 chat_send_file 准备图片，然后使用 ask_user；短信验证码或其他关键输入直接使用 ask_user，等待聊天回复后在原浏览器任务继续，不要 finish 提问；不重复登录已成功的设备。不要承诺所有验证码都能自动识别。
- 有任务结论时先说结论，细节最多 5 条短句。
`

const chatReplyFollowUpPrompt = `
【续聊】
这是同一聊天的下一句，不是新开机。禁止再次闪亮登场或重复报能力。直接接话。
`

// MultiTurnExtraPrompt 多轮追问时注入的 system 补充
func MultiTurnExtraPrompt(multiTurn bool, history *[]analyzer.Message) string {
	if !multiTurn || history == nil || CountUserTurns(*history) < 2 {
		return ""
	}
	return multiTurnFollowUpPrompt
}

// ChatReplyExtraPrompt 手机/IM 从第一轮就约束回复形态。
func ChatReplyExtraPrompt(chatReply bool, history *[]analyzer.Message) string {
	if !chatReply {
		return ""
	}
	if history != nil && CountUserTurns(*history) >= 2 {
		return chatReplyAlwaysPrompt + chatReplyFollowUpPrompt
	}
	return chatReplyAlwaysPrompt
}

// CountUserTurns 统计用户发言轮次
func CountUserTurns(history []analyzer.Message) int {
	n := 0
	for _, m := range history {
		if isRealUserTurn(m) {
			n++
		}
	}
	return n
}

// isRealUserTurn separates actual user input from tool feedback and control
// messages. Those observations intentionally use role=user for chat API
// compatibility, but they are not conversation turns.
func isRealUserTurn(m analyzer.Message) bool {
	return analyzer.IsRealUserTurn(m)
}

// CommitFinishToHistory 将 finish 结论写入 history，供下一轮追问引用
func CommitFinishToHistory(history *[]analyzer.Message, action AgentAction, report string) {
	if history == nil {
		return
	}
	report = strings.TrimSpace(report)
	if report == "" {
		report = strings.TrimSpace(action.Thought)
	}
	*history = append(*history, analyzer.Message{
		Role:    "assistant",
		Content: actionToJSON(action),
	})
	if report != "" {
		*history = append(*history, analyzer.Message{
			Role:    "system",
			Content: "【系统】本轮已结束。以下是结论摘要，供后续追问参考：\n" + report,
		})
	}
}
