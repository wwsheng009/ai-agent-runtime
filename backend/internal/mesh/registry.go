package mesh

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// NodeState is the structural state of a node record file. It answers "can
// this file be trusted as a node record at all"; the *effective* liveness
// (pid + heartbeat refinement) is computed by the view layer (see view.go).
type NodeState string

// Node states (architecture §3.5).
const (
	NodeStateLive    NodeState = "live"
	NodeStateStale   NodeState = "stale"
	NodeStateStopped NodeState = "stopped"
	NodeStateUnknown NodeState = "unknown"
)

// ErrUnknownSchema is returned (wrapped) by ReadNodeFile when a record carries
// a schema_version this build does not understand. The record is still
// returned for display, but callers must not act on it (no ownership
// decisions, no calls) — see architecture §3.6.
var ErrUnknownSchema = errors.New("mesh: unknown schema version")

// ProcessInfo describes how the node process was started.
type ProcessInfo struct {
	StartedAt time.Time `json:"started_at"`
	Exe       string    `json:"exe,omitempty"`
	Version   string    `json:"version,omitempty"`
	BuildTime string    `json:"build_time,omitempty"`
	ParentPID int       `json:"parent_pid,omitempty"`
	// Origin answers "who started this process": cli / web / mesh / resume.
	// Always emitted (empty string is meaningful: "unknown"), matching the
	// documented wire shape.
	Origin string `json:"origin"`
	// SpawnedBy is the parent node id when another mesh node spawned us;
	// empty for user-started processes.
	SpawnedBy string `json:"spawned_by"`
}

// EndpointInfo is the loopback control-plane address of the node. The whole
// section is omitted while the loopback server is not running.
type EndpointInfo struct {
	Scheme      string `json:"scheme,omitempty"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	Loopback    bool   `json:"loopback"`
	BaseURL     string `json:"base_url,omitempty"`
	WebBaseURL  string `json:"web_base_url,omitempty"`
	ManifestURL string `json:"manifest_url,omitempty"`
}

// AuthInfo carries the write token. The token is a secret: node records are
// written 0600 and every view masks it unless explicitly revealed.
type AuthInfo struct {
	Mode        string `json:"mode,omitempty"`
	Required    bool   `json:"required"`
	Token       string `json:"token,omitempty"`
	TokenSource string `json:"token_source,omitempty"`
}

// SessionInfo is the currently active session of the node (nil when the node
// has no active session yet).
type SessionInfo struct {
	ID          string    `json:"id,omitempty"`
	Title       string    `json:"title,omitempty"`
	Busy        bool      `json:"busy"`
	TurnID      string    `json:"turn_id,omitempty"`
	ActivatedAt time.Time `json:"activated_at,omitempty"`
}

// WorkspaceInfo is a filter/grouping dimension only — never a permission or
// visibility boundary (architecture §9.3).
type WorkspaceInfo struct {
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

// LivenessInfo is the writer's own view of its liveness. Readers may override
// State with "stale" after a pid/heartbeat check; they never write back.
type LivenessInfo struct {
	StartedAt       time.Time `json:"started_at,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
	HeartbeatAt     time.Time `json:"heartbeat_at,omitempty"`
	HeartbeatTTLSec int       `json:"heartbeat_ttl_sec,omitempty"`
	State           string    `json:"state,omitempty"`
}

// NodeRecord is one node's self-description: mesh/nodes/<node_id>.json
// (schema_version 2, architecture §3.1).
//
// Exactly one process writes this file (its owner) and it is always rewritten
// whole via a temp-file + rename; every other process only reads it.
type NodeRecord struct {
	SchemaVersion int            `json:"schema_version"`
	NodeID        string         `json:"node_id"`
	PID           int            `json:"pid"`
	Kind          string         `json:"kind,omitempty"`
	Process       ProcessInfo    `json:"process,omitempty"`
	Endpoint      *EndpointInfo  `json:"endpoint,omitempty"`
	Auth          *AuthInfo      `json:"auth,omitempty"`
	Session       *SessionInfo   `json:"session,omitempty"`
	Workspace     *WorkspaceInfo `json:"workspace,omitempty"`
	Capabilities  []string       `json:"capabilities,omitempty"`
	Liveness      LivenessInfo   `json:"liveness,omitempty"`
}

// NodeFile is the tolerant read result for one node record file.
type NodeFile struct {
	// Path is the absolute file path the record was read from.
	Path string
	// Record is the parsed record, or nil when the file could not be parsed.
	Record *NodeRecord
	// State is the structural state: NodeStateUnknown for unreadable files and
	// unknown schemas, otherwise the writer-declared liveness state (defaulting
	// to live when the writer declared nothing usable).
	State NodeState
	// Err carries the parse/schema problem (nil for a healthy record).
	Err error
}

