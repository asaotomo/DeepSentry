package chat

import (
	"ai-edr/internal/config"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// Credential checks are read-only; no test messages are sent to chat users.
func (s *Service) probeChannel(ctx context.Context, c config.ChatChannel) (string, error) {
	var err error
	switch c.Platform {
	case "feishu", "feishu_ws":
		_, err = s.feishuToken(ctx, c)
	case "qqbot":
		_, err = s.qqToken(ctx, c)
	case "dingtalk":
		_, err = s.dingToken(ctx, c)
	case "onebot":
		var response map[string]json.RawMessage
		response, err = s.request(ctx, "POST", strings.TrimRight(c.APIURL, "/")+"/get_status", os.Getenv(c.AccessTokenEnv), map[string]any{})
		if err == nil {
			var data struct {
				Online *bool `json:"online"`
				Good   *bool `json:"good"`
			}
			_ = json.Unmarshal(response["data"], &data)
			if data.Online == nil || !*data.Online || data.Good != nil && !*data.Good {
				err = errors.New("OneBot 未报告 online=true 或报告异常状态")
			}
		}
	default:
		states, e := daemonStatus(s.cfg)
		if e != nil {
			return "", e
		}
		state := states[c.Name]
		if state == "" {
			state = states[strings.TrimSuffix(c.Name, "-quick")]
		}
		if state != "connected" {
			return "", errors.New("尚未验证连接；请查看状态并从平台发送 /help 检验回调")
		}
	}
	if err != nil {
		return "", preflightError(err)
	}
	return "平台鉴权/连接检查通过；消息订阅、群权限和文件能力请从平台发送 /help、图片或文件复测。", nil
}
