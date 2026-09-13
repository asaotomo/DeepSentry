package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"ai-edr/internal/config"
)

type OutboxItem struct {
	File       *sendFile          `json:"file,omitempty"`
	JobID      string             `json:"job_id,omitempty"`
	Ephemeral  bool               `json:"ephemeral,omitempty"`
	ConfirmSeq int64              `json:"confirm_seq,omitempty"`
	ID         string             `json:"id"`
	Channel    config.ChatChannel `json:"channel"`
	Message    Message            `json:"message"`
	Chunks     []string           `json:"chunks"`
	Next       int                `json:"next"`
	Attempts   int                `json:"attempts"`
	Status     string             `json:"status"`
	Error      string             `json:"error,omitempty"`
	Created    time.Time          `json:"created"`
	RetryAt    time.Time          `json:"retry_at"`
}
type outboxState struct {
	mu    sync.Mutex
	items map[string]*OutboxItem
	busy  map[string]bool
}

func (s *Service) outboxPath(id string) string {
	return filepath.Join(s.cfg.Store+".outbox", id+".json")
}
func (s *Service) saveOutbox(i *OutboxItem) error {
	if err := os.MkdirAll(filepath.Dir(s.outboxPath(i.ID)), 0700); err != nil {
		return err
	}
	return writeConfirmJSON(s.outboxPath(i.ID), i)
}
func (s *Service) restoreOutbox() error {
	files, err := filepath.Glob(filepath.Join(s.cfg.Store+".outbox", "*.json"))
	if err != nil {
		return err
	}
	s.box.mu.Lock()
	defer s.box.mu.Unlock()
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var item OutboxItem
		if json.Unmarshal(b, &item) != nil || item.ID == "" || filepath.Base(path) != item.ID+".json" || item.Next < 0 || item.Next > item.partCount() {
			return fmt.Errorf("invalid outbox file %s", filepath.Base(path))
		}
		if item.Status == "delivered" && time.Since(item.Created) > 7*24*time.Hour {
			_ = os.Remove(path)
			if item.File != nil {
				_ = os.RemoveAll(filepath.Join(s.cfg.Store+".outbox", "files", item.ID))
			}
			continue
		}
		if item.Status == "sending" { // A crash after the network write may have delivered it.
			item.Status = "uncertain"
			item.Error = "服务在等待发送回执时退出，请检查聊天后重取结果。"
			if err := s.saveOutbox(&item); err != nil {
				return err
			}
		}
		s.box.items[item.ID] = &item
	}
	return nil
}
func (s *Service) queueDelivery(d delivery) error {
	chunks := deliveryChunks(d.channel.Platform, d.text)
	if len(chunks) == 0 {
		return nil
	}
	i := &OutboxItem{ID: firstNonEmpty(d.id, randomID()), JobID: d.jobID, Ephemeral: d.ephemeral, ConfirmSeq: d.confirmSeq, Channel: d.channel, Message: d.message, Chunks: chunks, Status: "pending", Created: time.Now()}
	if !d.created.IsZero() {
		i.Created = d.created
	}
	// Do not persist application credentials; resolve the active account before sending.
	i.Message.Attachments = nil
	i.Message.Text = ""
	i.Channel.AppSecret = ""
	i.Channel.PairCode = ""
	s.box.mu.Lock()
	defer s.box.mu.Unlock()
	if _, exists := s.box.items[i.ID]; exists {
		return nil
	}
	i.Message.DeliverySeq = 1
	for _, old := range s.box.items {
		if event(old.Channel, old.Message) == event(i.Channel, i.Message) {
			next, err := checkedDeliverySeq(old.Message.DeliverySeq, len(old.Chunks))
			if err != nil {
				return err
			}
			i.Message.DeliverySeq = max(i.Message.DeliverySeq, next)
		}
	}
	if err := s.saveOutbox(i); err != nil {
		return err
	}
	s.box.items[i.ID] = i
	return nil
}
func (s *Service) deliveryChannel(item *OutboxItem) (config.ChatChannel, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.Ephemeral {
		j := s.jobs[item.JobID]
		if !jobLive(j) || j.Status == "cancelling" || (j.SessionID != "" && j.SessionID != s.sessionIDLocked(j.Owner)) || (item.ConfirmSeq == 0 && time.Since(item.Created) > time.Minute) {
			return config.ChatChannel{}, false
		}
		if item.ConfirmSeq != 0 {
			req, err := ReadConfirmRequest(filepath.Join(filepath.Dir(s.cfg.Store), "confirm", item.JobID))
			if err != nil || req.Seq != item.ConfirmSeq {
				return config.ChatChannel{}, false
			}
		}
	}
	c, ok := s.channels[item.Channel.Name]
	if !ok || c.AppID != item.Channel.AppID || c.Platform != item.Channel.Platform {
		return c, false
	}
	m := item.Message
	allowed := false
	for _, u := range c.AllowedUsers {
		if u == m.User {
			allowed = true
		}
	}
	if s.paired[c.Name] == m.User && m.User != "" {
		allowed = true
	}
	if !allowed {
		return c, false
	}
	if m.Group || len(c.AllowedChats) > 0 {
		allowed = false
		for _, id := range c.AllowedChats {
			if id == m.Chat {
				allowed = true
			}
		}
	}
	return c, allowed
}
func (s *Service) outboxWorker() {
	defer s.wg.Done()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
		}
		s.box.mu.Lock()
		ordered := make([]*OutboxItem, 0, len(s.box.items))
		for _, i := range s.box.items {
			if i.Status == "pending" {
				ordered = append(ordered, i)
			}
		}
		sort.Slice(ordered, func(a, b int) bool { return ordered[a].Created.Before(ordered[b].Created) })
		var chosen *OutboxItem
		lanes := map[string]bool{}
		for _, i := range ordered {
			lane := owner(i.Channel, i.Message)
			if lanes[lane] {
				continue
			}
			lanes[lane] = true
			if s.box.busy[lane] || time.Now().Before(i.RetryAt) {
				continue
			}
			chosen = i
			s.box.busy[lane] = true
			break
		}
		s.box.mu.Unlock()
		if chosen == nil {
			continue
		}
		s.deliverOutbox(chosen)
		s.box.mu.Lock()
		delete(s.box.busy, owner(chosen.Channel, chosen.Message))
		s.box.mu.Unlock()
	}
}
func (s *Service) deliverOutbox(i *OutboxItem) {
	_, ok := s.deliveryChannel(i)
	s.box.mu.Lock()
	if !ok {
		i.RetryAt = time.Now().Add(5 * time.Second)
		if i.Ephemeral || time.Since(i.Created) > 24*time.Hour {
			i.Status = "failed"
			i.Error = "通道或接收者已不可用"
		}
		_ = s.saveOutbox(i)
		s.box.mu.Unlock()
		return
	}
	s.box.mu.Unlock()
	for i.Next < i.partCount() {
		if s.ctx.Err() != nil {
			return
		}
		c, ok := s.deliveryChannel(i)
		if !ok {
			s.box.mu.Lock()
			i.Status = "pending"
			i.RetryAt = time.Now().Add(5 * time.Second)
			_ = s.saveOutbox(i)
			s.box.mu.Unlock()
			return
		}
		s.box.mu.Lock()
		i.Status = "sending"
		i.Attempts++
		err := s.saveOutbox(i)
		if err != nil {
			i.Status = "pending"
			i.Attempts--
			i.RetryAt = time.Now().Add(5 * time.Second)
		}
		s.box.mu.Unlock()
		if err != nil {
			log.Printf("chat outbox persist failed: %v", err)
			return
		}
		m := i.Message
		m.DeliveryID = fmt.Sprintf("%s-%d", i.ID, i.Next)
		m.DeliverySeq, err = checkedDeliverySeq(i.Message.DeliverySeq, i.Next)
		timeout := 30 * time.Second
		if i.File != nil {
			timeout = 5 * time.Minute
		}
		ctx, cancel := context.WithTimeout(s.ctx, timeout)
		if err != nil {
			// Invalid stored sequence must fail without a network write.
		} else if i.File != nil {
			err = s.sendOutboundFile(ctx, c, m, i.File.Path, i.File.Name)
		} else {
			err = s.sendChunk(ctx, c, m, i.Chunks[i.Next])
		}
		cancel()
		s.box.mu.Lock()
		if err == nil {
			i.Next++
			i.Attempts = 0
			i.Status = "pending"
			i.Error = ""
			if i.Next == i.partCount() {
				i.Status = "delivered"
			}
		} else {
			i.Error = err.Error()
			i.Status = "failed"
			var pe *platformError
			if errors.As(err, &pe) && pe.Retryable && i.Attempts < 6 {
				i.Status = "pending"
				delay := time.Second * time.Duration(1<<min(i.Attempts, 6))
				if pe.RetryAfter > delay {
					delay = pe.RetryAfter
				}
				i.RetryAt = time.Now().Add(delay)
			} else if errors.As(err, &pe) && pe.Uncertain {
				i.Status = "uncertain"
			}
		}
		persistErr := s.saveOutbox(i)
		s.box.mu.Unlock()
		if persistErr != nil || err != nil {
			return
		}
		if !pause(s.ctx, 250*time.Millisecond) {
			return
		}
	}
}

