package chat

import (
	"fmt"
	"os"
	"path/filepath"
)

func lockStore(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("chat store 正被其他进程使用: %w", err)
	}
	return func() { _ = f.Close() }, nil
}
