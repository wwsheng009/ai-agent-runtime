package output

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/artifact"
)

// ArtifactWriter 约束 gateway 所需的最小 artifact 能力。
type ArtifactWriter interface {
	Put(ctx context.Context, record artifact.Record) (string, error)
}

// RawToolResult 表示工具执行后的原始输出。
type RawToolResult struct {
	SessionID  string
	ToolName   string
	ToolCallID string
	Step       int
	Content    interface{}
	Error      string
	Metadata   map[string]interface{}
}

// Envelope 是允许进入上下文窗口的压缩信号。
type Envelope struct {
	ToolName    string
	ToolCallID  string
	Summary     string
	Error       string
	ArtifactIDs []string
	Metadata    map[string]interface{}
}

// Reducer 负责把原始输出压成可控的 envelope。
type Reducer interface {
	Name() string
	Reduce(ctx context.Context, input ReducedInput) (*Envelope, bool, error)
}

// ReducedInput 是 reducer 的标准输入。
type ReducedInput struct {
	Raw       RawToolResult
	Text      string
	StoredAt  time.Time
	Artifact  string
	ByteCount int
}

// Gateway 在 tool output 与上下文窗口之间插入治理层。
type Gateway struct {
	store    ArtifactWriter
	reducers []Reducer
}

// NewGateway 创建 output gateway。
func NewGateway(store ArtifactWriter, reducers ...Reducer) *Gateway {
	normalized := make([]Reducer, 0, len(reducers))
	for _, reducer := range reducers {
		if reducer != nil {
			normalized = append(normalized, reducer)
		}
	}
	if len(normalized) == 0 {
		normalized = append(normalized,
			&GoTestJSONReducer{},
			&GitLogReducer{},
			&PlaywrightSnapshotReducer{},
			&JSONReducer{},
			&TableReducer{},
			&LogReducer{},
			NewTextReducer(1200, 16),
		)
	}

	return &Gateway{
		store:    store,
		reducers: normalized,
	}
}

// Process 将工具原始输出归档并压缩成可注入上下文的 envelope。
func (g *Gateway) Process(ctx context.Context, result RawToolResult) (*Envelope, error) {
	envelope := &Envelope{
		ToolName:   result.ToolName,
		ToolCallID: result.ToolCallID,
		Error:      strings.TrimSpace(result.Error),
		Metadata:   cloneMap(result.Metadata),
	}
	if envelope.Metadata == nil {
		envelope.Metadata = map[string]interface{}{}
	}

	text := stringify(result.Content)
	input := ReducedInput{
		Raw:       result,
		Text:      text,
		StoredAt:  time.Now().UTC(),
		ByteCount: len([]byte(text)),
	}
	envelope.Metadata["raw_bytes"] = input.ByteCount

	var processErrs []string
	if g.store != nil && strings.TrimSpace(text) != "" {
		artifactID, err := g.store.Put(ctx, artifact.Record{
			SessionID:  result.SessionID,
			ToolName:   result.ToolName,
			ToolCallID: result.ToolCallID,
			Summary:    preview(text, 400),
			Content:    text,
			Metadata:   cloneMap(result.Metadata),
			CreatedAt:  input.StoredAt,
		})
		if err != nil {
			processErrs = append(processErrs, err.Error())
		} else {
			input.Artifact = artifactID
			envelope.ArtifactIDs = append(envelope.ArtifactIDs, artifactID)
		}
	}

	var handled bool
	for _, reducer := range g.reducers {
		reduced, ok, err := reducer.Reduce(ctx, input)
		if err != nil {
			processErrs = append(processErrs, fmt.Sprintf("%s: %v", reducer.Name(), err))
			continue
		}
		if !ok || reduced == nil {
			continue
		}

		handled = true
		envelope = mergeEnvelope(envelope, reduced)
		envelope.Metadata["reducer"] = reducer.Name()
		break
	}

	if !handled {
		fallback, _, _ := NewTextReducer(1200, 16).Reduce(ctx, input)
		envelope = mergeEnvelope(envelope, fallback)
		envelope.Metadata["reducer"] = "text_truncation"
	}

	if len(processErrs) > 0 {
		envelope.Metadata["gateway_errors"] = processErrs
	}

	return envelope, joinErrors(processErrs)
}

// Render 把 envelope 变成适合放入 tool_result 的文本。
func (e *Envelope) Render() string {
	if e == nil {
		return ""
	}

	parts := make([]string, 0, 3)
	if e.Error != "" {
		parts = append(parts, "Tool execution failed: "+e.Error)
	}
	if strings.TrimSpace(e.Summary) != "" {
		parts = append(parts, strings.TrimSpace(e.Summary))
	}
	if len(e.ArtifactIDs) > 0 {
		parts = append(parts, "artifact_refs: "+strings.Join(e.ArtifactIDs, ", "))
	}

	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func mergeEnvelope(base *Envelope, override *Envelope) *Envelope {
	if base == nil {
		base = &Envelope{}
	}
	if override == nil {
		return base
	}

	if override.ToolName != "" {
		base.ToolName = override.ToolName
	}
	if override.ToolCallID != "" {
		base.ToolCallID = override.ToolCallID
	}
	if override.Summary != "" {
		base.Summary = override.Summary
	}
	if override.Error != "" {
		base.Error = override.Error
	}
	if len(override.ArtifactIDs) > 0 {
		base.ArtifactIDs = append(base.ArtifactIDs, override.ArtifactIDs...)
	}
	base.Metadata = mergeMap(base.Metadata, override.Metadata)

	return base
}

func stringify(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case fmt.Stringer:
		return typed.String()
	default:
		payload, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", typed)
		}
		return string(payload)
	}
}

func cloneMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func mergeMap(left, right map[string]interface{}) map[string]interface{} {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	merged := cloneMap(left)
	if merged == nil {
		merged = map[string]interface{}{}
	}
	for key, value := range right {
		merged[key] = value
	}
	return merged
}

func preview(content string, maxLen int) string {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if maxLen <= 0 || len(content) <= maxLen {
		return content
	}
	if maxLen <= 3 {
		return content[:maxLen]
	}
	return content[:maxLen-3] + "..."
}

func joinErrors(errs []string) error {
	if len(errs) == 0 {
		return nil
	}
	return errors.New(strings.Join(errs, "; "))
}
