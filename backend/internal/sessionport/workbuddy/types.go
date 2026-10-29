package workbuddy

import (
	"encoding/json"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	agentID       = string(sessionport.AgentWorkBuddy)
	formatName    = "workbuddy-session-jsonl"
	currentFormat = 1
)

// ImportOptions selects one WorkBuddy session. FilePath is for standalone JSONL
// bundles and diagnostics; Root/DBPath/ProjectsDir are used for a live instance.
type ImportOptions struct {
	Root        string
	Instance    string
	SessionID   string
	FilePath    string
	DBPath      string
	ProjectsDir string
	Latest      bool
	View        sessionport.SessionView
}

// ExportOptions describes an aicli → WorkBuddy JSONL bundle. The bundle does
// not write workbuddy.db; a live target needs an explicit index/apply step.
type ExportOptions struct {
	SessionID     string
	CWD           string
	Title         string
	CreatedAt     time.Time
	Model         string
	History       []runtimetypes.Message
	BundleDir     string
	SourceSession string
	UserID        string
	TargetUser    string
}

// ExportResult is one published WorkBuddy-native bundle.
type ExportResult struct {
	SessionID    string
	BundleDir    string
	SessionFile  string
	ManifestFile string
	IndexFile    string
	EventCount   int
	Warnings     []string
}

// Imported is one normalized aicli session read from WorkBuddy.
type Imported struct {
	Session    *runtimechat.Session
	SourceFile string
	Ref        sessionport.SessionRef
	Warnings   []string
}

// Loaded is one parsed WorkBuddy JSONL file.
type Loaded struct {
	Ref       sessionport.SessionRef
	Records   []Record
	Title     string
	CWD       string
	Model     string
	CreatedAt int64
	Warnings  []string
}

// Record is the subset of the WorkBuddy event envelope needed for history
// normalization. Unknown fields are intentionally ignored so a newer desktop
// build does not make old sessions unreadable.
type Record struct {
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	ParentID         string          `json:"parentId,omitempty"`
	Timestamp        int64           `json:"timestamp,omitempty"`
	Role             string          `json:"role,omitempty"`
	Content          []ContentBlock  `json:"content,omitempty"`
	CallID           string          `json:"callId,omitempty"`
	Name             string          `json:"name,omitempty"`
	Arguments        string          `json:"arguments,omitempty"`
	Status           string          `json:"status,omitempty"`
	Output           json.RawMessage `json:"output,omitempty"`
	RawContent       []ContentBlock  `json:"rawContent,omitempty"`
	AITitle          string          `json:"aiTitle,omitempty"`
	EditedUserItemID string          `json:"editedUserItemId,omitempty"`
	SessionID        string          `json:"sessionId,omitempty"`
	CWD              string          `json:"cwd,omitempty"`
	ProviderData     ProviderData    `json:"providerData,omitempty"`
	Metadata         map[string]any  `json:"_meta,omitempty"`
	Message          map[string]any  `json:"message,omitempty"`
}

// ContentBlock is a WorkBuddy message/content block.
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Value    string `json:"value,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Name     string `json:"name,omitempty"`
	Path     string `json:"path,omitempty"`
}

// ProviderData carries the stable per-message model/request fields.
type ProviderData struct {
	Agent                 string `json:"agent,omitempty"`
	Model                 string `json:"model,omitempty"`
	RequestModelID        string `json:"requestModelId,omitempty"`
	RequestModelName      string `json:"requestModelName,omitempty"`
	MessageID             string `json:"messageId,omitempty"`
	ConversationRequestID string `json:"conversationRequestId,omitempty"`
	StepSeq               int    `json:"stepSeq,omitempty"`
}

// DB session metadata used for detection/list and provenance.
type dbSession struct {
	ID              string
	CWD             string
	UserID          string
	Title           string
	CustomTitle     string
	Status          string
	Model           string
	Transport       string
	CreatedAt       int64
	UpdatedAt       int64
	LastActivityAt  int64
	DeletedAt       *int64
	IsPlayground    bool
	PermissionMode  string
	ConversationSrc string
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
