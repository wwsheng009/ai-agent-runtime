package agent

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// NodeIDFileName is the per-machine gateway node identity file, stored under
// the aicli home directory.
const NodeIDFileName = "gateway-node-id"

// NodeIDPath resolves the identity file location (aicli home overridable via
// AICLI_HOME, matching config.DefaultConfigSearchPaths behavior closely enough
// for an identity file).
func NodeIDPath() (string, error) {
	home := strings.TrimSpace(os.Getenv("AICLI_HOME"))
	if home == "" {
		home = strings.TrimSpace(os.Getenv("USERPROFILE"))
	}
	if home == "" {
		home = strings.TrimSpace(os.Getenv("HOME"))
	}
	if home == "" {
		return "", os.ErrNotExist
	}
	return filepath.Join(home, ".aicli", NodeIDFileName), nil
}

// LoadOrCreateNodeID returns this machine's stable gateway node id. It is
// generated once and persisted so reconnects (including to a different gateway
// instance after a failover) keep the same identity and replace the stale
// registry entry instead of accumulating a new one. An id that cannot be
// persisted degrades to a per-process random id (worst case: the pre-existing
// behavior).
func LoadOrCreateNodeID() string {
	path, err := NodeIDPath()
	if err == nil {
		if data, readErr := os.ReadFile(path); readErr == nil {
			if id := strings.TrimSpace(string(data)); id != "" {
				return id
			}
		}
	}
	id, idErr := newNodeID()
	if idErr != nil {
		return ""
	}
	if err == nil {
		if dir := filepath.Dir(path); dir != "" {
			_ = os.MkdirAll(dir, 0o700)
			_ = os.WriteFile(path, []byte(id+"\n"), 0o600)
		}
	}
	return id
}

// newNodeID builds node-<16 hex chars>.
func newNodeID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "node-" + hex.EncodeToString(buf), nil
}
