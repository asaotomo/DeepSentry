package tui

import (
	"bytes"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	charmansi "github.com/charmbracelet/x/ansi"
)

const (
	cursorAnchorHidden int32 = iota
	cursorAnchorVisible
	cursorAnchorPassthrough
)

// These private CSI sequences are renderer metadata, not terminal commands.
// inputCursorOutput removes them before stdout sees the frame. Keeping the
// cursor position inside the rendered frame prevents a newer model update
// from moving the physical IME cursor over an older frame that the renderer
// was already flushing.
const (
	cursorFrameMarkerPrefix = "\x1b[?2026;"
	footerFrameMarkerPrefix = "\x1b[?2027;"
	frameMarkerSuffix       = byte('z')
)

type renderedCursorAnchor struct {
	row, col int
	mode     int32
}

func encodeCursorFrameMarker(row, col int, mode int32) string {
	return cursorFrameMarkerPrefix + strconv.FormatInt(int64(mode), 10) + ";" +
		strconv.Itoa(row) + ";" + strconv.Itoa(col) + string(frameMarkerSuffix)
}

func encodeFooterFrameMarker(version uint64) string {
	return footerFrameMarkerPrefix + strconv.FormatUint(version, 10) + string(frameMarkerSuffix)
}

// inputCursorAnchorState is shared by Bubble Tea model copies and the
// renderer output wrapper. This keeps macOS IME candidate placement aligned
// without racing Bubble Tea with independent stdout writes.
type inputCursorAnchorState struct {
	position atomic.Pointer[renderedCursorAnchor]
	mode     atomic.Int32
}

func newInputCursorAnchorState() *inputCursorAnchorState {
	s := &inputCursorAnchorState{}
	s.mode.Store(cursorAnchorHidden)
	return s
}

func (s *inputCursorAnchorState) show(row, col int) {
	if s == nil {
		return
	}
	if row < 1 {
		row = 1
	}
	if col < 1 {
		col = 1
	}
	// Store both coordinates as one immutable value. Besides keeping the pair
	// atomic, this avoids narrowing through uint32 on 32-bit release targets.
	s.position.Store(&renderedCursorAnchor{row: row, col: col})
	s.mode.Store(cursorAnchorVisible)
}

func (s *inputCursorAnchorState) hide() {
	if s != nil {
		s.mode.Store(cursorAnchorHidden)
	}
}

func (s *inputCursorAnchorState) release() {
	if s != nil {
		s.mode.Store(cursorAnchorPassthrough)
	}
}

func (s *inputCursorAnchorState) snapshot() (row, col int, mode int32) {
	if s == nil {
		return 0, 0, cursorAnchorPassthrough
	}
	mode = s.mode.Load()
	position := s.position.Load()
	if position == nil {
		return 0, 0, mode
	}
	return position.row, position.col, mode
}

// inputCursorOutput removes private frame metadata and appends the physical
// IME cursor anchor belonging to the exact Bubble Tea diff being written.
// Writes that contain only renderer control sequences reuse the last anchor
// that was actually displayed.
type inputCursorOutput struct {
	dst     *os.File
	anchor  *inputCursorAnchorState
	mu      sync.Mutex
	last    renderedCursorAnchor
	hasLast bool
}

func newInputCursorOutput(dst *os.File, anchor *inputCursorAnchorState) *inputCursorOutput {
	return &inputCursorOutput{dst: dst, anchor: anchor}
}

func (w *inputCursorOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	frame, clean := stripFrameMarkers(p)
	clean = conhostFrame(clean)
	n, err := w.dst.Write(clean)
	if err != nil {
		return 0, err
	}
	if n != len(clean) {
		return 0, io.ErrShortWrite
	}

	if frame != nil {
		w.last = *frame
		w.hasLast = true
	}
	anchor := w.last
	if frame == nil {
		// Direct renderer control writes do not carry frame metadata. Reuse the
		// cursor belonging to the frame that is actually on screen. Hidden and
		// passthrough are explicit lifecycle transitions and take precedence.
		row, col, mode := w.anchor.snapshot()
		if mode != cursorAnchorVisible || !w.hasLast {
			anchor = renderedCursorAnchor{row: row, col: col, mode: mode}
		}
	}
	var suffix string
	switch anchor.mode {
	case cursorAnchorVisible:
		suffix = charmansi.CursorPosition(anchor.col, anchor.row) + charmansi.ShowCursor
	case cursorAnchorHidden:
		suffix = charmansi.HideCursor
	}
	if suffix != "" {
		if _, err := io.WriteString(w.dst, suffix); err != nil {
			return 0, err
		}
	}
	// io.Writer reports consumption of the caller's metadata-bearing input,
	// even though the private markers themselves were intentionally removed.
	return len(p), nil
}

