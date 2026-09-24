package harness

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/config"
	"ai-edr/internal/executor"
	"ai-edr/internal/harness/subagent"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// SubAgentCheckpoint describes one independently recoverable child execution.
// Credentials are deliberately excluded; target connections are reconstructed
// from the current configured inventory on resume.
type SubAgentCheckpoint struct {
	Key            string   `json:"key"`
	SpecName       string   `json:"spec_name"`
	TaskPrompt     string   `json:"task_prompt"`
	TargetName     string   `json:"target_name,omitempty"`
	TargetProtocol string   `json:"target_protocol,omitempty"`
	TargetHost     string   `json:"target_host,omitempty"`
	TargetUser     string   `json:"target_user,omitempty"`
	Phase          string   `json:"phase"` // ready | action_pending | complete
	PendingAction  string   `json:"pending_action,omitempty"`
	Results        []string `json:"results,omitempty"`
	Report         string   `json:"report,omitempty"`
}

func subAgentCheckpointKey(sessionID, specName, taskPrompt string, target config.TargetConfig) string {
	assignment := subAgentAssignmentForEstimate(taskPrompt)
	identity := strings.Join([]string{sessionID, specName, assignment, target.Name, target.Protocol, target.Host, target.User}, "\x00")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
}

func subAgentCheckpointStore(parent *CheckpointStore, key string) (*CheckpointStore, error) {
	if parent == nil {
		return nil, nil
	}
	if len(key) != 64 || strings.Trim(key, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("invalid sub-agent checkpoint key")
	}
	dir := filepath.Join(parent.SessionDir(), "subagents", key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return &CheckpointStore{dir: dir, sessionID: parent.sessionID}, nil
}

func loadSubAgentCheckpoint(store *CheckpointStore) (*CheckpointData, error) {
	if store == nil {
		return nil, nil
	}
	path := filepath.Join(store.SessionDir(), "checkpoint.json")
	data, err := loadCheckpointFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

func listSubAgentCheckpoints(parent *CheckpointStore) ([]*CheckpointData, error) {
	if parent == nil {
		return nil, nil
	}
	root := filepath.Join(parent.SessionDir(), "subagents")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshots []*CheckpointData
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		key := entry.Name()
		if len(key) != 64 || strings.Trim(key, "0123456789abcdef") != "" {
			continue
		}
		data, err := loadCheckpointFile(filepath.Join(root, key, "checkpoint.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if data.SubAgent == nil || data.SubAgent.Key != key || data.SessionID != parent.sessionID {
			return nil, fmt.Errorf("sub-agent checkpoint identity mismatch: %s", key)
		}
		snapshots = append(snapshots, data)
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].SubAgent.Key < snapshots[j].SubAgent.Key })
	return snapshots, nil
}

func listScopedSubAgentCheckpoints(parent *CheckpointStore, keys map[string]struct{}) ([]*CheckpointData, error) {
	if keys == nil {
		return listSubAgentCheckpoints(parent)
	}
	if parent == nil || len(keys) == 0 {
		return nil, nil
	}
	snapshots := make([]*CheckpointData, 0, len(keys))
	for key := range keys {
		if len(key) != 64 || strings.Trim(key, "0123456789abcdef") != "" {
			return nil, fmt.Errorf("invalid scoped sub-agent key")
		}
		data, err := loadCheckpointFile(filepath.Join(parent.SessionDir(), "subagents", key, "checkpoint.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if data.SubAgent == nil || data.SubAgent.Key != key || data.SessionID != parent.sessionID {
			return nil, fmt.Errorf("sub-agent checkpoint identity mismatch: %s", key)
		}
		snapshots = append(snapshots, data)
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].SubAgent.Key < snapshots[j].SubAgent.Key })
	return snapshots, nil
}

func (r *SubAgentRunner) openCheckpoint(spec subagent.Spec, taskPrompt string) (*CheckpointStore, *CheckpointData, *SubAgentCheckpoint, error) {
	if r.CheckpointParent == nil {
		return nil, nil, nil, nil
	}
	key := subAgentCheckpointKey(r.CheckpointParent.sessionID, spec.Name, taskPrompt, r.Target)
	if r.CheckpointKey != "" {
		key = r.CheckpointKey
	}
	store, err := subAgentCheckpointStore(r.CheckpointParent, key)
	if err != nil {
		return nil, nil, nil, err
	}
	data, err := loadSubAgentCheckpoint(store)
	if err != nil {
		return nil, nil, nil, err
	}
	if r.Parent != nil {
		if err := r.Parent.registerSubAgentKey(key); err != nil {
			return nil, nil, nil, fmt.Errorf("登记父会话子任务: %w", err)
		}
	}
	meta := &SubAgentCheckpoint{
		Key: key, SpecName: spec.Name, TaskPrompt: taskPrompt,
		TargetName: r.Target.Name, TargetProtocol: r.Target.Protocol,
		TargetHost: r.Target.Host, TargetUser: r.Target.User, Phase: "ready",
	}
	if data != nil {
		if data.SubAgent == nil || data.SubAgent.Key != key || data.SubAgent.SpecName != spec.Name || data.SessionID != r.CheckpointParent.sessionID {
			return nil, nil, nil, fmt.Errorf("sub-agent checkpoint identity mismatch")
		}
		meta = data.SubAgent
	}
	return store, data, meta, nil
}

