package sessionport

import "time"

// AgentID identifies one supported foreign-agent adapter.
type AgentID string

const (
	AgentDeepSeekHarness AgentID = "deepseek-harness"
	AgentWorkBuddy       AgentID = "workbuddy"
)

// SessionView selects which conversation history a source adapter exposes.
type SessionView string

const (
	// ViewSurface follows the source agent's current model-visible surface.
	ViewSurface SessionView = "surface"
	// ViewTranscript follows append-origin human-visible transcript events.
	ViewTranscript SessionView = "transcript"
)

// Capabilities describes the operations one detected instance supports for the
// current adapter version. Apply is intentionally a string so a future adapter
// can report "experimental" without pretending to be stable.
type Capabilities struct {
	List        bool   `json:"list"`
	Read        bool   `json:"read"`
	WriteBundle bool   `json:"write_bundle"`
	Apply       string `json:"apply"` // none | experimental | stable
}

// Instance is one detected data root of one agent.
type Instance struct {
	AgentID         string       `json:"agent_id"`
	InstanceID      string       `json:"instance_id"`
	Root            string       `json:"root"`
	SessionsDir     string       `json:"sessions_dir,omitempty"`
	DBPath          string       `json:"db_path,omitempty"`
	ProjectsDir     string       `json:"projects_dir,omitempty"`
	Format          string       `json:"format,omitempty"`
	FormatVersion   int          `json:"format_version,omitempty"`
	FormatStatus    string       `json:"format_status,omitempty"` // current | migration-required | unsupported | malformed
	AppVersion      string       `json:"app_version,omitempty"`
	SchemaVersion   string       `json:"schema_version,omitempty"`
	Backend         string       `json:"backend,omitempty"`
	Compression     string       `json:"compression,omitempty"`
	SessionCount    int          `json:"session_count"`
	TranscriptCount int          `json:"transcript_count"`
	Busy            bool         `json:"busy"`
	BusyReason      string       `json:"busy_reason,omitempty"`
	Confidence      float64      `json:"confidence"`
	Capabilities    Capabilities `json:"capabilities"`
	Warnings        []string     `json:"warnings,omitempty"`
}

// SessionRef is one source-session candidate without loading its full history.
type SessionRef struct {
	AgentID       string    `json:"agent_id"`
	InstanceID    string    `json:"instance_id"`
	SessionID     string    `json:"session_id"`
	Title         string    `json:"title,omitempty"`
	CWD           string    `json:"cwd,omitempty"`
	File          string    `json:"file,omitempty"`
	FormatVersion int       `json:"format_version,omitempty"`
	FormatStatus  string    `json:"format_status,omitempty"`
	Compression   string    `json:"compression,omitempty"`
	SizeBytes     int64     `json:"size_bytes,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
	EventCount    int       `json:"event_count,omitempty"`
	Readable      bool      `json:"readable"`
	Warnings      []string  `json:"warnings,omitempty"`
}
