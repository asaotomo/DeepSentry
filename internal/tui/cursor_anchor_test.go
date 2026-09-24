package tui

import (
	"io"
	"os"
	"strings"
	"testing"

	charmansi "github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

func TestInputCursorOutputAnchorsIMEInRendererWrite(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	anchor := newInputCursorAnchorState()
	anchor.show(22, 17)
	output := newInputCursorOutput(writer, anchor)
	frame := "rendered frame"
	if _, err := output.Write([]byte(frame)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := charmansi.CursorPosition(17, 22) + charmansi.ShowCursor
	if string(got) != frame+wantSuffix {
		t.Fatalf("renderer output did not end at the IME anchor: %q", string(got))
	}
}

func TestInputCursorOutputUsesAnchorFromExactRenderedFrame(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	anchor := newInputCursorAnchorState()
	// Simulate Update/View publishing a newer cursor while Bubble Tea is still
	// flushing the preceding frame. The output must follow the marker carried
	// by the old frame, not this newer shared value.
	anchor.show(5, 60)
	output := newInputCursorOutput(writer, anchor)
	frame := "rendered frame" + encodeFooterFrameMarker(9) + encodeCursorFrameMarker(22, 17, cursorAnchorVisible)
	if n, err := output.Write([]byte(frame)); err != nil || n != len(frame) {
		t.Fatalf("Write() = %d, %v; want %d, nil", n, err, len(frame))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	want := "rendered frame" + charmansi.CursorPosition(17, 22) + charmansi.ShowCursor
	if string(got) != want {
		t.Fatalf("frame cursor was not atomic: got %q want %q", string(got), want)
	}
}

func TestViewPublishesFocusedPhysicalCursorAnchor(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width, m.height = 80, 24
	m.input.SetValue("你好abc")
	m.input.SetCursor(2)
	m.recalcLayout()
	view := m.View()
	if strings.Contains(view, "\x1b[22;7H") {
		t.Fatal("View must not write cursor escapes outside the renderer output path")
	}
	row, col, mode := m.cursorAnchor.snapshot()
	if mode != cursorAnchorVisible || row != 22 || col != 7 {
		t.Fatalf("published cursor anchor = row %d col %d mode %d, want 22/7/visible", row, col, mode)
	}

	m.input.Blur()
	_ = m.View()
	_, _, mode = m.cursorAnchor.snapshot()
	if mode != cursorAnchorHidden {
		t.Fatalf("blurred input left physical cursor visible: mode=%d", mode)
	}
}

func TestPublishedCursorMatchesActualRenderedCell(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
		value  string
		cursor int
		marker string
	}{
		{name: "cjk", width: 80, height: 24, value: "你A界B", cursor: 2, marker: "界B"},
		{name: "slash suggestions", width: 80, height: 24, value: "/sk", cursor: 2, marker: "k"},
		{name: "wrapped", width: 30, height: 14, value: strings.Repeat("你好", 20) + "ZTAIL", cursor: 40, marker: "ZTAIL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
			m.width, m.height = tt.width, tt.height
			m.input.SetValue(tt.value)
			m.input.SetCursor(tt.cursor)
			m.recalcLayout()
			view := stripANSIForTest(m.View())
			wantRow, wantCol, ok := renderedMarkerPosition(view, tt.marker)
			if !ok {
				t.Fatalf("marker %q not found in rendered frame:\n%s", tt.marker, view)
			}
			row, col, mode := m.cursorAnchor.snapshot()
			if mode != cursorAnchorVisible || row != wantRow || col != wantCol {
				t.Fatalf("physical cursor=%d,%d mode=%d; rendered marker starts at %d,%d", row, col, mode, wantRow, wantCol)
			}
		})
	}
}

func renderedMarkerPosition(view, marker string) (row, col int, ok bool) {
	lines := strings.Split(view, "\n")
	// The same command text can appear in the suggestion menu. The input box
	// is lower, so search from the bottom of the actual terminal frame.
	for i := len(lines) - 1; i >= 0; i-- {
		if idx := strings.Index(lines[i], marker); idx >= 0 {
			return i + 1, runewidth.StringWidth(lines[i][:idx]) + 1, true
		}
	}
	return 0, 0, false
}
