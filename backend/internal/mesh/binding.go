package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// This file owns the two halves of what used to be `chat_web_port_store.go`
// (deleted in S3):
//
//   - SessionBinding: the persistent "session ↔ address" preference
//     (architecture §3.2), stored in mesh/bindings/<session_id>.json. `resume`
//     reads it to keep the loopback port stable; the mesh views read it to show
//     "last served at" even when no node is running.
//   - the process endpoint: the loopback address *this* process is serving on.
//     The old store kept the same value in package-level state; it lives here so
//     the binding path keeps working when the mesh is disabled (--mesh=false) or
//     the node record could not be written (MN1).
//
// A binding never carries a token (red line M7): consumers that need one must
// read it from the live node record.

// SessionBinding is the persistent preference record of one chat session.
type SessionBinding struct {
	SchemaVersion     int          `json:"schema_version"`
	SessionID         string       `json:"session_id"`
	Preferred         *BindingAddr `json:"preferred,omitempty"`
	LastNodeID        string       `json:"last_node_id,omitempty"`
	LastWorkspacePath string       `json:"last_workspace_path,omitempty"`
	UpdatedAt         time.Time    `json:"updated_at"`
}

// BindingAddr is the preferred host/port pair stored in a binding.
type BindingAddr struct {
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
}

// BindingUpdate is what a serving node publishes for one session.
type BindingUpdate struct {
	SessionID     string
	Host          string
	Port          int
	NodeID        string
	WorkspacePath string
}

// LoadBinding reads mesh/bindings/<session_id>.json.
//
// A missing, unreadable, malformed or unknown-schema file — or a port outside
// 1..65535 — all mean "no usable binding" (ok=false), so the caller falls back
// to a random port exactly like the pre-mesh port record did.
func LoadBinding(paths Paths, sessionID string) (SessionBinding, bool) {
	path := paths.BindingPath(sessionID)
	if path == "" {
		return SessionBinding{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionBinding{}, false
	}
	var binding SessionBinding
	if err := json.Unmarshal(data, &binding); err != nil {
		return SessionBinding{}, false
	}
	if binding.SchemaVersion != SchemaVersion {
		return SessionBinding{}, false
	}
	if binding.Preferred == nil || binding.Preferred.Port < 1 || binding.Preferred.Port > 65535 {
		return SessionBinding{}, false
	}
	if strings.TrimSpace(binding.SessionID) == "" {
		binding.SessionID = strings.TrimSpace(sessionID)
	}
	return binding, true
}

// SaveBinding writes a binding atomically (0644 — it holds no secrets).
//
// Writing is fail-closed: with an unresolved mesh root the call is a silent
// no-op, never a CWD-relative fallback. An unusable session id is a caller bug
// and is reported as an error.
func SaveBinding(paths Paths, binding SessionBinding) error {
	if !paths.Enabled() {
		return nil
	}
	path := paths.BindingPath(binding.SessionID)
	if path == "" {
		return fmt.Errorf("mesh: invalid session id %q for a binding", binding.SessionID)
	}
	if binding.Preferred != nil && (binding.Preferred.Port < 1 || binding.Preferred.Port > 65535) {
		return fmt.Errorf("mesh: invalid binding port %d (want 1..65535)", binding.Preferred.Port)
	}
	if binding.SchemaVersion == 0 {
		binding.SchemaVersion = SchemaVersion
	}
	if binding.UpdatedAt.IsZero() {
		binding.UpdatedAt = NowUTC()
	}
	data, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return fmt.Errorf("mesh: encode binding: %w", err)
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, 0o644)
}

// TouchBinding publishes update.SessionID's binding: the preferred address plus
// the last server (node id / workspace path) and updated_at=now. It is the
// one-liner the serving process calls when a session becomes active.
func TouchBinding(paths Paths, update BindingUpdate) error {
	if update.Port < 1 || update.Port > 65535 {
		return fmt.Errorf("mesh: invalid binding port %d (want 1..65535)", update.Port)
	}
	return SaveBinding(paths, SessionBinding{
		SchemaVersion:     SchemaVersion,
		SessionID:         strings.TrimSpace(update.SessionID),
		Preferred:         &BindingAddr{Host: strings.TrimSpace(update.Host), Port: update.Port},
		LastNodeID:        strings.TrimSpace(update.NodeID),
		LastWorkspacePath: strings.TrimSpace(update.WorkspacePath),
		UpdatedAt:         NowUTC(),
	})
}

// ---------------------------------------------------------------------------
// Process endpoint: the loopback address this process is serving on
// ---------------------------------------------------------------------------

var (
	processEndpointMu   sync.RWMutex
	processEndpointHost string
	processEndpointPort int
	processEndpointSet  bool
)

// SetProcessEndpoint records the loopback control-plane address this process
// just started listening on. Ports outside 1..65535 are ignored, so a failed
// server start cannot erase a working address.
//
// It is deliberately independent of the node record: the session binding path
// must keep working when the mesh is disabled (--mesh=false) or when the node
// record could not be written.
func SetProcessEndpoint(host string, port int) {
	if port < 1 || port > 65535 {
		return
	}
	processEndpointMu.Lock()
	processEndpointHost = strings.TrimSpace(host)
	processEndpointPort = port
	processEndpointSet = true
	processEndpointMu.Unlock()
}

// ProcessEndpoint returns the address recorded by SetProcessEndpoint. ok=false
// before the loopback server is up (or after ClearProcessEndpoint): callers
// must then skip writing a binding rather than record a bogus port.
func ProcessEndpoint() (host string, port int, ok bool) {
	processEndpointMu.RLock()
	defer processEndpointMu.RUnlock()
	if !processEndpointSet {
		return "", 0, false
	}
	return processEndpointHost, processEndpointPort, true
}

// ClearProcessEndpoint forgets the recorded address (the server is gone).
func ClearProcessEndpoint() {
	processEndpointMu.Lock()
	processEndpointHost = ""
	processEndpointPort = 0
	processEndpointSet = false
	processEndpointMu.Unlock()
}
