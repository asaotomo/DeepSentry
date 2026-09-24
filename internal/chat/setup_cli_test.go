package chat

import (
	"ai-edr/internal/config"
	"bufio"
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIStatusExitWithoutBrowser(t *testing.T) {
	cfg := config.ChatConfig{Store: filepath.Join(t.TempDir(), "jobs.json"), Listen: "127.0.0.1:0"}
	var output bytes.Buffer
	err := SetupCLI(context.Background(), cfg, func(context.Context, string, string) (string, error) { return "", nil }, strings.NewReader("3\n0\n"), &output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "终端接入") || !strings.Contains(output.String(), "HTTP 通道") {
		t.Fatal(output.String())
	}
}
func TestCLICancelWhileWaitingForInput(t *testing.T) {
	cfg := config.ChatConfig{Store: filepath.Join(t.TempDir(), "jobs.json"), Listen: "127.0.0.1:0"}
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- SetupCLI(ctx, cfg, func(context.Context, string, string) (string, error) { return "", nil }, input, io.Discard)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal wizard did not stop on cancellation")
	}
}
func TestCLICredentialsNotReadFromEchoingInput(t *testing.T) {
	tr := &terminalSetup{in: bufio.NewReader(strings.NewReader("secret\n")), out: io.Discard}
	if _, err := tr.ask("password", true); err == nil {
		t.Fatal("secret accepted from redirected input")
	}
}
func TestManualBindingRoundTripAndDisconnect(t *testing.T) {
	cfg := config.ChatConfig{BindingFile: filepath.Join(t.TempDir(), "connections.json")}
	b := binding{Platform: "dingtalk", Secret: "private-secret", AllowBatch: true, Manual: &config.ChatChannel{Name: "manual-ding-quick", Platform: "dingtalk", AppID: "app", AllowedUsers: []string{"owner"}}}
	if err := changeBinding(cfg, "manual-ding", &b); err != nil {
		t.Fatal(err)
	}
	all, err := loadBindings(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := channelFor("manual-ding", all["manual-ding"])
	if c.Platform != "dingtalk" || c.AppSecret != b.Secret || !c.AllowBatch || c.AllowedUsers[0] != "owner" {
		t.Fatalf("manual binding did not round-trip")
	}
	if err := changeBinding(cfg, "manual-ding", nil); err != nil {
		t.Fatal(err)
	}
	all, _ = loadBindings(cfg)
	if len(all) != 0 {
		t.Fatal("binding retained")
	}
}
