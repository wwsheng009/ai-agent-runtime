package types

import "strings"

const reasoningMetadataKey = "reasoning_details"

// ReasoningVisibility 表示 provider 返回的 reasoning 可见性。
type ReasoningVisibility string

const (
	ReasoningVisibilityNone    ReasoningVisibility = "none"
	ReasoningVisibilitySummary ReasoningVisibility = "summary"
	ReasoningVisibilityFull    ReasoningVisibility = "full"
	ReasoningVisibilityOpaque  ReasoningVisibility = "opaque"
)

// ReasoningBlock 统一表示各协议公开返回的 reasoning 信息。
//
// Summary/Content 用于展示给用户；
// OpaqueState 和 Metadata 用于在多轮对话中保留 provider 需要的续接状态。
type ReasoningBlock struct {
	Provider       string                 `json:"provider,omitempty" yaml:"provider,omitempty"`
	Format         string                 `json:"format,omitempty" yaml:"format,omitempty"`
	Summary        string                 `json:"summary,omitempty" yaml:"summary,omitempty"`
	Content        string                 `json:"content,omitempty" yaml:"content,omitempty"`
	OpaqueState    string                 `json:"opaque_state,omitempty" yaml:"opaque_state,omitempty"`
	ReplayRequired bool                   `json:"replay_required,omitempty" yaml:"replay_required,omitempty"`
	Streamable     bool                   `json:"streamable,omitempty" yaml:"streamable,omitempty"`
	Visibility     ReasoningVisibility    `json:"visibility,omitempty" yaml:"visibility,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// Clone 返回 ReasoningBlock 的副本。
func (r *ReasoningBlock) Clone() *ReasoningBlock {
	if r == nil {
		return nil
	}
	cloned := *r
	if len(r.Metadata) > 0 {
		cloned.Metadata = make(map[string]interface{}, len(r.Metadata))
		for key, value := range r.Metadata {
			cloned.Metadata[key] = value
		}
	}
	return &cloned
}

// DisplayText 返回适合展示给用户的 reasoning 文本。
func (r *ReasoningBlock) DisplayText() string {
	return strings.TrimSpace(r.RawDisplayText())
}

// RawDisplayText 返回保留原始空白字符的 reasoning 文本。
func (r *ReasoningBlock) RawDisplayText() string {
	if r == nil {
		return ""
	}
	if r.Summary != "" {
		return r.Summary
	}
	if r.Content != "" {
		return r.Content
	}
	return ""
}

// ToMap 将 ReasoningBlock 转为可写入 metadata/payload 的 map。
func (r *ReasoningBlock) ToMap() map[string]interface{} {
	if r == nil {
		return nil
	}
	result := map[string]interface{}{}
	if value := strings.TrimSpace(r.Provider); value != "" {
		result["provider"] = value
	}
	if value := strings.TrimSpace(r.Format); value != "" {
		result["format"] = value
	}
	if r.Summary != "" {
		result["summary"] = r.Summary
	}
	if r.Content != "" {
		result["content"] = r.Content
	}
	if value := strings.TrimSpace(r.OpaqueState); value != "" {
		result["opaque_state"] = value
	}
	if r.ReplayRequired {
		result["replay_required"] = true
	}
	if r.Streamable {
		result["streamable"] = true
	}
	if r.Visibility != "" {
		result["visibility"] = string(r.Visibility)
	}
	if len(r.Metadata) > 0 {
		meta := make(map[string]interface{}, len(r.Metadata))
		for key, value := range r.Metadata {
			meta[key] = value
		}
		result["metadata"] = meta
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// ReasoningBlockFromMap 从 map 解码统一 reasoning 信息。
func ReasoningBlockFromMap(value interface{}) *ReasoningBlock {
	raw, ok := value.(map[string]interface{})
	if !ok || len(raw) == 0 {
		return nil
	}
	block := &ReasoningBlock{}
	if provider, ok := raw["provider"].(string); ok {
		block.Provider = provider
	}
	if format, ok := raw["format"].(string); ok {
		block.Format = format
	}
	if summary, ok := raw["summary"].(string); ok {
		block.Summary = summary
	}
	if content, ok := raw["content"].(string); ok {
		block.Content = content
	}
	if opaque, ok := raw["opaque_state"].(string); ok {
		block.OpaqueState = opaque
	}
	if replay, ok := raw["replay_required"].(bool); ok {
		block.ReplayRequired = replay
	}
	if streamable, ok := raw["streamable"].(bool); ok {
		block.Streamable = streamable
	}
	if visibility, ok := raw["visibility"].(string); ok {
		block.Visibility = ReasoningVisibility(strings.TrimSpace(visibility))
	}
	if metadata, ok := raw["metadata"].(map[string]interface{}); ok && len(metadata) > 0 {
		block.Metadata = make(map[string]interface{}, len(metadata))
		for key, item := range metadata {
			block.Metadata[key] = item
		}
	}
	if block.RawDisplayText() == "" && strings.TrimSpace(block.OpaqueState) == "" && len(block.Metadata) == 0 {
		return nil
	}
	return block
}

// SetReasoningBlock 将统一 reasoning 信息写入消息 metadata。
func SetReasoningBlock(metadata Metadata, block *ReasoningBlock) {
	if metadata == nil {
		return
	}
	if block == nil {
		delete(metadata, reasoningMetadataKey)
		return
	}
	if encoded := block.ToMap(); len(encoded) > 0 {
		metadata[reasoningMetadataKey] = encoded
	}
}

// GetReasoningBlock 从 metadata 读取统一 reasoning 信息。
func GetReasoningBlock(metadata Metadata) *ReasoningBlock {
	if len(metadata) == 0 {
		return nil
	}
	return ReasoningBlockFromMap(metadata[reasoningMetadataKey])
}
