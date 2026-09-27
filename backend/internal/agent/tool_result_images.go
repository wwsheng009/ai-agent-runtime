package agent

import (
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// appendImagePassthroughMessages 把工具结果里声明的图片直通附件转成一条紧随该批
// tool_result 之后的 user 消息：provider 协议只在 user 角色渲染 image block，工具
// 消息本身只能携带文本；放在批次末尾也避免破坏 tool_calls → tool_result 的顺序。
func (b *MessageBuilder) appendImagePassthroughMessages(results []ToolResultPayload) {
	paths, notes := collectImagePassthroughs(results)
	if len(paths) == 0 {
		return
	}

	prompt := "工具读取的图片已作为图像输入附加：" + strings.Join(paths, "、")
	if len(notes) > 0 {
		prompt += "。" + strings.Join(notes, "；")
	}
	prompt += "。请结合图片内容继续。"

	message, err := llm.NewUserPromptMessageWithImages(prompt, paths)
	if err != nil || message == nil {
		// 单张图不可读不应吞掉整批工具结果：退化为纯文本提示。
		message = types.NewUserMessage(prompt)
	}
	b.history = append(b.history, *message)
}

// collectImagePassthroughs 收集直通图片路径与说明：顶层元数据优先，同时遍历
// files[] 批量结果里的 items[]。此前注入侧只读顶层元数据，批量读图片会整体
// 丢失图像输入（analysis §3.11）。
func collectImagePassthroughs(results []ToolResultPayload) ([]string, []string) {
	if len(results) == 0 {
		return nil, nil
	}
	paths := make([]string, 0, 2)
	notes := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	collectPath := func(path, note string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, err := os.Stat(path); err != nil {
			return
		}
		if _, duplicate := seen[path]; duplicate {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
		if note != "" {
			notes = append(notes, note)
		}
	}
	collect := func(meta map[string]interface{}) {
		if path, note, ok := toolresult.ImagePassthroughFromMetadata(meta); ok {
			collectPath(path, note)
		}
		// 一个结果可以声明多张图（notebook 的多个输出、提升的批量 items）：
		// 主路径之外的补充清单同样要附加。
		for _, extra := range toolresult.ImagePassthroughPathsFromMetadata(meta) {
			collectPath(extra, "")
		}
	}
	for _, result := range results {
		meta := map[string]interface{}(result.Metadata)
		walkImagePassthroughMetadata(meta, 0, collect)
	}
	if len(paths) == 0 {
		return nil, nil
	}
	// 顺序即声明顺序（顶层优先、随后 items）：paths/notes 保持同一遍历
	// 次序，消费者不应再按下标把它们当作严格配对（note 可缺省）。
	return paths, notes
}

// imagePassthroughMetadataDepth bounds the nested walk: the runtime wraps
// tool-authored metadata under tool_metadata, and message history can nest the
// same map one hop deeper after a JSON round trip.
const imagePassthroughMetadataDepth = 3

// walkImagePassthroughMetadata collects image declarations from one tool result
// metadata map, including the real runtime shape where a files[] batch lives at
// tool_metadata.items instead of top-level items. The flat walk alone dropped
// every batch image attachment on the production path (2026-09-27 review).
func walkImagePassthroughMetadata(meta map[string]interface{}, depth int, collect func(map[string]interface{})) {
	if len(meta) == 0 || depth > imagePassthroughMetadataDepth {
		return
	}
	collect(meta)
	collectImagePassthroughsFromItems(meta["items"], collect)
	if nested, ok := meta["tool_metadata"].(map[string]interface{}); ok && len(nested) > 0 {
		walkImagePassthroughMetadata(nested, depth+1, collect)
	}
}

// collectImagePassthroughsFromItems 兼容 items 在 JSON 往返前后的两种形状：
// []map[string]interface{}（内存态）与 []interface{}（解码态）。
func collectImagePassthroughsFromItems(raw interface{}, collect func(map[string]interface{})) {
	switch items := raw.(type) {
	case []map[string]interface{}:
		for _, item := range items {
			if len(item) > 0 {
				collect(item)
			}
		}
	case []interface{}:
		for _, item := range items {
			if meta, ok := item.(map[string]interface{}); ok && len(meta) > 0 {
				collect(meta)
			}
		}
	}
}
