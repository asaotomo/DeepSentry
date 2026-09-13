//go:build !386

package chat

import (
	"context"
	"log"
	"strings"

	"ai-edr/internal/config"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

func (s *Service) runFeishu(ctx context.Context, c config.ChatChannel, update func(string)) {
	handler := dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(func(ctx context.Context, e *larkim.P2MessageReceiveV1) error {
		if e == nil || e.Event == nil || e.Event.Message == nil || e.Event.Sender == nil || e.Event.Sender.SenderId == nil {
			return nil
		}
		msg, sender := e.Event.Message, e.Event.Sender
		if ptr(sender.SenderType) != "user" {
			return nil
		}
		text, attachments, err := parseFeishuContent(ptr(msg.MessageType), ptr(msg.Content))
		if err != nil {
			return nil
		}
		for _, mention := range msg.Mentions {
			if mention != nil {
				text = strings.ReplaceAll(text, ptr(mention.Key), "")
			}
		}
		m := Message{ID: ptr(msg.MessageId), User: ptr(sender.SenderId.OpenId), Chat: ptr(msg.ChatId), Text: text, Attachments: attachments, Group: ptr(msg.ChatType) == "group"}
		reply, err := s.command(c, m)
		if err != nil {
			return nil
		}
		if reply != "" {
			s.enqueue(delivery{channel: c, message: m, text: reply})
		}
		return nil
	})
	ws := larkws.NewClient(c.AppID, secret(c), larkws.WithEventHandler(handler), larkws.WithHttpClient(s.client), larkws.WithWebSocketDialer(socketDialer()), larkws.WithLogLevel(larkcore.LogLevelError), larkws.WithOnReady(func() { update("connected") }), larkws.WithOnReconnecting(func() { update("reconnecting") }), larkws.WithOnReconnected(func() { update("connected") }), larkws.WithOnDisconnected(func() { update("disconnected") }))
	if err := ws.Start(ctx); err != nil && ctx.Err() == nil {
		update("connection_failed")
		log.Printf("chat channel=%s 长连接失败，请检查应用凭据与事件订阅配置", c.Name)
	}
}