// NowUTC returns the current time in UTC truncated to seconds — the timestamp
// granularity used by every mesh record.
func NowUTC() time.Time {
	return time.Now().UTC().Truncate(time.Second)
}

// Validate checks the minimum invariants required to write a record.
func (r NodeRecord) Validate() error {
	if strings.TrimSpace(r.NodeID) == "" {
		return fmt.Errorf("mesh: node record needs a node_id")
	}
	if r.PID < 0 {
		return fmt.Errorf("mesh: node record %s has negative pid %d", r.NodeID, r.PID)
	}
	return nil
}

// WriteNodeRecord writes (or replaces) this node's record atomically. The
// writer must own the record; passing another node's id is a programming
// error and callers must not do it.
func WriteNodeRecord(paths Paths, record NodeRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	path := paths.NodePath(record.NodeID)
	if path == "" {
		// Fail-closed: no mesh root (or an unusable node id) means "persist
		// nothing", never a CWD-relative fallback.
		return nil
	}
	if record.SchemaVersion == 0 {
		record.SchemaVersion = SchemaVersion
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("mesh: encode node record: %w", err)
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, 0o600)
}

// DeleteNodeRecord removes this node's record file. A missing file is not an
// error (double exit paths must stay quiet).
func DeleteNodeRecord(paths Paths, nodeID string) error {
	path := paths.NodePath(nodeID)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("mesh: delete node record: %w", err)
	}
	return nil
}

// ReadNodeFile reads one node record file tolerantly:
//
//   - missing / unreadable / malformed JSON → State=unknown, Record=nil, Err set
//   - schema_version != SchemaVersion      → State=unknown, Record parsed, Err=ErrUnknownSchema
//   - otherwise                            → declared liveness state (default live)
//
// It never panics and never writes to disk (readers never repair other
// processes' files — architecture §4.2 rule 4).
func ReadNodeFile(path string) NodeFile {
	result := NodeFile{Path: path, State: NodeStateUnknown}
	data, err := os.ReadFile(path)
	if err != nil {
		result.Err = err
		return result
	}
	var record NodeRecord
	if err := json.Unmarshal(data, &record); err != nil {
		result.Err = fmt.Errorf("mesh: parse node record %s: %w", path, err)
		return result
	}
	result.Record = &record
	if record.SchemaVersion != SchemaVersion {
		result.Err = fmt.Errorf("%w: %s has schema_version=%d (want %d)",
			ErrUnknownSchema, path, record.SchemaVersion, SchemaVersion)
		return result
	}
	result.State = declaredNodeState(record.Liveness.State)
	return result
}

func declaredNodeState(declared string) NodeState {
	switch NodeState(strings.TrimSpace(strings.ToLower(declared))) {
	case NodeStateStale:
		return NodeStateStale
	case NodeStateStopped:
		return NodeStateStopped
	case NodeStateUnknown:
		return NodeStateUnknown
	default:
		// The writer is running (it wrote this file) unless it said otherwise.
		return NodeStateLive
	}
}

// ListNodeFiles lists every node record under paths.Nodes, sorted by node id.
// A missing directory yields an empty slice (no mesh yet is not an error).
// Unparsable files are included with State=unknown so callers can show them.
func ListNodeFiles(paths Paths) []NodeFile {
	if !paths.Enabled() {
		return nil
	}
	entries, err := os.ReadDir(paths.Nodes)
	if err != nil {
		return nil
	}
	files := make([]NodeFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		files = append(files, ReadNodeFile(filepath.Join(paths.Nodes, entry.Name())))
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

// writeFileAtomic writes data to path via a same-directory temp file, fsync and
// rename, so a reader never observes a half-written file (risk R1). The temp
// file is removed on every failure path.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mesh: create dir %s: %w", dir, err)
	}
	file, err := os.CreateTemp(dir, fmt.Sprintf(".tmp-%d-*", os.Getpid()))
	if err != nil {
		return fmt.Errorf("mesh: create temp file in %s: %w", dir, err)
	}
	tmpPath := file.Name()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("mesh: write %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("mesh: sync %s: %w", path, err)
	}
	// Best effort: on Windows this only toggles the read-only bit.
	_ = file.Chmod(perm)
	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("mesh: close %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("mesh: replace %s: %w", path, err)
	}
	return nil
}