// stripFrameMarkers removes all DeepSentry renderer metadata and returns the
// last cursor marker in this write. Bubble Tea flushes a complete diff in one
// Write call, so this binds the IME anchor to that exact diff.
func stripFrameMarkers(p []byte) (*renderedCursorAnchor, []byte) {
	cursorPrefix := []byte(cursorFrameMarkerPrefix)
	footerPrefix := []byte(footerFrameMarkerPrefix)
	remaining := p
	clean := make([]byte, 0, len(p))
	var frame *renderedCursorAnchor

	for len(remaining) > 0 {
		cursorAt := bytes.Index(remaining, cursorPrefix)
		footerAt := bytes.Index(remaining, footerPrefix)
		at, isCursor := nextFrameMarker(cursorAt, footerAt)
		if at < 0 {
			clean = append(clean, remaining...)
			break
		}
		clean = append(clean, remaining[:at]...)
		prefixLen := len(footerPrefix)
		if isCursor {
			prefixLen = len(cursorPrefix)
		}
		payloadStart := at + prefixLen
		endOffset := bytes.IndexByte(remaining[payloadStart:], frameMarkerSuffix)
		if endOffset < 0 {
			// An incomplete/foreign sequence must pass through untouched.
			clean = append(clean, remaining[at:]...)
			break
		}
		payloadEnd := payloadStart + endOffset
		if isCursor {
			if parsed, ok := parseCursorFrameMarker(string(remaining[payloadStart:payloadEnd])); ok {
				copy := parsed
				frame = &copy
			}
		}
		remaining = remaining[payloadEnd+1:]
	}
	return frame, clean
}

func nextFrameMarker(cursorAt, footerAt int) (at int, cursor bool) {
	switch {
	case cursorAt < 0:
		return footerAt, false
	case footerAt < 0:
		return cursorAt, true
	case cursorAt <= footerAt:
		return cursorAt, true
	default:
		return footerAt, false
	}
}

func parseCursorFrameMarker(payload string) (renderedCursorAnchor, bool) {
	parts := strings.Split(payload, ";")
	if len(parts) != 3 {
		return renderedCursorAnchor{}, false
	}
	mode, err := strconv.ParseInt(parts[0], 10, 32)
	if err != nil || mode < int64(cursorAnchorHidden) || mode > int64(cursorAnchorPassthrough) {
		return renderedCursorAnchor{}, false
	}
	row, err := strconv.Atoi(parts[1])
	if err != nil {
		return renderedCursorAnchor{}, false
	}
	col, err := strconv.Atoi(parts[2])
	if err != nil {
		return renderedCursorAnchor{}, false
	}
	return renderedCursorAnchor{row: row, col: col, mode: int32(mode)}, true
}

// Preserve Bubble Tea's terminal detection and resize handling under
// tea.WithOutput. Close is deliberately a no-op: stdout belongs to the host.
func (w *inputCursorOutput) Read(p []byte) (int, error) { return w.dst.Read(p) }
func (w *inputCursorOutput) Close() error               { return nil }
func (w *inputCursorOutput) Fd() uintptr                { return w.dst.Fd() }

func cancelInputCursorAnchor() {}

func (m AgentModel) hideInputCursorAnchor() {
	if m.cursorAnchor != nil {
		m.cursorAnchor.hide()
	}
}

func (m AgentModel) syncInputCursorAnchor() {
	if m.cursorAnchor == nil {
		return
	}
	if m.quitting {
		m.cursorAnchor.hide()
		return
	}
	row, col, ok := m.inputCursorAnchor()
	if !ok {
		m.cursorAnchor.hide()
		return
	}
	m.cursorAnchor.show(row, col)
}

func (m AgentModel) syncInputCursorAnchorForLayout(headerH, bodyH, suggestionsH, statusH int) {
	if m.cursorAnchor == nil {
		return
	}
	if m.quitting {
		m.cursorAnchor.hide()
		return
	}
	row, col, ok := m.inputCursorAnchorForLayout(headerH, bodyH, suggestionsH, statusH)
	if !ok {
		m.cursorAnchor.hide()
		return
	}
	m.cursorAnchor.show(row, col)
}

func (m AgentModel) scheduleInputCursorAnchor() { m.syncInputCursorAnchor() }
