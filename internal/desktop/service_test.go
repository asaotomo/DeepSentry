package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	state        State
	calls        int
	last         Request
	inputError   error
	captureError error
	block        chan struct{}
	entered      chan struct{}
	released     bool
	statusHook   func()
	captureHook  func(string)
	inputHook    func()
	grounding    string
	elements     []Element
	liveName     string
}

func (b *fakeBackend) Status(context.Context) (State, error) {
	if b.statusHook != nil {
		b.statusHook()
	}
	return b.state, nil
}
func (b *fakeBackend) Capture(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.captureHook != nil {
		b.captureHook(path)
	}
	if b.captureError != nil {
		return b.captureError
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, image.NewRGBA(image.Rect(0, 0, 200, 100)))
}
func (b *fakeBackend) Elements(context.Context) (string, []Element, error) {
	if b.grounding == "" && len(b.elements) == 0 {
		return "vision", nil, nil
	}
	grounding := b.grounding
	if grounding == "" {
		grounding = "element"
	}
	return grounding, b.elements, nil
}
func (b *fakeBackend) Input(ctx context.Context, r Request) error {
	if (r.Action == "press_element" || r.Action == "click_element" || r.Action == "set_value") && b.liveName != "" && r.ExpectName != b.liveName {
		return errors.New("element changed; observe again")
	}
	b.calls++
	if b.inputHook != nil {
		b.inputHook()
	}
	b.last = r
	if b.entered != nil {
		close(b.entered)
	}
	if b.block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.block:
		}
	}
	return b.inputError
}
func (b *fakeBackend) Release(context.Context) error { b.released = true; return nil }
func fixture(t *testing.T) (*Service, *fakeBackend) {
	t.Helper()
	b := &fakeBackend{state: State{Ready: true, Width: 100, Height: 50, X: -100, Y: 10, Foreground: "window-1"}}
	s := New(b, t.TempDir())
	t.Cleanup(s.unlockDesktop)
	return s, b
}
func observe(t *testing.T, s *Service) *Observation {
	t.Helper()
	r := s.Call(context.Background(), "test-session", map[string]string{"action": "observe"})
	if !r.OK || r.Observation == nil {
		t.Fatalf("observe: %#v", r)
	}
	return r.Observation
}
func clickArgs(o *Observation) map[string]string {
	return map[string]string{"action": "click", "x": "100", "y": "50", "observation_id": o.ID, "action_id": "test-click-1"}
}
func TestObserveClickMappingAndDedup(t *testing.T) {
	s, b := fixture(t)
	o := observe(t, s)
	a := clickArgs(o)
	r := s.Call(context.Background(), "test-session", a)
	if !r.OK || !r.InputDispatched || r.OutcomeVerified || r.Observation == nil {
		t.Fatalf("result: %#v", r)
	}
	if b.last.X != -50 || b.last.Y != 35 {
		t.Fatalf("retina/negative origin mapping failed: %#v", b.last)
	}
	repeat := s.Call(context.Background(), "test-session", a)
	if repeat.Observation.ID != r.Observation.ID || b.calls != 1 {
		t.Fatal("uncertain retry replayed input")
	}
	a["x"] = "99"
	if got := s.Call(context.Background(), "test-session", a); got.Category != "id_conflict" {
		t.Fatalf("id reuse accepted: %#v", got)
	}
}
func TestFullActionLedgerForgetsExpiredReceipts(t *testing.T) {
	s, _ := fixture(t)
	old := time.Now().Add(-2 * seenRetention)
	for i := 0; i < 4096; i++ {
		s.seen[fmt.Sprintf("test-session\x00old-%d", i)] = seenAction{at: old}
	}
	if r := s.Call(context.Background(), "test-session", clickArgs(observe(t, s))); !r.OK {
		t.Fatalf("full ledger of expired receipts blocked input: %#v", r)
	}
	if len(s.seen) != 1 {
		t.Fatalf("expired receipts kept: %d", len(s.seen))
	}
}

func TestDownscaledScreenshotMapsPixelCenter(t *testing.T) {
	r := Request{Action: "click", X: 10, Y: 10}
	o := Observation{Width: 1280, Height: 720, State: State{Width: 2560, Height: 1440}}
	if err := mapCoordinates(&r, o); err != nil {
		t.Fatal(err)
	}
	if r.X != 21 || r.Y != 21 {
		t.Fatalf("pixel (10,10) of a half-size capture mapped to %d,%d, want its center 21,21", r.X, r.Y)
	}
}

