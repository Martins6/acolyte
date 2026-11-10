package lock

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/martins6/acolyte/internal/paths"
)

// SessionPath returns the lock file for an OpenCode session id. Session ids
// are hashed so arbitrary ids cannot escape the locks directory.
func SessionPath(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("lock: empty session id")
	}
	home, err := paths.Home()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(sessionID))
	return filepath.Join(home, "locks", "sessions", hex.EncodeToString(sum[:])+".lock"), nil
}