func (s *Service) recoverJobReplies() error {
	for _, j := range s.jobs {
		if n := j.Notification; n != nil && time.Since(n.Created) < 7*24*time.Hour {
			if err := s.queueDelivery(delivery{id: n.ID, created: n.Created, channel: n.Channel, message: n.Message, text: formatTaskNotify(j.Status, j.ID, j.Result)}); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Service) deliveryStatusLocked(own string) string {
	s.box.mu.Lock()
	defer s.box.mu.Unlock()
	var items []*OutboxItem
	for _, i := range s.box.items {
		if owner(i.Channel, i.Message) == own && !i.Ephemeral {
			items = append(items, i)
		}
	}
	sort.Slice(items, func(a, b int) bool { return items[a].Created.After(items[b].Created) })
	text := "最近回复的投递状态："
	for _, i := range items[:min(5, len(items))] {
		text += fmt.Sprintf("\n%s · %s · %d/%d 段", i.ID, i.Status, i.Next, i.partCount())
		if i.File != nil {
			text += " · 文件：" + i.File.Name
		}
		if i.Error != "" {
			text += " · " + i.Error
		}
	}
	return text + "\nfailed 可用 /retry 投递ID 重试；uncertain 请先检查聊天，重发可能重复。"
}
func (s *Service) retryDeliveryLocked(own, id string, m Message) string {
	s.box.mu.Lock()
	defer s.box.mu.Unlock()
	i := s.box.items[id]
	if i == nil || owner(i.Channel, i.Message) != own || i.Ephemeral {
		return "未找到本对话的投递记录。用 /delivery 查看。"
	}
	if s.box.busy[own] {
		return "当前投递仍在收尾，请稍后重试。"
	}
	if i.Status != "failed" && i.Status != "uncertain" {
		return "该回复无需手动重试。"
	}
	i.Message.ID = m.ID
	i.Message.ContextToken = m.ContextToken
	i.Message.ReplyURL = m.ReplyURL
	i.Message.DeliverySeq = 1
	i.Attempts = 0
	i.Status = "pending"
	i.Error = ""
	i.RetryAt = time.Time{}
	if err := s.saveOutbox(i); err != nil {
		i.Status = "failed"
		return "保存重发请求失败。"
	}
	return "已安排重发尚未确认送达的部分。"
}

func (i *OutboxItem) partCount() int {
	if i.File != nil {
		return 1
	}
	return len(i.Chunks)
}

func checkedDeliverySeq(base uint32, delta int) (uint32, error) {
	if delta < 0 || delta > math.MaxInt32 {
		return 0, errors.New("投递序号超出范围")
	}
	increment := uint32(delta)
	if increment > math.MaxUint32-base {
		return 0, errors.New("投递序号超出范围")
	}
	return base + increment, nil
}