func TestStaleBoundsOwnershipAndForeground(t *testing.T) {
	s, b := fixture(t)
	o := observe(t, s)
	for _, change := range []func(map[string]string){func(a map[string]string) { a["x"] = "200" }, func(a map[string]string) { a["observation_id"] = "old" }, func(a map[string]string) { a["x"] = "-1" }} {
		a := clickArgs(o)
		change(a)
		if s.Call(context.Background(), "test-session", a).OK {
			t.Fatal("invalid coordinates or observation accepted")
		}
	}
	if s.Call(context.Background(), "another-session", map[string]string{"action": "observe"}).Category != "desktop_owned" {
		t.Fatal("session stole desktop")
	}
	b.state.Foreground = "other-window"
	if s.Call(context.Background(), "test-session", clickArgs(o)).Category != "desktop_changed" {
		t.Fatal("foreign foreground accepted")
	}
	if b.calls != 0 {
		t.Fatal("unsafe input was sent")
	}
	b.state = o.State
	s.now = func() time.Time { return o.At.Add(61 * time.Second) }
	if s.Call(context.Background(), "test-session", clickArgs(o)).Category != "stale_observation" {
		t.Fatal("expired screenshot accepted")
	}
}
func TestInputFailureNeverReplayAndReleaseKeys(t *testing.T) {
	s, b := fixture(t)
	a := clickArgs(observe(t, s))
	b.inputError = errors.New("partial delivery")
	r := s.Call(context.Background(), "test-session", a)
	if r.OK || r.RetrySafe || !b.released {
		t.Fatalf("failure unsafe: %#v", r)
	}
	_ = s.Call(context.Background(), "test-session", a)
	if b.calls != 1 {
		t.Fatal("failed action replayed")
	}
}
func TestStopCancelsInFlightAndRequiresResume(t *testing.T) {
	s, b := fixture(t)
	a := clickArgs(observe(t, s))
	b.block = make(chan struct{})
	b.entered = make(chan struct{})
	var result Result
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); result = s.Call(context.Background(), "test-session", a) }()
	<-b.entered
	busy := s.Call(context.Background(), "test-session", map[string]string{"action": "observe"})
	if busy.Category != "busy" {
		t.Fatal("concurrent desktop operation admitted")
	}
	stopped := s.Call(context.Background(), "operator", map[string]string{"action": "stop"})
	if !stopped.Stopped {
		t.Fatal("stop failed")
	}
	wg.Wait()
	if result.RetrySafe || !b.released {
		t.Fatal("cancelled action was not cleaned up")
	}
	if s.Call(context.Background(), "test-session", map[string]string{"action": "observe"}).Category != "stopped" {
		t.Fatal("stop did not latch")
	}
	if !s.Call(context.Background(), "test-session", map[string]string{"action": "resume"}).OK {
		t.Fatal("resume failed")
	}
	a = clickArgs(&Observation{ID: "old"})
	a["action_id"] = "after-resume-1"
	if s.Call(context.Background(), "test-session", a).Category != "stale_observation" {
		t.Fatal("resume reused screenshot")
	}
}
func TestCrossProcessLockAndAuditPrivacy(t *testing.T) {
	s, b := fixture(t)
	o := observe(t, s)
	other := New(b, t.TempDir())
	other.lockPath = s.lockPath
	t.Cleanup(other.unlockDesktop)
	if got := other.Call(context.Background(), "other", map[string]string{"action": "observe"}); got.Category != "desktop_owned" {
		t.Fatalf("OS lock failed: %#v", got)
	}
	r := s.Call(context.Background(), "test-session", map[string]string{"action": "type", "text": "private typed content", "action_id": "type-test-1", "observation_id": o.ID})
	if !r.OK {
		t.Fatalf("type failed: %#v", r)
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, "actions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private typed content") {
		t.Fatal("typed text leaked into audit")
	}
	if !s.Call(context.Background(), "test-session", map[string]string{"action": "release"}).OK {
		t.Fatal("release failed")
	}
	if !other.Call(context.Background(), "other", map[string]string{"action": "observe"}).OK {
		t.Fatal("lock not released")
	}
}
func TestKeyValidation(t *testing.T) {
	for _, key := range []string{"CTRL+A", "META+SHIFT+Z", "ENTER", "ALT+TAB"} {
		if err := validateKey(key); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"", "CTRL+CTRL+A", "A;shutdown", "SUPER+A", "CTRL+", "ENTER+X"} {
		if validateKey(key) == nil {
			t.Fatalf("accepted %q", key)
		}
	}
}

