package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PendingChatReply survives inbox rotation and daemon downtime. Removal only
// acknowledges transfer to the chat daemon's durable outbox, not network send.
type PendingChatReply struct {
	ID   string
	Line ChatInboxLine
}

func pendingChatDir(store string) string { return chatInboxPath(store) + ".pending" }

func savePendingChatReply(store string, line ChatInboxLine) error {
	if !strings.HasPrefix(line.Session, "session_chat_") || (line.Kind != "result" && line.Kind != "error") {
		return nil
	}
	data, err := json.Marshal(line)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	dir := pendingChatDir(store)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, hex.EncodeToString(sum[:])+".json"))
}

func PendingChatReplies(store string) ([]PendingChatReply, error) {
	paths, err := filepath.Glob(filepath.Join(pendingChatDir(store), "*.json"))
	if err != nil {
		return nil, err
	}
	var replies []PendingChatReply
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var line ChatInboxLine
		if err = json.Unmarshal(data, &line); err != nil {
			return nil, fmt.Errorf("pending chat reply %s: %w", filepath.Base(path), err)
		}
		replies = append(replies, PendingChatReply{ID: strings.TrimSuffix(filepath.Base(path), ".json"), Line: line})
	}
	return replies, nil
}

func AckChatReply(store, id string) error {
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("invalid pending chat reply id")
	}
	err = os.Remove(filepath.Join(pendingChatDir(store), id+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
