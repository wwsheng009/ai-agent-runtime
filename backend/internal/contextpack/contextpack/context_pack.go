package contextpack

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspace"
)

// SessionSnapshot is a transport-friendly view of session state used by context packing.
type SessionSnapshot struct {
	ID             string
	UserID         string
	State          string
	Tags           []string
	Context        map[string]interface{}
	TotalTurns     int
	LastAgent      string
	LastSkill      string
	LastModel      string
	RecentMessages []types.Message
	UpdatedAt      time.Time
}

// Input 上下文构建输入
type Input struct {
	Prompt        string
	Messages      []types.Message
	Session       *SessionSnapshot
	Profile       map[string]interface{}
	Workspace     *workspace.WorkspaceContext
	WorkspacePath string
	TeamID        string
	TaskID        string
}

// Provider 上下文提供者
type Provider interface {
	Name() string
	Build(ctx context.Context, input *Input) (map[string]interface{}, error)
}

// Builder 上下文构建器
type Builder struct {
	providers []Provider
}

// NewBuilder 创建上下文构建器
func NewBuilder() *Builder {
	return &Builder{providers: []Provider{}}
}

// AddProvider 添加 Provider
func (b *Builder) AddProvider(provider Provider) {
	if b == nil || provider == nil {
		return
	}
	b.providers = append(b.providers, provider)
}

// Build 构建上下文包
func (b *Builder) Build(ctx context.Context, input *Input) (map[string]interface{}, []string) {
	if b == nil {
		return nil, nil
	}
	pack := make(map[string]interface{})
	warnings := make([]string, 0)

	for _, provider := range b.providers {
		payload, err := provider.Build(ctx, input)
		if err != nil {
			warnings = append(warnings, provider.Name()+": "+err.Error())
			continue
		}
		if payload != nil {
			pack[provider.Name()] = payload
		}
	}

	if len(warnings) > 0 {
		pack["_warnings"] = warnings
	}
	return pack, warnings
}

// ProfileProvider profile 上下文提供者。
type ProfileProvider struct{}

// NewProfileProvider 创建 ProfileProvider。
func NewProfileProvider() *ProfileProvider {
	return &ProfileProvider{}
}

func (p *ProfileProvider) Name() string { return "profile" }

func (p *ProfileProvider) Build(ctx context.Context, input *Input) (map[string]interface{}, error) {
	if input == nil || len(input.Profile) == 0 {
		return nil, nil
	}
	return cloneMap(input.Profile), nil
}

// WorkspaceProvider 工作区上下文提供者
type WorkspaceProvider struct{}

// NewWorkspaceProvider 创建 WorkspaceProvider
func NewWorkspaceProvider() *WorkspaceProvider {
	return &WorkspaceProvider{}
}

func (p *WorkspaceProvider) Name() string { return "workspace" }

func (p *WorkspaceProvider) Build(ctx context.Context, input *Input) (map[string]interface{}, error) {
	if input == nil || input.Workspace == nil {
		return nil, nil
	}
	return map[string]interface{}{
		"path":       input.WorkspacePath,
		"query":      input.Workspace.Query,
		"files":      input.Workspace.Files,
		"symbols":    input.Workspace.Symbols,
		"references": input.Workspace.References,
		"chunks":     input.Workspace.Chunks,
		"summary":    input.Workspace.Summary,
	}, nil
}

// SessionProvider 会话上下文提供者
type SessionProvider struct {
	MaxMessages int
}

// NewSessionProvider 创建 SessionProvider
func NewSessionProvider(maxMessages int) *SessionProvider {
	return &SessionProvider{MaxMessages: maxMessages}
}

func (p *SessionProvider) Name() string { return "session" }

func (p *SessionProvider) Build(ctx context.Context, input *Input) (map[string]interface{}, error) {
	if input == nil || input.Session == nil {
		return nil, nil
	}
	maxMessages := p.MaxMessages
	if maxMessages <= 0 {
		maxMessages = 10
	}
	recent := input.Session.RecentMessages
	if len(recent) > maxMessages {
		recent = recent[len(recent)-maxMessages:]
	}

	return map[string]interface{}{
		"id":              input.Session.ID,
		"user_id":         input.Session.UserID,
		"state":           input.Session.State,
		"tags":            append([]string(nil), input.Session.Tags...),
		"context":         cloneMap(input.Session.Context),
		"total_turns":     input.Session.TotalTurns,
		"last_agent":      input.Session.LastAgent,
		"last_skill":      input.Session.LastSkill,
		"last_model":      input.Session.LastModel,
		"recent_messages": recent,
		"updated_at":      input.Session.UpdatedAt.Format(time.RFC3339),
	}, nil
}