func (r *SubAgentRunner) saveCheckpoint(store *CheckpointStore, meta *SubAgentCheckpoint, step int, history []analyzer.Message) error {
	if store == nil {
		return nil
	}
	return store.Save(CheckpointData{StepNum: step, State: r.State, History: history, SubAgent: meta})
}

func subAgentTargetFromInventory(meta *SubAgentCheckpoint) (config.TargetConfig, bool) {
	if meta == nil || (meta.TargetName == "" && meta.TargetHost == "") {
		return config.TargetConfig{}, true
	}
	for _, target := range config.GlobalConfig.Targets {
		if target.Name == meta.TargetName && target.Protocol == meta.TargetProtocol && target.Host == meta.TargetHost && target.User == meta.TargetUser {
			return target, true
		}
	}
	return config.TargetConfig{}, false
}

func (a *DeepAgent) resumePendingSubAgents(cfg RunLoopConfig, ui UISink, step int) error {
	if !a.resumedFromCheckpoint || a.Checkpoint == nil || cfg.History == nil {
		return nil
	}
	a.resumedFromCheckpoint = false
	if !canContinueSavedSubAgents(*cfg.History, a.restoredHistoryLen) {
		return nil
	}
	snapshots, err := listScopedSubAgentCheckpoints(a.Checkpoint, a.resumeSubAgentKeys)
	if err != nil {
		return fmt.Errorf("查找子 Agent checkpoint: %w", err)
	}
	if a.resumeSubAgentKeys == nil {
		// Older parent checkpoints had no child index. Capture the discovered
		// set now so a later parent save will not adopt unrelated future work.
		a.resumeSubAgentKeys = make(map[string]struct{}, len(snapshots))
		for _, snapshot := range snapshots {
			a.resumeSubAgentKeys[snapshot.SubAgent.Key] = struct{}{}
		}
	}
	resumedAny := false
	for _, snapshot := range snapshots {
		if a.resumeSubAgentKeys != nil {
			if _, ok := a.resumeSubAgentKeys[snapshot.SubAgent.Key]; !ok {
				continue
			}
		}
		if shouldStop(cfg.Stop) {
			if resumedAny {
				break
			}
			return nil
		}
		meta := snapshot.SubAgent
		spec, ok := subagent.Find(meta.SpecName)
		if !ok {
			return fmt.Errorf("无法恢复未知子 Agent %q", meta.SpecName)
		}
		target, ok := subAgentTargetFromInventory(meta)
		if !ok {
			return fmt.Errorf("子 Agent %s 的目标 %s/%s 已不在配置中", meta.SpecName, meta.TargetName, meta.TargetHost)
		}
		runner := makeSubAgentRunner(a)
		runner.CheckpointParent = a.Checkpoint
		runner.CheckpointKey = meta.Key
		runner.UI = ui
		runner.ConfirmFn = cfg.ConfirmFn
		runner.Stop = cfg.Stop
		runner.MaxStepsCap = cfg.SubAgentMaxSteps
		runner.Target = target
		if target.Name != "" || target.Host != "" {
			runner.Executor, err = executor.NewEphemeralExecutor(target)
			if err != nil {
				return fmt.Errorf("恢复子 Agent %s 的目标连接: %w", spec.Name, err)
			}
		}
		ui.Emit(UIEvent{Kind: EventSubAgentStart, Message: spec.Name + " 恢复", Detail: meta.TaskPrompt})
		out, runErr := runner.Run(*spec, meta.TaskPrompt, cfg.SysCtx, cfg.BatchMode)
		if runner.Executor != nil && (target.Name != "" || target.Host != "") {
			runner.Executor.Close()
		}
		if runErr != nil {
			return fmt.Errorf("恢复子 Agent %s: %w", spec.Name, runErr)
		}
		if shouldStop(cfg.Stop) {
			// A stop can arrive immediately after the child finishes. Only a
			// completed child may be handed to the parent at this point; a paused
			// child stays in its own checkpoint for the next resume.
			store, storeErr := subAgentCheckpointStore(a.Checkpoint, meta.Key)
			if storeErr != nil {
				return storeErr
			}
			latest, loadErr := loadSubAgentCheckpoint(store)
			if loadErr != nil {
				return loadErr
			}
			if latest == nil || latest.SubAgent == nil || latest.SubAgent.Phase != "complete" {
				if resumedAny {
					break
				}
				return nil
			}
		}
		ui.Emit(UIEvent{Kind: EventSubAgentResult, Message: spec.Name + " 恢复完成", Detail: out})
		*cfg.History = append(*cfg.History, analyzer.Message{Role: "user", Content: fmt.Sprintf("子 Agent [%s] 从 checkpoint 恢复后的结果：\n%s", spec.Name, out), Synthetic: true})
		a.deliveredSubAgents.Store(meta.Key, struct{}{})
		if a.State != nil {
			a.State.ObserveCoreClues(out, "subagent/"+spec.Name)
		}
		resumedAny = true
		if shouldStop(cfg.Stop) {
			break
		}
	}
	if resumedAny {
		if !a.saveCheckpointUI(step, cfg.History, ui) {
			return fmt.Errorf("子 Agent 恢复结果未能写入父会话 checkpoint")
		}
	}
	return nil
}

