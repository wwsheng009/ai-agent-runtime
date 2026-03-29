package checkpoint

import (
	"time"

	runtimetypes "github.com/ai-gateway/ai-agent-runtime/internal/types"
)

// FileSnapshot captures file state before/after a mutation.
type FileSnapshot struct {
	Path         string `json:"path"`
	Op           string `json:"op"`
	Before       string `json:"before,omitempty"`
	After        string `json:"after,omitempty"`
	BeforeExists bool   `json:"before_exists"`
	AfterExists  bool   `json:"after_exists"`
	BeforeHash   string `json:"before_hash,omitempty"`
	AfterHash    string `json:"after_hash,omitempty"`
	Diff         string `json:"diff,omitempty"`
	Skipped      bool   `json:"skipped,omitempty"`
	Error        string `json:"error,omitempty"`
}

// PendingCheckpoint tracks a pending auto-capture.
type PendingCheckpoint struct {
	SessionID               string
	ToolName                string
	ToolCallID              string
	Paths                   []string
	DirectoryRoots          []string
	DirectorySnapshotErrors []string
	Snapshots               map[string]*FileSnapshot
	StartedAt               time.Time
	MessageCount            int
	Conversation            []runtimetypes.Message
}

// RestoreMode describes checkpoint restore mode.
type RestoreMode string

const (
	RestoreCode         RestoreMode = "code"
	RestoreConversation RestoreMode = "conversation"
	RestoreBoth         RestoreMode = "both"
)

// RestoreRequest describes a checkpoint restore request.
type RestoreRequest struct {
	SessionID    string
	CheckpointID string
	Mode         RestoreMode
	PreviewOnly  bool
}

// RestoreResult captures restore outcomes.
type RestoreResult struct {
	CheckpointID         string                 `json:"checkpoint_id"`
	Mode                 string                 `json:"mode"`
	AppliedPaths         []string               `json:"applied_paths,omitempty"`
	Errors               []string               `json:"errors,omitempty"`
	Preview              []string               `json:"preview,omitempty"`
	PreviewFiles         []PreviewFile          `json:"preview_files,omitempty"`
	ConversationChanged  bool                   `json:"conversation_changed,omitempty"`
	ConversationHead     int                    `json:"conversation_head,omitempty"`
	ConversationExact    bool                   `json:"conversation_exact,omitempty"`
	ConversationMessages []runtimetypes.Message `json:"conversation_messages,omitempty"`
	Provenance           ProvenanceSummary      `json:"provenance,omitempty"`
}

// PreviewFile describes a checkpoint preview entry.
type PreviewFile struct {
	Path     string `json:"path"`
	Change   string `json:"change"`
	DiffText string `json:"diff_text,omitempty"`
}

// ProvenanceSummary describes profile/resource provenance linked to a checkpoint.
type ProvenanceSummary struct {
	SourceRefs            []string       `json:"source_refs,omitempty"`
	ProfileResourceRefs   []string       `json:"profile_resource_refs,omitempty"`
	ProfileResourceKinds  map[string]int `json:"profile_resource_kinds,omitempty"`
	ProfileResourceCount  int            `json:"profile_resource_count,omitempty"`
	ProfileMemoryCount    int            `json:"profile_memory_count,omitempty"`
	ProfileNotesCount     int            `json:"profile_notes_count,omitempty"`
	ProfileResourceLabels []string       `json:"profile_resource_labels,omitempty"`
}
