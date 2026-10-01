package lsp

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sort"
	"strings"
)

// Fingerprints are stable, truncated, non-reversible identifiers used by the
// observability plane to correlate facts without exporting paths or
// diagnostic text (docs/plan/lsp-observability-and-analysis-plan-20260929.md
// §3.4). They are deterministic across sessions so the offline baseline can
// compute edit→diagnostic closure; they are not a secrecy boundary, only a
// "do not persist the raw path/message" boundary.
const fingerprintBytes = 12

// FingerprintPath hashes a (usually absolute) file path.
func FingerprintPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	// Windows paths are case-insensitive: normalize so the same file yields
	// the same fingerprint regardless of drive-letter case.
	return shortHash(strings.ToLower(filepath.ToSlash(trimmed)))
}

// DiagnosticsFingerprint hashes the canonical identity of a diagnostic set
// (order-independent). Empty sets return "".
func DiagnosticsFingerprint(items []Diagnostic) string {
	if len(items) == 0 {
		return ""
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.Key())
	}
	sort.Strings(keys)
	return shortHash(strings.Join(keys, "\x00"))
}

func shortHash(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])[:fingerprintBytes]
}