func (a *DeepAgent) registerSubAgentKey(key string) error {
	a.subAgentTurnMu.Lock()
	defer a.subAgentTurnMu.Unlock()
	if a.subAgentTurnKeys == nil {
		a.subAgentTurnKeys = make(map[string]struct{})
	}
	a.subAgentTurnKeys[key] = struct{}{}
	if a.subAgentActionKeys != nil {
		a.subAgentActionKeys[key] = struct{}{}
	}
	if a.Checkpoint == nil {
		return nil
	}
	data, err := loadCheckpointFile(filepath.Join(a.Checkpoint.SessionDir(), "checkpoint.json"))
	if errors.Is(err, os.ErrNotExist) {
		// Direct runner callers can create a child before the first parent run.
		return nil
	}
	if err != nil {
		return err
	}
	keys := make(map[string]struct{})
	if data.SubAgentKeys != nil {
		for _, saved := range *data.SubAgentKeys {
			keys[saved] = struct{}{}
		}
	}
	keys[key] = struct{}{}
	indexed := make([]string, 0, len(keys))
	for saved := range keys {
		indexed = append(indexed, saved)
	}
	sort.Strings(indexed)
	data.SubAgentKeys = &indexed
	return a.Checkpoint.Save(*data)
}

func canContinueSavedSubAgents(history []analyzer.Message, restoredLen int) bool {
	if restoredLen < 0 || restoredLen > len(history) {
		return false
	}
	for _, message := range history[restoredLen:] {
		if !analyzer.IsRealUserTurn(message) {
			continue
		}
		text := strings.TrimSpace(message.Content)
		text = strings.TrimSpace(strings.TrimPrefix(text, "需求："))
		text = strings.TrimSpace(strings.TrimPrefix(text, "需求:"))
		text = strings.TrimSpace(strings.TrimPrefix(text, "用户补充："))
		text = strings.TrimSpace(strings.TrimPrefix(text, "用户补充:"))
		switch strings.ToLower(text) {
		case "继续", "继续任务", "继续之前的任务", "接着做", "resume", "continue":
		default:
			return false
		}
	}
	return true
}

func (a *DeepAgent) beginSubAgentAction() {
	a.subAgentTurnMu.Lock()
	a.subAgentActionKeys = make(map[string]struct{})
	a.subAgentTurnMu.Unlock()
}

func (a *DeepAgent) discardSubAgentActionResults() {
	a.subAgentTurnMu.Lock()
	a.subAgentActionKeys = nil
	a.subAgentTurnMu.Unlock()
}

func (a *DeepAgent) markTaskSubAgentResultsDelivered() {
	a.subAgentTurnMu.Lock()
	defer a.subAgentTurnMu.Unlock()
	for key := range a.subAgentActionKeys {
		a.deliveredSubAgents.Store(key, struct{}{})
	}
	a.subAgentActionKeys = nil
}

func acknowledgeCompletedSubAgents(parent *CheckpointStore, delivered *sync.Map) error {
	if parent == nil || delivered == nil {
		return nil
	}
	var keys []string
	delivered.Range(func(key, _ any) bool {
		if name, ok := key.(string); ok {
			keys = append(keys, name)
		}
		return true
	})
	sort.Strings(keys)
	for _, key := range keys {
		if len(key) != 64 || strings.Trim(key, "0123456789abcdef") != "" {
			return fmt.Errorf("invalid delivered sub-agent key")
		}
		path := filepath.Join(parent.SessionDir(), "subagents", key, "checkpoint.json")
		data, err := loadCheckpointFile(path)
		if errors.Is(err, os.ErrNotExist) {
			delivered.Delete(key)
			continue
		}
		if err != nil {
			return err
		}
		if data.SubAgent == nil || data.SubAgent.Key != key || data.SessionID != parent.sessionID {
			return fmt.Errorf("sub-agent checkpoint identity mismatch: %s", key)
		}
		if data.SubAgent.Phase != "complete" {
			continue
		}
		if err := os.RemoveAll(filepath.Dir(path)); err != nil {
			return err
		}
		delivered.Delete(key)
	}
	return nil
}
