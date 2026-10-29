package remotetools

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"sync"
)

var (
	machineKeyOnce  sync.Once
	machineKeyValue string
)

// machineIdentityKey hashes an OS-level stable machine id (machine-id,
// MachineGuid, IOPlatformUUID) so the value can be written to binding metadata
// without exposing the raw identifier. Hostname is the fallback when the OS
// source is unavailable.
func machineIdentityKey() string {
	machineKeyOnce.Do(func() {
		raw := strings.TrimSpace(machineIdentity())
		if raw == "" {
			raw, _ = os.Hostname()
		}
		raw = strings.ToLower(strings.TrimSpace(raw))
		if raw == "" {
			raw = "unknown-machine"
		}
		sum := sha256.Sum256([]byte("aicli-machine-v1\x00" + raw))
		machineKeyValue = hex.EncodeToString(sum[:8])
	})
	return machineKeyValue
}

// machineInstanceLabel is a short, path-safe hostname used in the readable part
// of an auto instance id.
func machineInstanceLabel() string {
	host, _ := os.Hostname()
	host = strings.ToLower(strings.TrimSpace(host))
	var builder strings.Builder
	lastDash := false
	for _, r := range host {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	label := strings.Trim(builder.String(), "-")
	if len(label) > 12 {
		label = strings.Trim(label[:12], "-")
	}
	return label
}
