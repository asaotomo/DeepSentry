package builtin

import (
	"crypto/sha256"
	"encoding/hex"
)

func sha256Sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
