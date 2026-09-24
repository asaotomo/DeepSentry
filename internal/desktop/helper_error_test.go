package desktop

import "testing"

func TestSimplifyHelperErrorReadsPowerShellCLIXML(t *testing.T) {
	raw := `#< CLIXML
<Objs Version="1.1.0.1"><Obj S="progress" RefId="0"></Obj><S S="Error">SetCursorPos failed. The foreground window is higher integrity._x000D__x000A_</S><S S="Error">+ ... etCursorPos_x000D__x000A_</S><S S="Error">    + CategoryInfo          : OperationStopped: (:) [], RuntimeException_x000D__x000A_</S></Objs>`
	got := simplifyHelperError(raw)
	want := "SetCursorPos failed. The foreground window is higher integrity."
	if got != want {
		t.Fatalf("got %q", got)
	}
	if simplifyHelperError("plain failure") != "plain failure" {
		t.Fatal("non-CLIXML stderr should pass through")
	}
}