// Reduce shrinks a context pack into a prompt-safe summary payload.
func Reduce(pack map[string]interface{}) map[string]interface{} {
	if len(pack) == 0 {
		return nil
	}

	reduced := map[string]interface{}{}

	if profile, ok := pack["profile"].(map[string]interface{}); ok {
		if summary := reduceProfile(profile); len(summary) > 0 {
			reduced["profile"] = summary
		}
	}

	if workspacePack, ok := pack["workspace"].(map[string]interface{}); ok {
		workspaceSummary := map[string]interface{}{}
		if summary, ok := workspacePack["summary"].(string); ok && strings.TrimSpace(summary) != "" {
			workspaceSummary["summary"] = strings.TrimSpace(summary)
		}
		if query, ok := workspacePack["query"].(string); ok && strings.TrimSpace(query) != "" {
			workspaceSummary["query"] = strings.TrimSpace(query)
		}
		if files := toStringSlice(workspacePack["files"]); len(files) > 0 {
			workspaceSummary["files"] = limitStringSlice(files, 5)
		}
		if len(workspaceSummary) > 0 {
			reduced["workspace"] = workspaceSummary
		}
	}

	if sessionPack, ok := pack["session"].(map[string]interface{}); ok {
		sessionSummary := map[string]interface{}{}
		copyString(sessionSummary, "id", sessionPack["id"])
		copyString(sessionSummary, "user_id", sessionPack["user_id"])
		copyString(sessionSummary, "state", sessionPack["state"])
		copyString(sessionSummary, "last_agent", sessionPack["last_agent"])
		copyString(sessionSummary, "last_skill", sessionPack["last_skill"])
		copyString(sessionSummary, "last_model", sessionPack["last_model"])
		if tags := toStringSlice(sessionPack["tags"]); len(tags) > 0 {
			sessionSummary["tags"] = limitStringSlice(tags, 5)
		}
		if totalTurns, ok := toInt(sessionPack["total_turns"]); ok {
			sessionSummary["total_turns"] = totalTurns
		}
		if len(sessionSummary) > 0 {
			reduced["session"] = sessionSummary
		}
	}

	if teamPack, ok := pack["team"].(map[string]interface{}); ok {
		teamSummary := map[string]interface{}{}
		copyString(teamSummary, "team_id", teamPack["team_id"])
		copyString(teamSummary, "task_id", teamPack["task_id"])
		if summary, ok := teamPack["summary"].(string); ok && strings.TrimSpace(summary) != "" {
			teamSummary["summary"] = summarizeString(summary, 300)
		}
		if taskCount, ok := toInt(teamPack["task_count"]); ok {
			teamSummary["task_count"] = taskCount
		}
		if mailCount, ok := toInt(teamPack["mail_count"]); ok {
			teamSummary["mail_count"] = mailCount
		}
		if len(teamSummary) > 0 {
			reduced["team"] = teamSummary
		}
	}

	if warnings, ok := pack["_warnings"]; ok {
		reduced["warnings"] = warnings
	}

	if len(reduced) == 0 {
		return nil
	}
	return reduced
}

func reduceProfile(profile map[string]interface{}) map[string]interface{} {
	if len(profile) == 0 {
		return nil
	}

	summary := map[string]interface{}{}
	copyString(summary, "reference", profile["reference"])
	copyString(summary, "name", profile["name"])
	copyString(summary, "agent", profile["agent"])
	copyString(summary, "root", profile["root"])
	copyString(summary, "memory_path", profile["memory_path"])
	copyString(summary, "notes_path", profile["notes_path"])
	if resources, ok := profile["resources"].(map[string]interface{}); ok {
		if reduced := reduceProfileResources(resources); len(reduced) > 0 {
			summary["resources"] = reduced
		}
	}
	return summary
}

func reduceProfileResources(resources map[string]interface{}) map[string]interface{} {
	if len(resources) == 0 {
		return nil
	}

	reduced := map[string]interface{}{}
	for key, raw := range resources {
		item, ok := raw.(map[string]interface{})
		if !ok || len(item) == 0 {
			continue
		}
		summary := map[string]interface{}{}
		copyString(summary, "path", item["path"])
		copyString(summary, "format", item["format"])
		if content, ok := item["content"].(string); ok && strings.TrimSpace(content) != "" {
			summary["content"] = summarizeString(content, 300)
		}
		if truncated, ok := toBool(item["truncated"]); ok && truncated {
			summary["truncated"] = true
		}
		if len(summary) > 0 {
			reduced[key] = summary
		}
	}

	if len(reduced) == 0 {
		return nil
	}
	return reduced
}

func summarizeString(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" || limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func toStringSlice(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	default:
		return nil
	}
}

func limitStringSlice(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	return values[:limit]
}

func copyString(target map[string]interface{}, key string, value interface{}) {
	if target == nil {
		return
	}
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		target[key] = strings.TrimSpace(text)
	}
}

func toInt(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func toBool(value interface{}) (bool, bool) {
	typed, ok := value.(bool)
	return typed, ok
}

func cloneMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = cloneValue(value)
	}
	return cloned
}

func cloneValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneMap(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneValue(item)
		}
		return cloned
	case []string:
		cloned := make([]string, len(typed))
		copy(cloned, typed)
		return cloned
	default:
		return typed
	}
}