func TestUnavailableDesktopPreservesReason(t *testing.T) {
	s, b := fixture(t)
	b.state = State{Reason: "missing X11 dependencies"}
	r := s.Call(context.Background(), "test-session", map[string]string{"action": "observe"})
	if r.OK || r.Category != "unavailable" || !strings.Contains(r.Error, b.state.Reason) {
		t.Fatalf("missing diagnostic: %#v", r)
	}
}

func TestFailedRefreshInvalidatesPreviousFrame(t *testing.T) {
	s, b := fixture(t)
	old := observe(t, s)
	b.captureError = errors.New("capture unavailable")
	if s.Call(context.Background(), "test-session", map[string]string{"action": "observe"}).OK {
		t.Fatal("expected refresh failure")
	}
	b.captureError = nil
	if r := s.Call(context.Background(), "test-session", clickArgs(old)); r.Category != "stale_observation" || b.calls != 0 {
		t.Fatalf("old frame actionable: %#v", r)
	}
}
func TestForeignResumeCannotClearStop(t *testing.T) {
	s, _ := fixture(t)
	observe(t, s)
	s.Call(context.Background(), "operator", map[string]string{"action": "stop"})
	if r := s.Call(context.Background(), "foreign", map[string]string{"action": "resume"}); r.Category != "desktop_owned" {
		t.Fatalf("foreign resume: %#v", r)
	}
	if r := s.Call(context.Background(), "test-session", map[string]string{"action": "observe"}); r.Category != "stopped" {
		t.Fatalf("stop was cleared: %#v", r)
	}
}
func TestSlowStatusCannotExtendFrameValidity(t *testing.T) {
	s, b := fixture(t)
	old := observe(t, s)
	b.statusHook = func() { s.now = func() time.Time { return old.At.Add(61 * time.Second) } }
	if r := s.Call(context.Background(), "test-session", clickArgs(old)); r.Category != "stale_observation" || b.calls != 0 {
		t.Fatalf("expired frame used: %#v", r)
	}
}
func TestPostCaptureFailureDoesNotReplayInput(t *testing.T) {
	s, b := fixture(t)
	args := clickArgs(observe(t, s))
	b.inputHook = func() { b.captureError = errors.New("capture failed after input") }
	r := s.Call(context.Background(), "test-session", args)
	if r.OK || !r.InputDispatched || r.RetrySafe || r.Category != "post_observation_failed" {
		t.Fatalf("wrong receipt: %#v", r)
	}
	s.Call(context.Background(), "test-session", args)
	if b.calls != 1 {
		t.Fatal("replayed input after screenshot failure")
	}
}
func TestScreenshotReservedBeforeCapture(t *testing.T) {
	s, b := fixture(t)
	b.captureHook = func(path string) {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("artifact not reserved before capture: %v", err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("exposed screenshot permissions: %v", info.Mode())
		}
	}
	observe(t, s)
}

func namedElements() []Element {
	return []Element{
		{Index: 0, Role: "AXButton", Name: "Send", Press: true, Path: "0", X: -100, Y: 10, W: 50, H: 25},
		{Index: 1, Role: "AXButton", Name: "Cancel", Press: true, Path: "1", X: -50, Y: 10, W: 10, H: 10},
		{Index: 2, Role: "AXTextField", Name: "Input", Settable: true, Path: "2", X: -80, Y: 30, W: 20, H: 10},
	}
}

func TestAppIdentityIgnoresTitleAndRejectsAppSwitch(t *testing.T) {
	s, b := fixture(t)
	b.state.AppID = "com.tencent.xinWeChat"
	b.state.Foreground = "9:微信"
	o := observe(t, s)
	b.state.Foreground = "9:微信(2)"
	first := s.Call(context.Background(), "test-session", clickArgs(o))
	if !first.OK || first.Observation == nil {
		t.Fatalf("title change rejected: %#v", first)
	}
	b.state.AppID = "com.apple.finder"
	b.state.Foreground = "3:访达"
	next := clickArgs(first.Observation)
	next["action_id"] = "test-click-2"
	if got := s.Call(context.Background(), "test-session", next); got.Category != "desktop_changed" {
		t.Fatalf("app switch accepted: %#v", got)
	}
}

