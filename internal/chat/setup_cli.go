package chat

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"ai-edr/internal/config"
	"github.com/skip2/go-qrcode"
	"golang.org/x/term"
)

type terminalSetup struct {
	ctx  context.Context
	in   *bufio.Reader
	out  io.Writer
	file *os.File
}

func (t *terminalSetup) ask(label string, sensitive bool) (string, error) {
	if t.ctx == nil {
		return t.read(label, sensitive)
	}
	type answer struct {
		text string
		err  error
	}
	ch := make(chan answer, 1)
	go func() { text, err := t.read(label, sensitive); ch <- answer{text, err} }()
	select {
	case a := <-ch:
		return a.text, a.err
	case <-t.ctx.Done():
		return "", t.ctx.Err()
	}
}
func (t *terminalSetup) read(label string, sensitive bool) (string, error) {
	fmt.Fprint(t.out, label+": ")
	if sensitive && t.file != nil && term.IsTerminal(int(t.file.Fd())) {
		b, err := term.ReadPassword(int(t.file.Fd()))
		fmt.Fprintln(t.out)
		return strings.TrimSpace(string(b)), err
	}
	if sensitive {
		return "", errors.New("凭据输入需要真实终端，禁止通过会回显的重定向输入密码；请使用环境变量配置")
	}
	line, err := t.in.ReadString('\n')
	return strings.TrimSpace(line), err
}

