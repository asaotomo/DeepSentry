package chat

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ai-edr/internal/config"
	"ai-edr/internal/scheduler"
)

// replyRoute remembers where a chat conversation can be reached, so a scheduled
// reply_text task created inside that conversation is delivered back to it
// instead of only landing in the TUI inbox.
type replyRoute struct {
	Channel  string    `json:"channel"`
	Platform string    `json:"platform"`
	AppID    string    `json:"app_id"`
	Message  Message   `json:"message"`
	At       time.Time `json:"at"`
}

const maxReplyRoutes = 4000

// rememberReplyRouteLocked records the freshest routing for a conversation,
// keyed by the owner-derived session prefix that scheduled tasks carry.
func (s *Service) rememberReplyRouteLocked(c config.ChatChannel, own string, m Message) error {
	if !isQuick(c.Platform) {
		return nil
	}
	prefix := sessionOwnerPrefix(own)
	s.replyRoutes[prefix] = replyRoute{
		Channel: c.Name, Platform: c.Platform, AppID: c.AppID,
		Message: Message{User: m.User, Chat: m.Chat, Group: m.Group, ID: m.ID, ContextToken: m.ContextToken, ReplyURL: m.ReplyURL, ReplyExpires: m.ReplyExpires},
		At:      time.Now(),
	}
	if len(s.replyRoutes) <= maxReplyRoutes {
		return s.saveReplyRoutesLocked()
	}
	oldestKey, oldest := "", time.Now()
	for k, r := range s.replyRoutes {
		if r.At.Before(oldest) {
			oldestKey, oldest = k, r.At
		}
	}
	if oldestKey != "" {
		delete(s.replyRoutes, oldestKey)
	}
	return s.saveReplyRoutesLocked()
}

// resolveReplyRouteLocked maps a scheduler session id back to a live channel and
// routing message. It only succeeds for conversations the daemon has served, so
// a reply for an unknown session remains pending until its route is available.
func (s *Service) resolveReplyRouteLocked(session string) (config.ChatChannel, Message, bool) {
	for prefix, route := range s.replyRoutes {
		if !strings.HasPrefix(session, prefix) {
			continue
		}
		c, ok := s.channels[route.Channel]
		if !ok || c.Platform != route.Platform || c.AppID != route.AppID {
			return config.ChatChannel{}, Message{}, false
		}
		return c, route.Message, true
	}
	return config.ChatChannel{}, Message{}, false
}

func (s *Service) saveReplyRoutesLocked() error {
	path := s.cfg.Store + ".routes.json"
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return writeConfirmJSON(path, s.replyRoutes)
}

func (s *Service) restoreReplyRoutes() error {
	data, err := os.ReadFile(s.cfg.Store + ".routes.json")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var routes map[string]replyRoute
	if err = json.Unmarshal(data, &routes); err != nil {
		return fmt.Errorf("chat reply routes: %w", err)
	}
	if routes != nil {
		s.replyRoutes = routes
	}
	return nil
}

// Only acknowledge a pending result after the outbox has durably accepted it.
// Unknown routes stay pending until the user reconnects the originating chat.
func (s *Service) drainScheduledReplies() error {
	replies, err := scheduler.PendingChatReplies(s.inboxStore)
	if err != nil {
		return err
	}
	for _, reply := range replies {
		if s.routeScheduledReply(reply.Line) {
			if err := scheduler.AckChatReply(s.inboxStore, reply.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) scheduleInboxWorker() {
	defer s.wg.Done()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		if err := s.drainScheduledReplies(); err != nil {
			log.Printf("schedule delivery pending: %v", err)
		}
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Service) routeScheduledReply(ln scheduler.ChatInboxLine) bool {
	if strings.TrimSpace(ln.Session) == "" || (ln.Kind != "result" && ln.Kind != "error") {
		return false
	}
	text := strings.TrimSpace(ln.Text)
	if text == "" {
		return false
	}
	s.mu.Lock()
	c, m, ok := s.resolveReplyRouteLocked(ln.Session)
	s.mu.Unlock()
	if !ok {
		return false
	}
	at := ln.At
	if at.IsZero() {
		at = time.Now()
	}
	// A stable id keyed on the task run dedups re-reads after a rotation or
	// restart; the durable outbox skips an id it already holds.
	payload, err := json.Marshal(ln)
	if err != nil {
		return false
	}
	id := fmt.Sprintf("sched-%x", sha256.Sum256(payload))
	if err := s.queueDelivery(delivery{id: id, created: at, channel: c, message: m, text: text}); err != nil {
		log.Printf("schedule outbox write failed: %v", err)
		return false
	}
	return true
}
