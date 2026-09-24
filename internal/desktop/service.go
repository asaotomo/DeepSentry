// Package desktop controls the local interactive desktop. It never redirects
// input to an SSH target and never equates input delivery with task success.
package desktop

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type State struct {
	Driver     string `json:"driver"`
	Ready      bool   `json:"ready"`
	Reason     string `json:"reason,omitempty"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Foreground string `json:"foreground"`
	AppID      string `json:"app_id,omitempty"`
	WindowID   string `json:"window_id,omitempty"`
	Surface    string `json:"surface"`
}

// Element is one accessibility node from the focused window. Path is the
// helper's private child index and is never sent to the model.
type Element struct {
	Index    int    `json:"index"`
	Role     string `json:"role"`
	Name     string `json:"name,omitempty"`
	Press    bool   `json:"press,omitempty"`
	Settable bool   `json:"settable,omitempty"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	W        int    `json:"w"`
	H        int    `json:"h"`
	Path     string `json:"-"`
}

func (e *Element) UnmarshalJSON(data []byte) error {
	var raw struct {
		Index    int    `json:"index"`
		Role     string `json:"role"`
		Name     string `json:"name"`
		Press    bool   `json:"press"`
		Settable bool   `json:"settable"`
		X        int    `json:"x"`
		Y        int    `json:"y"`
		W        int    `json:"w"`
		H        int    `json:"h"`
		Path     string `json:"path"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*e = Element{Index: raw.Index, Role: raw.Role, Name: raw.Name, Press: raw.Press, Settable: raw.Settable, X: raw.X, Y: raw.Y, W: raw.W, H: raw.H, Path: raw.Path}
	return nil
}

type Request struct {
	Action       string `json:"action"`
	X            int    `json:"x,omitempty"`
	Y            int    `json:"y,omitempty"`
	ToX          int    `json:"to_x,omitempty"`
	ToY          int    `json:"to_y,omitempty"`
	Button       string `json:"button,omitempty"`
	Text         string `json:"text,omitempty"`
	Key          string `json:"key,omitempty"`
	Amount       int    `json:"amount,omitempty"`
	Path         string `json:"path,omitempty"`
	HasPoint     bool   `json:"has_point,omitempty"`
	ElementIndex int    `json:"element_index,omitempty"`
	ElementPath  string `json:"element_path,omitempty"`
	ExpectRole   string `json:"expect_role,omitempty"`
	ExpectName   string `json:"expect_name,omitempty"`
}
type Backend interface {
	Status(context.Context) (State, error)
	Capture(context.Context, string) error
	Elements(context.Context) (string, []Element, error)
	Input(context.Context, Request) error
	Release(context.Context) error
}
type Observation struct {
	ID              string    `json:"id"`
	Path            string    `json:"path"`
	SHA256          string    `json:"sha256"`
	Width           int       `json:"width"`
	Height          int       `json:"height"`
	CoordinateSpace string    `json:"coordinate_space"`
	Grounding       string    `json:"grounding,omitempty"`
	GroundingNote   string    `json:"grounding_note,omitempty"`
	Elements        []Element `json:"elements,omitempty"`
	At              time.Time `json:"at"`
	State           State     `json:"desktop"`
}
type Result struct {
	OK                bool         `json:"ok"`
	Category          string       `json:"category,omitempty"`
	Error             string       `json:"error,omitempty"`
	State             *State       `json:"desktop,omitempty"`
	Observation       *Observation `json:"observation,omitempty"`
	InputDispatched   bool         `json:"input_dispatched"`
	OutcomeVerified   bool         `json:"outcome_verified"`
	DeliveryUncertain bool         `json:"delivery_uncertain"`
	RetrySafe         bool         `json:"retry_safe"`
	Stopped           bool         `json:"stopped"`
}
type seenAction struct {
	digest [32]byte
	result Result
	at     time.Time
}

// Observations are single-use and expire after 60 seconds, so a receipt older
// than this can no longer authorize a replay and is safe to forget.
const seenRetention = 5 * time.Minute

func clipNote(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return s
}

func (s *Service) pruneSeen() {
	cutoff := s.now().Add(-seenRetention)
	for key, entry := range s.seen {
		if entry.at.Before(cutoff) {
			delete(s.seen, key)
		}
	}
}

type Service struct {
	backend     Backend
	dir         string
	gate        chan struct{}
	mu          sync.Mutex
	cancel      context.CancelFunc
	stopped     bool
	leaseUntil  time.Time
	leaseTimer  *time.Timer
	lockPath    string
	lockFile    *os.File
	owner       string
	observation *Observation
	seen        map[string]seenAction
	now         func() time.Time
}

func New(backend Backend, dir string) *Service {
	return &Service{backend: backend, dir: dir, gate: make(chan struct{}, 1), seen: map[string]seenAction{}, now: time.Now, lockPath: filepath.Join(dir, "desktop.lock")}
}

var Default = defaultService()

func defaultService() *Service {
	dir := filepath.Join("reports", "computer-use")
	if abs, err := filepath.Abs(dir); err == nil {
		// Later chdir calls must not split screenshots and quota across folders.
		dir = abs
	}
	s := New(newNativeBackend(), dir)
	if cache, err := os.UserCacheDir(); err == nil {
		s.lockPath = filepath.Join(cache, "deepsentry", "desktop.lock")
	}
	return s
}

// SelfCheck proves a real capture works, not just that permissions report
// ready. The test frame is deleted and the desktop released afterwards.
func (s *Service) SelfCheck(ctx context.Context) Result {
	const session = "diagnostic"
	status := s.Call(ctx, session, map[string]string{"action": "status"})
	if !status.OK || status.State == nil || !status.State.Ready {
		return status
	}
	shot := s.Call(ctx, session, map[string]string{"action": "observe"})
	if shot.Observation != nil {
		_ = os.Remove(shot.Observation.Path)
	}
	_ = s.Call(ctx, session, map[string]string{"action": "release"})
	if !shot.OK {
		shot.State = status.State
		shot.Category = "capture_failed"
		return shot
	}
	return status
}

func ReadOnly(action string) bool {
	return action == "status" || action == "observe" || action == "stop" || action == "release" || action == "wait"
}

type callError struct {
	category string
	msg      string
}

func (e *callError) Error() string { return e.msg }

func sameDesktop(a, b State) bool {
	if a.Ready != b.Ready || a.Driver != b.Driver || a.Surface != b.Surface || a.X != b.X || a.Y != b.Y || a.Width != b.Width || a.Height != b.Height {
		return false
	}
	if a.AppID != "" && b.AppID != "" {
		if a.AppID != b.AppID {
			return false
		}
		// Titles change with unread counts; the window number does not.
		// Without it on either side, fall back to app identity only.
		if a.WindowID != "" && b.WindowID != "" {
			return a.WindowID == b.WindowID
		}
		return true
	}
	if a.Driver == "windows-sendinput" && b.Driver == "windows-sendinput" {
		// The helper reports HWND:title. Document titles can change without a
		// foreground switch; compare the stable HWND and keep rejecting new windows.
		ah, aok := windowsForegroundHandle(a.Foreground)
		bh, bok := windowsForegroundHandle(b.Foreground)
		if aok && bok {
			return ah == bh
		}
	}
	return a.Foreground == b.Foreground && a.Reason == b.Reason && a.AppID == b.AppID
}

func windowsForegroundHandle(foreground string) (string, bool) {
	handle, _, ok := strings.Cut(foreground, ":")
	if !ok || handle == "" {
		return "", false
	}
	if n, err := strconv.ParseInt(handle, 10, 64); err != nil || n == 0 {
		return "", false
	}
	return handle, true
}

func (s *Service) waitAndObserve(ctx context.Context, session string, args map[string]string) Result {
	ms, err := strconv.Atoi(strings.TrimSpace(args["wait_ms"]))
	if err != nil || ms < 200 || ms > 3000 {
		return failure("invalid_arguments", fmt.Errorf("wait_ms must be 200..3000"))
	}
	if err = s.lockDesktop(); err != nil {
		return failure("desktop_owned", err)
	}
	timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		if s.owner == "" {
			s.unlockDesktop()
		}
		return failure("cancelled", ctx.Err())
	case <-timer.C:
	}
	res := s.observe(ctx)
	if !res.OK && s.owner == "" {
		s.unlockDesktop()
	}
	if res.OK {
		s.owner = session
		s.renewLease()
	}
	return res
}

func (s *Service) attachElements(ctx context.Context, obs *Observation) {
	obs.Grounding = "vision"
	grounding, elements, err := s.backend.Elements(ctx)
	if err != nil {
		obs.GroundingNote = "accessibility tree unavailable this frame (" + clipNote(err.Error()) + "); element clicks are off, use x,y from this screenshot"
		return
	}
	if grounding == "" || grounding == "vision" {
		return
	}
	if grounding == "unsupported" {
		obs.Grounding = "unsupported"
		return
	}
	scaled := scaleElements(elements, obs.State, obs.Width, obs.Height)
	if classifyGrounding(scaled) != "element" {
		return
	}
	obs.Grounding = "element"
	obs.Elements = scaled
}

func scaleElements(elements []Element, state State, shotW, shotH int) []Element {
	if state.Width <= 0 || state.Height <= 0 || shotW <= 0 || shotH <= 0 {
		return nil
	}
	out := make([]Element, 0, len(elements))
	for _, el := range elements {
		el.X = scaleDelta(el.X-state.X, shotW, state.Width)
		el.Y = scaleDelta(el.Y-state.Y, shotH, state.Height)
		el.W = scaleDelta(el.W, shotW, state.Width)
		el.H = scaleDelta(el.H, shotH, state.Height)
		if el.W <= 0 || el.H <= 0 || el.X >= shotW || el.Y >= shotH || el.X+el.W <= 0 || el.Y+el.H <= 0 {
			continue
		}
		if el.X < 0 {
			el.W += el.X
			el.X = 0
		}
		if el.Y < 0 {
			el.H += el.Y
			el.Y = 0
		}
		if el.X+el.W > shotW {
			el.W = shotW - el.X
		}
		if el.Y+el.H > shotH {
			el.H = shotH - el.Y
		}
		if el.W <= 0 || el.H <= 0 {
			continue
		}
		out = append(out, el)
	}
	return out
}

func scaleDelta(delta, shot, desktop int) int {
	return int(int64(delta) * int64(shot) / int64(desktop))
}

func classifyGrounding(elements []Element) string {
	named := 0
	for _, el := range elements {
		if strings.TrimSpace(el.Name) != "" {
			named++
		}
	}
	if named < 3 {
		return "vision"
	}
	return "element"
}

func bindElement(req *Request, obs *Observation) *callError {
	if req.Action != "click_element" && req.Action != "set_value" {
		return nil
	}
	if obs.Grounding == "unsupported" {
		return &callError{"unsupported", "click_element is not supported on this platform; use screenshot x,y"}
	}
	if obs.Grounding != "element" {
		return &callError{"vision_only", "无障碍树不可用或过于稀疏，请按截图像素使用 x,y"}
	}
	var el Element
	found := false
	for _, candidate := range obs.Elements {
		if candidate.Index == req.ElementIndex {
			el = candidate
			found = true
			break
		}
	}
	if !found || el.Path == "" {
		return &callError{"stale_element", "element_index 不属于当前截图，请重新 observe"}
	}
	if req.Action == "set_value" && !el.Settable {
		return &callError{"not_settable", "该元素不能设值，请改用 click_element 或截图像素"}
	}
	req.ElementPath = el.Path
	req.ExpectRole = el.Role
	req.ExpectName = el.Name
	if req.Action == "click_element" {
		req.X = el.X
		req.Y = el.Y
		if el.W > 1 {
			req.X += el.W / 2
		}
		if el.H > 1 {
			req.Y += el.H / 2
		}
		if req.X >= obs.Width {
			req.X = obs.Width - 1
		}
		if req.Y >= obs.Height {
			req.Y = obs.Height - 1
		}
		if req.X < 0 {
			req.X = 0
		}
		if req.Y < 0 {
			req.Y = 0
		}
		if el.Press {
			req.Action = "press_element"
		}
	}
	return nil
}

func failure(category string, err error) Result {
	return Result{Category: category, Error: err.Error(), RetrySafe: true}
}
func (s *Service) Call(parent context.Context, session string, args map[string]string) Result {
	action := strings.ToLower(strings.TrimSpace(args["action"]))
	if session == "" {
		return failure("missing_session", fmt.Errorf("desktop control requires an identified local session"))
	}
	if action == "stop" {
		s.mu.Lock()
		s.stopped = true
		if s.cancel != nil {
			s.cancel()
		}
		s.mu.Unlock()
		return Result{OK: true, Stopped: true, RetrySafe: true}
	}
	if os.Getenv("DEEPSENTRY_COMPUTER_USE_DISABLED") == "1" {
		return failure("disabled", fmt.Errorf("computer_use disabled by operator environment"))
	}
	// Never wait behind another desktop call; the caller must observe again.
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	default:
		return failure("busy", fmt.Errorf("another desktop operation is in progress"))
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	s.mu.Lock()
	s.cancel = cancel
	stopped := s.stopped
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.cancel = nil; s.mu.Unlock() }()
	if err := ctx.Err(); err != nil {
		return failure("cancelled", err)
	}
	if action == "status" {
		state, err := s.backend.Status(ctx)
		if err != nil {
			return failure("unavailable", err)
		}
		return Result{OK: true, State: &state, Stopped: stopped, RetrySafe: true}
	}
	if s.owner != "" && s.now().After(s.leaseUntil) {
		s.owner = ""
		s.observation = nil
		s.unlockDesktop()
	}
	if action == "resume" {
		if s.owner != "" && s.owner != session {
			return failure("desktop_owned", fmt.Errorf("only the owning session can resume desktop control"))
		}
		s.mu.Lock()
		s.stopped = false
		s.mu.Unlock()
		s.observation = nil
		return Result{OK: true, RetrySafe: true}
	}
	if action == "release" {
		if s.owner == "" {
			return Result{OK: true, RetrySafe: true}
		}
		if s.owner != "" && s.owner != session {
			return failure("desktop_owned", fmt.Errorf("desktop belongs to another session"))
		}
		if err := s.backend.Release(ctx); err != nil {
			return failure("release_failed", err)
		}
		s.owner = ""
		s.observation = nil
		s.unlockDesktop()
		return Result{OK: true, RetrySafe: true}
	}
	if stopped {
		return failure("stopped", fmt.Errorf("desktop input stopped; explicit resume then a fresh observe is required"))
	}
	if s.owner != "" && s.owner != session {
		return failure("desktop_owned", fmt.Errorf("desktop belongs to another session; release it there first"))
	}
	if action == "wait" {
		return s.waitAndObserve(ctx, session, args)
	}
	if action == "observe" {
		if err := s.lockDesktop(); err != nil {
			return failure("desktop_owned", err)
		}
		res := s.observe(ctx)
		if !res.OK && s.owner == "" {
			s.unlockDesktop()
		}
		if res.OK {
			s.owner = session
			s.renewLease()
		}
		return res
	}
	req, err := parseRequest(args)
	if err != nil {
		return failure("invalid_arguments", err)
	}
	id := args["action_id"]
	if len(id) < 8 || len(id) > 128 {
		return failure("invalid_action_id", fmt.Errorf("action_id must be 8..128 characters; reuse the same id on an uncertain retry"))
	}
	key := session + "\x00" + id
	raw, _ := json.Marshal(args)
	digest := sha256.Sum256(raw)
	if previous, ok := s.seen[key]; ok {
		if previous.digest != digest {
			return failure("id_conflict", fmt.Errorf("action_id already used with different arguments"))
		}
		return previous.result
	}
	if len(s.seen) >= 4096 {
		s.pruneSeen()
	}
	// Recent receipts still guard against duplicate execution; never drop them.
	if len(s.seen) >= 4096 {
		return failure("action_limit", fmt.Errorf("more than 4096 desktop inputs in %s; wait a few minutes, then observe again", seenRetention))
	}
	obs := s.observation
	if obs == nil || obs.ID != args["observation_id"] || s.now().Sub(obs.At) > 60*time.Second {
		return failure("stale_observation", fmt.Errorf("observe again; input requires the current screenshot from this session, less than 60 seconds old"))
	}
	state, err := s.backend.Status(ctx)
	if err != nil {
		return failure("unavailable", err)
	}
	if !state.Ready {
		return failure("unavailable", fmt.Errorf("%s", state.Reason))
	}
	if s.now().Sub(obs.At) > 60*time.Second {
		s.observation = nil
		return failure("stale_observation", fmt.Errorf("observation expired during desktop status check; observe again"))
	}
	if !sameDesktop(state, obs.State) {
		s.observation = nil
		return failure("desktop_changed", fmt.Errorf("foreground application, permissions or display geometry changed; observe again"))
	}
	if bindErr := bindElement(&req, obs); bindErr != nil {
		return failure(bindErr.category, bindErr)
	}
	if err = mapCoordinates(&req, *obs); err != nil {
		return failure("out_of_bounds", err)
	}
	// Reserve the id before touching the desktop, and consume the observation.
	uncertain := Result{Category: "input_uncertain", Error: "input may have been delivered; observe before any new action", DeliveryUncertain: true, RetrySafe: false}
	s.seen[key] = seenAction{digest, uncertain, s.now()}
	s.observation = nil
	if err = s.audit(session, id, req.Action, "attempt"); err != nil {
		delete(s.seen, key)
		return failure("audit_failed", err)
	}
	s.mu.Lock()
	stopped = s.stopped
	s.mu.Unlock()
	if err = ctx.Err(); err != nil || stopped {
		if err == nil {
			err = fmt.Errorf("stopped")
		}
		res := failure("cancelled_before_input", fmt.Errorf("%v; no input was sent, but the screenshot is used up: observe again and use a new action_id", err))
		s.seen[key] = seenAction{digest, res, s.now()}
		return res
	}
	err = s.backend.Input(ctx, req)
	var res Result
	if err != nil {
		cleanup, c := context.WithTimeout(context.Background(), 3*time.Second)
		_ = s.backend.Release(cleanup)
		c()
		res = uncertain
		res.Error = err.Error() + "; delivery uncertain, do not replay; observe first"
	} else {
		// Input is delivered, but only a model/user can validate the semantic goal.
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
		res = s.observe(ctx)
		res.InputDispatched = true
		res.RetrySafe = false
		if !res.OK {
			res.Category = "post_observation_failed"
			res.Error += "; input already sent, do not replay"
		}
	}
	s.renewLease()
	s.seen[key] = seenAction{digest, res, s.now()}
	if err := s.audit(session, id, req.Action, res.Category); err != nil {
		res.Error += "; audit completion failed: " + err.Error()
		s.seen[key] = seenAction{digest, res, s.now()}
	}
	return res
}
func (s *Service) observe(ctx context.Context) Result {
	// A failed refresh must never leave the previous frame actionable.
	s.observation = nil
	before, err := s.backend.Status(ctx)
	if err != nil {
		return failure("unavailable", err)
	}
	if !before.Ready {
		return failure("unavailable", fmt.Errorf("%s", before.Reason))
	}
	if before.Width <= 0 || before.Height <= 0 {
		return failure("unavailable", fmt.Errorf("invalid desktop geometry"))
	}
	if entries, e := os.ReadDir(s.dir); e == nil {
		var used int64
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "screen-") && strings.HasSuffix(entry.Name(), ".png") {
				if info, e := entry.Info(); e == nil {
					used += info.Size()
				}
			}
		}
		if used > 512*1024*1024 {
			return failure("artifact_quota", fmt.Errorf("desktop screenshots exceed 512 MiB; archive/clean reports/computer-use before continuing"))
		}
	}
	if err = os.MkdirAll(s.dir, 0700); err != nil {
		return failure("artifact_failed", err)
	}
	idBytes := make([]byte, 16)
	if _, err = rand.Read(idBytes); err != nil {
		return failure("artifact_failed", err)
	}
	id := hex.EncodeToString(idBytes)
	path, err := filepath.Abs(filepath.Join(s.dir, "screen-"+id+".png"))
	if err != nil {
		return failure("artifact_failed", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	// Restrict access before the backend writes any screen contents.
	reserved, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return failure("artifact_failed", err)
	}
	if err = reserved.Close(); err != nil {
		return failure("artifact_failed", err)
	}
	if err = s.backend.Capture(ctx, path); err != nil {
		return failure("capture_failed", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return failure("capture_failed", err)
	}
	cfg, format, err := image.DecodeConfig(file)
	file.Close()
	if err != nil || format != "png" || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 64000000 {
		return failure("invalid_capture", fmt.Errorf("capture must be a PNG under 64 megapixels"))
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() > 20*1024*1024 {
		return failure("invalid_capture", fmt.Errorf("capture exceeds 20 MiB limit"))
	}
	after, err := s.backend.Status(ctx)
	if err != nil {
		return failure("unavailable", err)
	}
	if !sameDesktop(before, after) {
		return failure("desktop_changed", fmt.Errorf("desktop changed during capture; observe again"))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return failure("artifact_failed", err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		return failure("artifact_failed", err)
	}
	hash := sha256.Sum256(raw)
	obs := &Observation{ID: id, Path: path, SHA256: hex.EncodeToString(hash[:]), Width: cfg.Width, Height: cfg.Height, CoordinateSpace: "screenshot_pixels", At: s.now(), State: after}
	s.attachElements(ctx, obs)
	s.observation = obs
	keep = true
	return Result{OK: true, Observation: obs, RetrySafe: true}
}
func parseRequest(args map[string]string) (Request, error) {
	r := Request{Action: args["action"], Button: args["button"], Text: args["text"], Key: args["key"]}
	if r.Button == "" {
		r.Button = "left"
	}
	if r.Button != "left" && r.Button != "right" && r.Button != "middle" {
		return r, fmt.Errorf("button must be left/right/middle")
	}
	num := func(key string) (int, error) {
		n, err := strconv.Atoi(args[key])
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return n, nil
	}
	switch r.Action {
	case "click", "double_click", "move", "drag":
		var err error
		r.X, err = num("x")
		if err != nil {
			return r, err
		}
		r.Y, err = num("y")
		if err != nil {
			return r, err
		}
		if r.Action == "drag" {
			r.ToX, err = num("to_x")
			if err != nil {
				return r, err
			}
			r.ToY, err = num("to_y")
			if err != nil {
				return r, err
			}
		}
	case "scroll":
		var err error
		r.Amount, err = num("amount")
		if err != nil {
			return r, err
		}
		if r.Amount == 0 || r.Amount < -10 || r.Amount > 10 {
			return r, fmt.Errorf("amount must be -10..-1 (up) or 1..10 (down)")
		}
		_, hasX := args["x"]
		_, hasY := args["y"]
		if hasX || hasY {
			if !hasX || !hasY || strings.TrimSpace(args["x"]) == "" || strings.TrimSpace(args["y"]) == "" {
				return r, fmt.Errorf("scroll x and y must be provided together")
			}
			r.X, err = num("x")
			if err != nil {
				return r, err
			}
			r.Y, err = num("y")
			if err != nil {
				return r, err
			}
			r.HasPoint = true
		}
	case "click_element":
		index, err := num("element_index")
		if err != nil || index < 0 {
			return r, fmt.Errorf("element_index must be a non-negative integer from the current screenshot")
		}
		r.ElementIndex = index
	case "set_value":
		index, err := num("element_index")
		if err != nil || index < 0 {
			return r, fmt.Errorf("element_index must be a non-negative integer from the current screenshot")
		}
		r.ElementIndex = index
		r.Text = args["text"]
		if r.Text == "" || len([]rune(r.Text)) > 2000 || strings.ContainsRune(r.Text, 0) {
			return r, fmt.Errorf("text must contain 1..2000 Unicode characters without NUL")
		}
	case "type":
		if r.Text == "" || len([]rune(r.Text)) > 2000 || strings.ContainsRune(r.Text, 0) {
			return r, fmt.Errorf("text must contain 1..2000 Unicode characters without NUL")
		}
	case "key":
		if err := validateKey(r.Key); err != nil {
			return r, err
		}
	default:
		return r, fmt.Errorf("unsupported desktop action %q", r.Action)
	}
	return r, nil
}
func validateKey(key string) error {
	parts := strings.Split(strings.ToUpper(key), "+")
	if len(parts) > 5 {
		return fmt.Errorf("too many key modifiers")
	}
	seen := map[string]bool{}
	for _, p := range parts[:len(parts)-1] {
		if (p != "CTRL" && p != "ALT" && p != "SHIFT" && p != "META") || seen[p] {
			return fmt.Errorf("invalid or repeated modifier %q", p)
		}
		seen[p] = true
	}
	k := parts[len(parts)-1]
	if len(k) == 1 && ((k[0] >= 'A' && k[0] <= 'Z') || (k[0] >= '0' && k[0] <= '9')) {
		return nil
	}
	switch k {
	case "ENTER", "TAB", "ESCAPE", "SPACE", "BACKSPACE", "DELETE", "UP", "DOWN", "LEFT", "RIGHT", "HOME", "END", "PAGEUP", "PAGEDOWN":
		return nil
	}
	return fmt.Errorf("unsupported key; use CTRL/ALT/SHIFT/META + A-Z, 0-9 or ENTER/TAB/ESCAPE/SPACE/BACKSPACE/DELETE/UP/DOWN/LEFT/RIGHT/HOME/END/PAGEUP/PAGEDOWN")
}
func mapCoordinates(r *Request, o Observation) error {
	point := r.Action == "click" || r.Action == "double_click" || r.Action == "move" || r.Action == "drag" || r.Action == "click_element" || r.Action == "press_element" || (r.Action == "scroll" && r.HasPoint)
	if !point {
		return nil
	}
	convert := func(x, y int) (int, int, error) {
		if x < 0 || y < 0 || x >= o.Width || y >= o.Height {
			return 0, 0, fmt.Errorf("coordinates outside screenshot %dx%d", o.Width, o.Height)
		}
		// Map the center of the screenshot pixel, not its left edge, so a
		// downscaled capture does not bias clicks toward the top-left.
		return o.State.X + int((2*int64(x)+1)*int64(o.State.Width)/(2*int64(o.Width))), o.State.Y + int((2*int64(y)+1)*int64(o.State.Height)/(2*int64(o.Height))), nil
	}
	var err error
	r.X, r.Y, err = convert(r.X, r.Y)
	if err != nil {
		return err
	}
	if r.Action == "drag" {
		r.ToX, r.ToY, err = convert(r.ToX, r.ToY)
	}
	return err
}
func (s *Service) audit(session, id, action, category string) error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, "actions.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(map[string]any{"time": s.now().UTC(), "session": session, "action_id": id, "action": action, "category": category})
}

func (s *Service) lockDesktop() error {
	if s.lockFile != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.lockPath), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = tryDesktopLock(f); err != nil {
		f.Close()
		return fmt.Errorf("another process owns the desktop: %w", err)
	}
	s.lockFile = f
	return nil
}
func (s *Service) unlockDesktop() {
	if s.leaseTimer != nil {
		s.leaseTimer.Stop()
	}
	if s.lockFile != nil {
		releaseDesktopLock(s.lockFile)
		s.lockFile.Close()
		s.lockFile = nil
	}
}

func (s *Service) renewLease() {
	s.leaseUntil = s.now().Add(5 * time.Minute)
	if s.leaseTimer != nil {
		s.leaseTimer.Stop()
	}
	s.leaseTimer = time.AfterFunc(5*time.Minute, s.expireLease)
}
func (s *Service) expireLease() {
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	default:
		time.AfterFunc(time.Second, s.expireLease)
		return
	}
	remaining := s.leaseUntil.Sub(s.now())
	if s.owner != "" && remaining <= 0 {
		s.owner = ""
		s.observation = nil
		s.unlockDesktop()
	} else if s.owner != "" {
		s.leaseTimer = time.AfterFunc(remaining, s.expireLease)
	}
}