// SetupCLI never launches a browser or exposes a setup HTTP endpoint. The chat
// service still owns callback/control endpoints and the existing private store.
func SetupCLI(ctx context.Context, cfg config.ChatConfig, run Runner, input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	svc, err := New(ctx, cfg, run)
	if err != nil {
		return err
	}
	client := *config.HTTPClient(18 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	st := &setupState{cfg: cfg, client: &client, service: svc, pending: map[string]*pendingLogin{}, status: map[string]string{}}
	svc.OnStatus = func(name, status string) {
		st.mu.Lock()
		st.status[strings.TrimSuffix(name, "-quick")] = status
		st.mu.Unlock()
	}
	done := make(chan error, 1)
	go func() { done <- svc.Serve() }()
	attached := false
	select {
	case <-svc.Ready:
	case err := <-done:
		if !isStoreBusy(err) {
			return err
		}
		attached = true
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() {
		cancel()
		if !attached {
			select {
			case <-done:
			case <-time.After(6 * time.Second):
			}
		}
	}()
	st.attach = attached
	if attached {
		st.service = nil
	}
	t := &terminalSetup{ctx: ctx, in: bufio.NewReader(input), out: output}
	t.file, _ = input.(*os.File)
	if t.file != nil && term.IsTerminal(int(t.file.Fd())) {
		if state, e := term.GetState(int(t.file.Fd())); e == nil {
			defer term.Restore(int(t.file.Fd()), state)
		}
	}
	for ctx.Err() == nil {
		fmt.Fprintln(output, "\nDeepSentry 终端接入：1 扫码/授权链接  2 手动添加  3 状态/连接检查  4 解绑  5 测试凭据/连接  0 退出")
		choice, err := t.ask("选择", false)
		if err != nil {
			return nil
		}
		switch choice {
		case "0":
			return nil
		case "1":
			p, e := t.ask("平台 feishu / qq / wechat（企微请用手动 BotID/Secret）", false)
			if e != nil {
				return e
			}
			if p != "feishu" && p != "qq" && p != "wechat" {
				fmt.Fprintln(output, "请选择支持终端扫码的三个平台之一")
				continue
			}
			allow, _ := t.ask("允许此账号执行任务？输入 yes", false)
			st.mu.Lock()
			e = st.begin(ctx, setupInput{Platform: p, AllowBatch: allow == "yes"})
			link := ""
			if pending := st.pending[p]; pending != nil {
				link = pending.URL
			}
			st.mu.Unlock()
			if e != nil {
				fmt.Fprintln(output, e)
				continue
			}
			fmt.Fprintln(output, "请用对应手机客户端扫码或打开授权链接：", link)
			if qr, e := qrcode.New(link, qrcode.Medium); e == nil {
				fmt.Fprintln(output, qr.ToSmallString(false))
			}
			for ctx.Err() == nil {
				action, e := t.ask("扫码后回车查询；验证码输入 code:验证码；输入 cancel 取消", false)
				if e != nil {
					return e
				}
				st.mu.Lock()
				if action == "cancel" {
					delete(st.pending, p)
					st.mu.Unlock()
					break
				}
				pending := st.pending[p]
				if pending == nil {
					st.mu.Unlock()
					break
				}
				delay := time.Until(pending.Next)
				st.mu.Unlock()
				if delay > 0 {
					select {
					case <-time.After(delay):
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				st.mu.Lock()
				e = st.poll(ctx, setupInput{Platform: p, VerifyCode: strings.TrimPrefix(action, "code:")})
				_, waiting := st.pending[p]
				status := st.status[p]
				st.mu.Unlock()
				if e != nil {
					fmt.Fprintln(output, e)
				}
				if !waiting {
					fmt.Fprintln(output, "授权流程状态：", status, "；输入 3 检查连接，向机器人发送 /help 验证消息权限")
					break
				}
				fmt.Fprintln(output, "仍在等待平台授权")
			}
		case "2":
			if err := t.manual(ctx, st, cfg, run); err != nil {
				fmt.Fprintln(output, "添加失败：", err)
			}
		case "3":
			b, e := loadBindings(cfg)
			if e != nil {
				fmt.Fprintln(output, e)
				continue
			}
			statuses, e := daemonStatus(cfg)
			if e != nil {
				fmt.Fprintln(output, "无法读取运行状态：", e)
			}
			keys := make([]string, 0, len(b))
			for k := range b {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				c := channelFor(k, b[k])
				state := statuses[c.Name]
				if state == "" {
					state = statuses[strings.TrimSuffix(c.Name, "-quick")]
				}
				if state == "" {
					state = "已保存；等待连接或回调"
				}
				fmt.Fprintf(output, "%s | %s | %s\n", k, c.Platform, state)
			}
			for _, c := range cfg.Channels {
				fmt.Fprintf(output, "YAML: %s | %s（修改 YAML 管理）\n", c.Name, c.Platform)
			}
			fmt.Fprintln(output, "connected 仅表示传输连接；请主动发 /help 检验入站和回复。HTTP 通道需公网回调配置，不显示为已验证。")
		case "5":
			key, e := t.ask("输入绑定名称", false)
			if e != nil {
				return e
			}
			all, e := loadBindings(cfg)
			if e != nil {
				fmt.Fprintln(output, e)
				continue
			}
			b, ok := all[key]
			if !ok {
				fmt.Fprintln(output, "未找到绑定")
				continue
			}
			c := channelFor(key, b)
			probeCtx, stop := context.WithTimeout(ctx, 15*time.Second)
			result, e := svc.probeChannel(probeCtx, c)
			stop()
			if e != nil {
				fmt.Fprintln(output, "连接检查失败：", e)
			} else {
				fmt.Fprintln(output, result)
			}
		case "4":
			key, e := t.ask("输入状态列表中的绑定名称", false)
			if e != nil {
				return e
			}
			all, e := loadBindings(cfg)
			if e != nil {
				fmt.Fprintln(output, e)
				continue
			}
			b, ok := all[key]
			if !ok {
				fmt.Fprintln(output, "未找到绑定；YAML 通道需修改配置")
				continue
			}
			if e = changeBinding(cfg, key, nil); e != nil {
				fmt.Fprintln(output, e)
				continue
			}
			if attached {
				e = notifyDaemonReload(cfg)
			} else {
				svc.Disconnect(channelFor(key, b).Name)
			}
			fmt.Fprintln(output, "已删除本地绑定并请求断开；平台账号未删除", e)
		default:
			fmt.Fprintln(output, "无效选项")
		}
	}
	return ctx.Err()
}
func (t *terminalSetup) manual(ctx context.Context, st *setupState, cfg config.ChatConfig, run Runner) error {
	p, err := t.ask("平台 feishu_ws / qqbot / weixin / wecom_ws / dingtalk / onebot / wechat", false)
	if err != nil {
		return err
	}
	allowed := map[string]bool{"feishu_ws": true, "qqbot": true, "weixin": true, "wecom_ws": true, "dingtalk": true, "onebot": true, "wechat": true}
	if !allowed[p] {
		return errors.New("不支持的平台")
	}
	name, err := t.ask("连接名称（字母/数字/短横线）", false)
	if err != nil {
		return err
	}
	key := "manual-" + name
	all, err := loadBindings(cfg)
	if err != nil {
		return err
	}
	if _, exists := all[key]; exists {
		return errors.New("同名绑定已存在，请先解绑")
	}
	c := config.ChatChannel{Name: key + "-quick", Platform: p}
	b := binding{Platform: p, Manual: &c}
	if p == "onebot" || p == "wechat" {
		c.APIURL, err = t.ask("桥接 API URL", false)
		if err != nil {
			return err
		}
		c.SecretEnv, err = t.ask("入站密钥环境变量名称（需预先设置）", false)
		if err != nil {
			return err
		}
		c.AccessTokenEnv, err = t.ask("出站 Token 环境变量名称（需预先设置）", false)
		if err != nil {
			return err
		}
	} else {
		c.AppID, err = t.ask("AppID/AppKey/BotID（微信为 bot 标识）", false)
		if err != nil {
			return err
		}
		b.Secret, err = t.ask("AppSecret/BotSecret/微信 Token（不回显）", true)
		if err != nil {
			return err
		}
		c.AppSecret = b.Secret
	}
	users, err := t.ask("允许控制的用户 ID（逗号分隔）", false)
	if err != nil {
		return err
	}
	c.AllowedUsers = splitSetupList(users)
	chats, err := t.ask("允许的群/聊天 ID（逗号分隔；留空仅私聊）", false)
	if err != nil {
		return err
	}
	c.AllowedChats = splitSetupList(chats)
	yes, err := t.ask("允许执行任务？输入 yes", false)
	if err != nil {
		return err
	}
	c.AllowBatch = yes == "yes"
	b.AllowBatch = c.AllowBatch
	trial := cfg
	trial.Channels = append(append([]config.ChatChannel{}, cfg.Channels...), c)
	check, err := New(ctx, trial, run)
	if err != nil {
		return err
	}
	check.stop()
	if err = changeBinding(cfg, key, &b); err != nil {
		return err
	}
	if st.attach {
		err = notifyDaemonReload(cfg)
	} else {
		st.service.Connect(c)
	}
	fmt.Fprintln(t.out, "已保存到私有绑定文件。HTTP 回调渠道请配置 chat.listen 和平台回调地址后重启 --chat；长连接请用状态菜单检查。")
	return err
}
func splitSetupList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
