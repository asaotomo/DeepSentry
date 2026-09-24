package harness

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/config"
	"ai-edr/internal/runtimev3"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

type discardCheckpointUISink struct{}

func (discardCheckpointUISink) Emit(UIEvent) {}

func TestCheckpointRejectsPathTraversalSessionID(t *testing.T) {
	for _, id := range []string{"../outside", "session_../../outside", "session_/tmp/evil", "not-a-session"} {
		if _, err := NewCheckpointStore(id); err == nil || !strings.Contains(err.Error(), "非法 session_id") {
			t.Fatalf("NewCheckpointStore(%q) err=%v", id, err)
		}
		if _, err := LoadCheckpoint(id); err == nil || !strings.Contains(err.Error(), "非法 session_id") {
			t.Fatalf("LoadCheckpoint(%q) err=%v", id, err)
		}
	}
}

func TestCheckpointRedactsConfiguredSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := config.GlobalConfig
	config.GlobalConfig.ApiKey = "api-secret-value-123"
	config.GlobalConfig.Targets = []config.TargetConfig{{Password: "ssh-secret-value-456"}}
	defer func() { config.GlobalConfig = old }()

	store, err := NewCheckpointStore("session_redaction")
	if err != nil {
		t.Fatal(err)
	}
	history := []analyzer.Message{{Role: "user", Content: "api_key: api-secret-value-123 password=ssh-secret-value-456"}}
	if err := store.Save(CheckpointData{State: NewAgentState(""), History: history}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.SessionDir() + "/checkpoint.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"api-secret-value-123", "ssh-secret-value-456"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("checkpoint leaked %q:\n%s", secret, raw)
		}
	}
	if !strings.Contains(string(raw), "***") {
		t.Fatalf("checkpoint should retain redaction marker: %s", raw)
	}
}

func TestCheckpointStructuralRedactionPreservesValidJSONAndLongText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := NewCheckpointStore("session_structural_redaction")
	if err != nil {
		t.Fatal(err)
	}
	// Redacting the serialized form of this value used to consume the escaped
	// quote after foo and leave syntactically invalid JSON.
	content := "<!doctype html>\n<head>HEAD_MARKER</head>\npassword=foo\"bar\\baz\n<body>MIDDLE_MARKER</body>\n</html> TAIL_MARKER"
	if err := store.Save(CheckpointData{
		State:   NewAgentState(""),
		History: []analyzer.Message{{Role: "user", Content: content}},
	}); err != nil {
		t.Fatalf("save structurally redacted checkpoint: %v", err)
	}
	raw, err := os.ReadFile(store.SessionDir() + "/checkpoint.json")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatalf("checkpoint must remain valid JSON:\n%s", raw)
	}
	if strings.Contains(string(raw), "password=foo") {
		t.Fatalf("checkpoint leaked credential: %s", raw)
	}
	loaded, err := LoadCheckpoint("session_structural_redaction")
	if err != nil {
		t.Fatalf("load structurally redacted checkpoint: %v", err)
	}
	if len(loaded.History) != 1 {
		t.Fatalf("history length=%d", len(loaded.History))
	}
	for _, marker := range []string{"HEAD_MARKER", "MIDDLE_MARKER", "TAIL_MARKER", "</html>"} {
		if !strings.Contains(loaded.History[0].Content, marker) {
			t.Fatalf("redaction lost %q from long text: %q", marker, loaded.History[0].Content)
		}
	}
}

func TestSessionSummariesSortNewestFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	olderID := "session_100"
	newerID := "session_200"
	for _, tc := range []struct {
		id    string
		stamp time.Time
	}{{olderID, time.Now().Add(-time.Hour)}, {newerID, time.Now()}} {
		store, err := NewCheckpointStore(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save(CheckpointData{State: NewAgentState(""), UserGoal: tc.id}); err != nil {
			t.Fatal(err)
		}
		path := store.SessionDir() + "/checkpoint.json"
		data, err := LoadCheckpoint(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		data.SavedAt = tc.stamp
		data.IntegritySHA256 = ""
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	summaries, err := ListSessionSummaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 || summaries[0].ID != newerID {
		t.Fatalf("summaries not newest first: %#v", summaries)
	}
}

func TestCheckpointUserGoalIgnoresToolFeedback(t *testing.T) {
	got := checkpointUserGoal([]analyzer.Message{
		{Role: "user", Content: "Output:\nsynthetic"},
		{Role: "user", Content: "需求：排查 SSH 暴力破解"},
		{Role: "user", Content: "后续追问"},
	})
	if got != "排查 SSH 暴力破解" {
		t.Fatalf("goal=%q", got)
	}
}

func TestCheckpointAcceptsGeneratedSessionID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := NewSessionID()
	store, err := NewCheckpointStore(id)
	if err != nil {
		t.Fatal(err)
	}
	if store.sessionID != id {
		t.Fatalf("session=%q want %q", store.sessionID, id)
	}
	for step := 1; step <= 2; step++ {
		if err := store.Save(CheckpointData{StepNum: step, State: NewAgentState("")}); err != nil {
			t.Fatalf("save step %d: %v", step, err)
		}
	}
	loaded, err := LoadCheckpoint(id)
	if err != nil || loaded.StepNum != 2 {
		t.Fatalf("replaced checkpoint step=%v err=%v", loaded, err)
	}
}

func TestCheckpointPersistsCoreClueBoard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "session_core_clues"
	store, err := NewCheckpointStore(id)
	if err != nil {
		t.Fatal(err)
	}
	state := NewAgentState("")
	state.ObserveCoreClues("关键结论：攻击源 198.51.100.7，文件 /var/www/html/x.php", "subagent/log")
	if err := store.Save(CheckpointData{State: state}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCheckpoint(id)
	if err != nil {
		t.Fatal(err)
	}
	prompt := loaded.State.CoreCluesPrompt(4000)
	if !strings.Contains(prompt, "198.51.100.7") || !strings.Contains(prompt, "/var/www/html/x.php") {
		t.Fatalf("checkpoint lost core clues:\n%s", prompt)
	}
}

func TestCheckpointIntegrityFallsBackToPreviousSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := NewCheckpointStore("session_integrity")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(CheckpointData{StepNum: 1, State: NewAgentState("")}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(CheckpointData{StepNum: 2, State: NewAgentState("")}); err != nil {
		t.Fatal(err)
	}
	path := store.SessionDir() + "/checkpoint.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 1
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCheckpoint("session_integrity")
	if err != nil || loaded.StepNum != 1 {
		t.Fatalf("expected previous snapshot, loaded=%#v err=%v", loaded, err)
	}
}

func TestCheckpointIntegrityRejectsUnknownFieldTampering(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := NewCheckpointStore("session_unknown_field")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(CheckpointData{StepNum: 1, State: NewAgentState("")}); err != nil {
		t.Fatal(err)
	}
	path := store.SessionDir() + "/checkpoint.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["unexpected_action_state"] = json.RawMessage(`{"completed":true}`)
	tampered, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpoint("session_unknown_field"); err == nil || !strings.Contains(err.Error(), "完整性") {
		t.Fatalf("unknown-field tampering passed integrity check: %v", err)
	}
}

func TestCheckpointV3IntegrityAndToolSafePointStillLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := NewCheckpointStore("session_v3_compat")
	if err != nil {
		t.Fatal(err)
	}
	state := NewAgentState("")
	state.BeginToolCall(ToolCallRecord{ID: "pending_v3", Name: "config_manage", Risk: "high"})
	if err := store.Save(CheckpointData{SchemaVersion: 3, State: state}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCheckpoint("session_v3_compat")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.State.ToolCallPending("pending_v3"); !ok {
		t.Fatal("v3 safe point was lost during compatibility load")
	}
}

func TestCheckpointPersistsToolCallSafePoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := NewCheckpointStore("session_tool_calls")
	if err != nil {
		t.Fatal(err)
	}
	state := NewAgentState("")
	state.BeginToolCall(ToolCallRecord{ID: "call_pending", Name: "file_upload", Risk: "high"})
	state.BeginToolCall(ToolCallRecord{ID: "call_done", Name: "config_manage", Risk: "high"})
	state.CompleteToolCall("call_done")
	if err := store.Save(CheckpointData{State: state}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCheckpoint("session_tool_calls")
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.State.ToolCallCompleted("call_done") {
		t.Fatal("completed modifying call was lost")
	}
	if pending, ok := loaded.State.ToolCallPending("call_pending"); !ok || pending.Risk != "high" {
		t.Fatalf("pending call was lost: %#v %v", pending, ok)
	}
}

func TestLegacyCheckpointMigratesAtTurnBoundary(t *testing.T) {
	state := NewAgentState("")
	state.Todos = []TodoItem{{Content: "保留待办"}}
	state.CoreClues = []CoreClue{{Kind: "fact", Value: "保留线索"}}
	state.BeginToolCall(ToolCallRecord{ID: "old_pending", Name: "file_upload", Risk: "high"})
	state.BeginToolCall(ToolCallRecord{ID: "old_done", Name: "config_manage", Risk: "high"})
	state.CompleteToolCall("old_done")
	data := &CheckpointData{
		SchemaVersion: 2,
		RunID:         "old_run",
		TurnID:        "old_turn",
		EventCursor:   99,
		StepNum:       6,
		State:         state,
		History:       []analyzer.Message{{Role: "user", Content: "保留历史"}},
	}
	initializeCheckpointState(data)
	if data.RuntimeVersion != "legacy" {
		t.Fatalf("old checkpoint runtime=%q want legacy", data.RuntimeVersion)
	}
	if data.RunID != "" || data.TurnID != "" || data.EventCursor != 0 || data.StepNum != 6 {
		t.Fatalf("old checkpoint did not migrate to its turn boundary: %#v", data)
	}
	if len(data.State.PendingToolCalls) != 0 || len(data.State.CompletedToolCalls) != 0 {
		t.Fatalf("old checkpoint fabricated tool safe points: pending=%#v completed=%#v", data.State.PendingToolCalls, data.State.CompletedToolCalls)
	}
	if len(data.State.Todos) != 1 || len(data.State.CoreClues) != 1 || len(data.History) != 1 {
		t.Fatalf("boundary migration lost durable state: %#v", data)
	}
}

func TestRuntimeV3FlushesSessionEventsBeforeCheckpointCursor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := NewCheckpointStore("session_event_order")
	if err != nil {
		t.Fatal(err)
	}
	eventPath := store.SessionDir() + "/events.jsonl"
	logSink, err := runtimev3.NewJSONLTraceSink(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	agent := &DeepAgent{
		State:      NewAgentState(""),
		SessionID:  "session_event_order",
		Checkpoint: store,
		RunID:      "run_test",
		Events:     &runtimev3.SequenceSink{Sink: runtimev3.MultiSink{logSink}},
	}
	agent.emitRuntime(runtimev3.RunEvent{Kind: runtimev3.EventToolEnd, ToolCallID: "call_1"})
	history := []analyzer.Message{{Role: "user", Content: "test"}}
	agent.saveCheckpointUI(1, &history, discardCheckpointUISink{})
	if err := logSink.Close(); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadCheckpoint("session_event_order")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.EventCursor != 1 {
		t.Fatalf("checkpoint cursor=%d want preceding durable event cursor 1", loaded.EventCursor)
	}
	raw, err := os.ReadFile(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kind":"tool.end"`) || !strings.Contains(string(raw), `"kind":"checkpoint.save"`) {
		t.Fatalf("missing append-only session events: %s", raw)
	}
}

func TestRestoreContinuesRunIDAndEventCursor(t *testing.T) {
	capture := &runtimeEventCapture{}
	sequence := &runtimev3.SequenceSink{Sink: capture}
	agent := &DeepAgent{RunID: "new_process_run", Events: sequence, State: NewAgentState("")}
	agent.RestoreFromCheckpoint(&CheckpointData{
		RunID:       "persisted_run",
		EventCursor: 41,
		StepNum:     7,
		State:       NewAgentState(""),
	})
	agent.emitRuntime(runtimev3.RunEvent{Kind: runtimev3.EventModelStart})
	if agent.RunID != "persisted_run" || agent.StartStep != 7 {
		t.Fatalf("run=%q step=%d", agent.RunID, agent.StartStep)
	}
	if len(capture.events) != 1 || capture.events[0].Sequence != 42 || capture.events[0].RunID != "persisted_run" {
		t.Fatalf("resumed event=%#v", capture.events)
	}
}

type runtimeEventCapture struct{ events []runtimev3.RunEvent }

func (s *runtimeEventCapture) Emit(_ context.Context, event runtimev3.RunEvent) error {
	s.events = append(s.events, event)
	return nil
}
