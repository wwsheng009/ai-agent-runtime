package dsh

import (
	"encoding/json"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
)

// Header is the DSH Session physical header.
type Header struct {
	Type            string `json:"type"`
	Version         int    `json:"version"`
	ID              string `json:"id"`
	CreatedAt       int64  `json:"createdAt"`
	CWD             string `json:"cwd,omitempty"`
	ParentSession   string `json:"parentSession,omitempty"`
	IsSeeded        bool   `json:"isSeeded"`
	DelegationDepth int    `json:"delegationDepth"`
	AgentPreset     string `json:"agentPreset,omitempty"`
}

// Event is one DSH Session log envelope.
type Event struct {
	Type            string          `json:"type"`
	Seq             int             `json:"seq"`
	Time            int64           `json:"time"`
	Data            json.RawMessage `json:"data"`
	SurfaceOp       json.RawMessage `json:"surfaceOp,omitempty"`
	SourceEventSeqs []int           `json:"sourceEventSeqs,omitempty"`
	Ignorable       bool            `json:"ignorable,omitempty"`
}

// ContentBlock is the subset of DSH content blocks needed for normalization.
type ContentBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Arguments  string          `json:"arguments,omitempty"`
	Attachment json.RawMessage `json:"attachment,omitempty"`
	Offloaded  bool            `json:"offloaded,omitempty"`
}

// SourceInfo is the subset of DSH message source metadata needed for normalization.
type SourceInfo struct {
	Kind     string `json:"kind,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Plugin   string `json:"plugin,omitempty"`
	CallID   string `json:"callId,omitempty"`
}

// Message is the common DSH message representation inside surface events.
type Message struct {
	ID         string         `json:"id"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	Source     SourceInfo     `json:"source"`
	ToolCallID string         `json:"toolCallId,omitempty"`
	IsError    bool           `json:"isError,omitempty"`
}

type userMessageData struct {
	ID      string         `json:"id"`
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
	Source  SourceInfo     `json:"source"`
}

type assistantMessageData struct {
	Turn        int               `json:"turn"`
	Step        int               `json:"step"`
	Message     Message           `json:"message"`
	Usage       json.RawMessage   `json:"usage,omitempty"`
	Stream      []json.RawMessage `json:"stream,omitempty"`
	Interrupted bool              `json:"interrupted,omitempty"`
}

type toolResultData struct {
	Turn    int             `json:"turn"`
	Step    int             `json:"step"`
	Message Message         `json:"message"`
	Error   json.RawMessage `json:"error,omitempty"`
	Meta    json.RawMessage `json:"meta,omitempty"`
}

type titleData struct {
	Title       string `json:"title"`
	MessageSeqs []int  `json:"messageSeqs,omitempty"`
}

type surfaceReplace struct {
	Op       string `json:"op"`
	StartSeq int    `json:"startSeq"`
	EndSeq   int    `json:"endSeq"`
}

// Loaded is one parsed v4 session log.
type Loaded struct {
	Header   Header
	Events   []Event
	Ref      sessionport.SessionRef
	Warnings []string
}

// Imported is one normalized aicli session read from DSH.
type Imported struct {
	Session    *runtimechat.Session
	SourceFile string
	Ref        sessionport.SessionRef
	Warnings   []string
}

// View normalizes the CLI value.
func NormalizeView(value string) (sessionport.SessionView, error) {
	switch sessionport.SessionView(value) {
	case "", sessionport.ViewSurface:
		return sessionport.ViewSurface, nil
	case sessionport.ViewTranscript:
		return sessionport.ViewTranscript, nil
	default:
		return "", &UnsupportedViewError{Value: value}
	}
}

// UnsupportedViewError reports an unknown --view value.
type UnsupportedViewError struct {
	Value string
}

func (e *UnsupportedViewError) Error() string {
	return "不支持的视图: " + e.Value + "（可选 surface|transcript）"
}
