package chat

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func outboxFixture(t *testing.T) (*Service, *OutboxItem) {
	t.Helper()
	s := testService(t, nil)
	c := testChannel()
	s.channels[c.Name] = c
	if err := s.queueDelivery(delivery{channel: c, message: testMessage("incoming", ""), text: strings.Repeat("字", 1100)}); err != nil {
		t.Fatal(err)
	}
	for _, i := range s.box.items {
		return s, i
	}
	t.Fatal("no item")
	return nil, nil
}
func TestOutboxResumesOnlyUnsentChunksAndHonorsRetryAfter(t *testing.T) {
	s, i := outboxFixture(t)
	var sent []string
	reject := true
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(sent) == 1 && reject {
			reject = false
			resp := jsonResponse(map[string]int{"code": 0})
			resp.StatusCode = 429
			resp.Header.Set("Retry-After", "60")
			return resp, nil
		}
		sent = append(sent, body.Text)
		return jsonResponse(map[string]int{"code": 0}), nil
	})}
	s.channels[i.Channel.Name] = i.Channel
	s.channels[i.Channel.Name] = testChannel()
	c := s.channels[i.Channel.Name]
	c.APIURL = "https://bridge.example/send"
	s.channels[c.Name] = c
	s.deliverOutbox(i)
	if i.Next != 1 || i.Status != "pending" || time.Until(i.RetryAt) < 55*time.Second {
		t.Fatalf("retry state %+v", i)
	}
	s2 := testService(t, nil)
	s2.cfg.Store = s.cfg.Store
	s2.client = s.client
	s2.channels[c.Name] = c
	if err := s2.restoreOutbox(); err != nil {
		t.Fatal(err)
	}
	restored := s2.box.items[i.ID]
	s2.deliverOutbox(restored)
	if restored.Status != "delivered" || len(sent) != 2 || strings.Join(sent, "") != strings.Repeat("字", 1100) {
		t.Fatalf("duplicated/truncated delivery %+v sent=%d", restored, len(sent))
	}
}
func TestOutboxUnknownOutcomeIsNotAutomaticallyRetried(t *testing.T) {
	s, i := outboxFixture(t)
	c := testChannel()
	c.APIURL = "https://bridge.example/send"
	s.channels[c.Name] = c
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("lost response") })}
	s.deliverOutbox(i)
	if i.Status != "uncertain" {
		t.Fatalf("%+v", i)
	}
	i.Status = "sending"
	if err := s.saveOutbox(i); err != nil {
		t.Fatal(err)
	}
	if err := s.restoreOutbox(); err != nil {
		t.Fatal(err)
	}
	if s.box.items[i.ID].Status != "uncertain" {
		t.Fatal("crash replayed unacknowledged write")
	}
}
func TestOutboxDurabilityBeyondObservationBufferAndSecretRedaction(t *testing.T) {
	s := testService(t, nil)
	c := testChannel()
	c.AppSecret = "do-not-store"
	for n := 0; n < 140; n++ {
		s.enqueue(delivery{channel: c, message: testMessage("m", ""), text: "hello"})
	}
	files, _ := filepath.Glob(s.cfg.Store + ".outbox/*.json")
	if len(files) != 140 {
		t.Fatalf("lost messages: %d", len(files))
	}
	for _, path := range files {
		b, _ := os.ReadFile(path)
		if strings.Contains(string(b), "do-not-store") {
			t.Fatal("credential persisted")
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm()&0077 != 0 {
			t.Fatal("public outbox")
		}
	}
}
func TestOutboxRechecksRecipientAndExpiresProgress(t *testing.T) {
	s, i := outboxFixture(t)
	c := testChannel()
	c.AllowedUsers = []string{"bob"}
	s.channels[c.Name] = c
	if _, ok := s.deliveryChannel(i); ok {
		t.Fatal("revoked recipient accepted")
	}
	s.channels[c.Name] = testChannel()
	i.Ephemeral = true
	i.JobID = "finished"
	s.jobs["finished"] = &Job{Status: "completed"}
	if _, ok := s.deliveryChannel(i); ok {
		t.Fatal("stale progress accepted")
	}
}
func TestOutboxFinalRecoveredFromJobWithoutDuplicate(t *testing.T) {
	s := testService(t, nil)
	c := testChannel()
	s.jobs["job"] = &Job{ID: "job", Status: "completed", Result: "answer", Notification: &OutboxItem{ID: "final-job", Channel: c, Message: testMessage("m", ""), Created: time.Now()}}
	if err := s.recoverJobReplies(); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverJobReplies(); err != nil {
		t.Fatal(err)
	}
	if len(s.box.items) != 1 {
		t.Fatal("recovery duplicated result")
	}
}
func TestOutboxSlowConversationDoesNotBlockAnother(t *testing.T) {
	s := testService(t, nil)
	c := testChannel()
	c.APIURL = "https://bridge.example/send"
	s.channels[c.Name] = c
	release := make(chan struct{})
	started := make(chan struct{})
	other := make(chan struct{})
	var once sync.Once
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body struct {
			User string `json:"user_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.User == "alice" {
			once.Do(func() { close(started) })
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		} else {
			select {
			case <-other:
			default:
				close(other)
			}
		}
		return jsonResponse(map[string]int{"code": 0}), nil
	})}
	s.enqueue(delivery{channel: c, message: testMessage("a", ""), text: "one"})
	m := testMessage("b", "")
	m.User = "bob"
	m.Chat = "other"
	s.enqueue(delivery{channel: c, message: m, text: "two"})
	for n := 0; n < 2; n++ {
		s.wg.Add(1)
		go s.outboxWorker()
	}
	defer close(release)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("not started")
	}
	select {
	case <-other:
	case <-time.After(2 * time.Second):
		t.Fatal("unrelated conversation blocked")
	}
}
func TestRetryCannotResendAnotherOwnersMessage(t *testing.T) {
	s, i := outboxFixture(t)
	i.Status = "failed"
	out := s.retryDeliveryLocked("different", i.ID, testMessage("r", ""))
	if !strings.Contains(out, "未找到") || i.Status != "failed" {
		t.Fatal(out)
	}
}

func TestOutboxDoesNotSendUntilSendingStateIsDurable(t *testing.T) {
	s, i := outboxFixture(t)
	c := testChannel()
	c.APIURL = "https://bridge.example/send"
	s.channels[c.Name] = c
	calls := 0
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(map[string]int{"code": 0}), nil
	})}
	path := s.outboxPath(i.ID)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	s.deliverOutbox(i)
	if calls != 0 || i.Status != "pending" || i.Attempts != 0 {
		t.Fatalf("sent before durable intent: calls=%d status=%s attempts=%d", calls, i.Status, i.Attempts)
	}
}

func TestDeliverySequenceRejectsOverflow(t *testing.T) {
	for _, tc := range []struct {
		base  uint32
		delta int
		valid bool
	}{{0, 1, true}, {4294967294, 1, true}, {4294967295, 1, false}, {0, -1, false}} {
		got, err := checkedDeliverySeq(tc.base, tc.delta)
		if (err == nil) != tc.valid {
			t.Fatalf("%+v: %d %v", tc, got, err)
		}
	}
}