func TestSameAppWindowSwitchInvalidatesObservation(t *testing.T) {
	s, b := fixture(t)
	b.state.AppID = "com.tencent.xinWeChat"
	b.state.WindowID = "101"
	b.state.Foreground = "9:微信"
	o := observe(t, s)
	b.state.WindowID = "202"
	if got := s.Call(context.Background(), "test-session", clickArgs(o)); got.Category != "desktop_changed" {
		t.Fatalf("click landed on another window of the same app: %#v", got)
	}
}

func TestClickElementPressesMatchingNode(t *testing.T) {
	s, b := fixture(t)
	b.grounding = "element"
	b.elements = namedElements()
	o := observe(t, s)
	if o.Grounding != "element" || len(o.Elements) != 3 {
		t.Fatalf("grounding=%s n=%d", o.Grounding, len(o.Elements))
	}
	raw, err := json.Marshal(o)
	if err != nil || strings.Contains(string(raw), `"path":"0"`) {
		t.Fatalf("path leaked: %s %v", raw, err)
	}
	r := s.Call(context.Background(), "test-session", map[string]string{"action": "click_element", "element_index": "0", "observation_id": o.ID, "action_id": "elem-click-1"})
	if !r.OK || b.last.Action != "press_element" || b.last.ExpectName != "Send" || b.last.ElementPath != "0" || b.last.X != -75 || b.last.Y != 22 {
		t.Fatalf("result=%#v last=%#v", r, b.last)
	}
}

func TestElementNameMismatchDoesNotInject(t *testing.T) {
	s, b := fixture(t)
	b.grounding = "element"
	b.liveName = "Other"
	b.elements = namedElements()
	o := observe(t, s)
	r := s.Call(context.Background(), "test-session", map[string]string{"action": "click_element", "element_index": "0", "observation_id": o.ID, "action_id": "elem-click-2"})
	if r.OK || b.calls != 0 || !b.released || !strings.Contains(r.Error, "element changed") {
		t.Fatalf("mismatch injected: %#v calls=%d", r, b.calls)
	}
}

func TestSparseTreeUsesVisionCoordinates(t *testing.T) {
	s, b := fixture(t)
	b.grounding = "element"
	b.elements = []Element{{Index: 0, Role: "AXGroup", Name: "", Path: "0", X: -100, Y: 10, W: 10, H: 10}}
	o := observe(t, s)
	if o.Grounding != "vision" || len(o.Elements) != 0 {
		t.Fatalf("sparse tree exposed: %#v", o)
	}
	got := s.Call(context.Background(), "test-session", map[string]string{"action": "click_element", "element_index": "0", "observation_id": o.ID, "action_id": "elem-click-3"})
	if got.Category != "vision_only" || b.calls != 0 {
		t.Fatalf("vision fallback missing: %#v", got)
	}
}

func TestUnsupportedPlatformRejectsElementClick(t *testing.T) {
	s, b := fixture(t)
	b.grounding = "unsupported"
	o := observe(t, s)
	got := s.Call(context.Background(), "test-session", map[string]string{"action": "click_element", "element_index": "0", "observation_id": o.ID, "action_id": "elem-click-4"})
	if got.Category != "unsupported" || b.calls != 0 {
		t.Fatalf("platform fallback missing: %#v", got)
	}
}

func TestScrollUsesPointAndWaitDoesNotInject(t *testing.T) {
	s, b := fixture(t)
	o := observe(t, s)
	scrolled := s.Call(context.Background(), "test-session", map[string]string{"action": "scroll", "amount": "1", "x": "100", "y": "50", "observation_id": o.ID, "action_id": "scroll-0001"})
	if !scrolled.OK || !b.last.HasPoint || b.last.X != -50 || b.last.Y != 35 {
		t.Fatalf("scroll point: %#v last=%#v", scrolled, b.last)
	}
	before := b.calls
	waited := s.Call(context.Background(), "test-session", map[string]string{"action": "wait", "wait_ms": "200"})
	if !waited.OK || waited.Observation == nil || b.calls != before {
		t.Fatalf("wait injected input: %#v calls=%d", waited, b.calls)
	}
}
