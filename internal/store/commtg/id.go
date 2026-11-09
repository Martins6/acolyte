package commtg

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func newID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("comm_tg: generate id: %w", err)
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}
