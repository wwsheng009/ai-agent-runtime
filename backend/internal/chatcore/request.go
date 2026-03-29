package chatcore

import "github.com/ai-gateway/ai-agent-runtime/internal/types"

// ChatRequest is the transport-neutral request contract shared by chat entrypoints.
type ChatRequest struct {
	Prompt          string                 `json:"prompt"`
	Messages        []types.Message        `json:"messages,omitempty"`
	History         []types.Message        `json:"history,omitempty"`
	Context         map[string]interface{} `json:"context,omitempty"`
	Options         map[string]interface{} `json:"options,omitempty"`
	Metadata        types.Metadata         `json:"metadata,omitempty"`
	Provider        string                 `json:"provider,omitempty"`
	Model           string                 `json:"model,omitempty"`
	ReasoningEffort string                 `json:"reasoning_effort,omitempty"`
	Thinking        *types.ThinkingConfig  `json:"thinking,omitempty"`
	Profile         string                 `json:"profile,omitempty"`
	Agent           string                 `json:"agent,omitempty"`
	WorkspacePath   string                 `json:"workspace_path,omitempty"`
	TeamID          string                 `json:"team_id,omitempty"`
	TaskID          string                 `json:"task_id,omitempty"`
	Stream          bool                   `json:"stream,omitempty"`
}

// NewChatRequest creates a shared chat-core request.
func NewChatRequest(prompt string) *ChatRequest {
	return &ChatRequest{
		Prompt:   prompt,
		Messages: make([]types.Message, 0),
		History:  make([]types.Message, 0),
		Context:  make(map[string]interface{}),
		Options:  make(map[string]interface{}),
		Metadata: types.NewMetadata(),
	}
}

// Clone copies the request for adapter-specific mutation.
func (r *ChatRequest) Clone() *ChatRequest {
	if r == nil {
		return nil
	}

	cloned := &ChatRequest{
		Prompt:          r.Prompt,
		Provider:        r.Provider,
		Model:           r.Model,
		ReasoningEffort: r.ReasoningEffort,
		Thinking:        types.CloneThinkingConfig(r.Thinking),
		Profile:         r.Profile,
		Agent:           r.Agent,
		WorkspacePath:   r.WorkspacePath,
		TeamID:          r.TeamID,
		TaskID:          r.TaskID,
		Stream:          r.Stream,
		Metadata:        r.Metadata.Clone(),
	}

	if len(r.Messages) > 0 {
		cloned.Messages = make([]types.Message, len(r.Messages))
		for i, msg := range r.Messages {
			cloned.Messages[i] = *msg.Clone()
		}
	} else {
		cloned.Messages = make([]types.Message, 0)
	}

	if len(r.History) > 0 {
		cloned.History = make([]types.Message, len(r.History))
		for i, msg := range r.History {
			cloned.History[i] = *msg.Clone()
		}
	} else {
		cloned.History = make([]types.Message, 0)
	}

	if len(r.Context) > 0 {
		cloned.Context = make(map[string]interface{}, len(r.Context))
		for k, v := range r.Context {
			cloned.Context[k] = v
		}
	} else {
		cloned.Context = make(map[string]interface{})
	}

	if len(r.Options) > 0 {
		cloned.Options = make(map[string]interface{}, len(r.Options))
		for k, v := range r.Options {
			cloned.Options[k] = v
		}
	} else {
		cloned.Options = make(map[string]interface{})
	}

	return cloned
}
