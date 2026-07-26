package toolkit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// SearchTool is a meta-tool that ranks catalog entries for the model.
// Hosts inject it into the model surface when the catalog is large; the tool
// itself always ShouldList=true so it stays discoverable once projected.
type SearchTool struct {
	*BaseTool
	index ToolSearchIndex
}

// NewSearchTool builds a search_tool bound to the given index.
// index may be nil; Execute then returns an empty ready snapshot.
func NewSearchTool(index ToolSearchIndex) *SearchTool {
	return &SearchTool{
		BaseTool: NewBaseTool(
			ToolSearchName,
			"Search the available tool catalog by keyword. Use when a needed capability is not in the currently listed tools. Returns matching tool names, descriptions, scores, and parameter schemas so you can call them next.",
			"1.0.0",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Keyword or short phrase describing the capability to find (tool name fragments and descriptions are matched).",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum results to return (default 8, max 25).",
						"minimum":     1,
						"maximum":     25,
					},
				},
				"required":             []string{"query"},
				"additionalProperties": false,
			},
			true,
		),
		index: index,
	}
}

// DefinitionMetadata marks search_tool as always-listed core surface.
func (t *SearchTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		MetaCoreTool:   true,
		MetaShouldList: true,
		"source":       "harness",
		"kind":         "meta_search",
	}
}

// ShouldList always keeps search_tool visible once the host injects it.
func (t *SearchTool) ShouldList(ctx ListToolsContext) bool {
	return true
}

// SetIndex replaces the backing catalog index.
func (t *SearchTool) SetIndex(index ToolSearchIndex) {
	if t == nil {
		return
	}
	t.index = index
}

// Index returns the current search index.
func (t *SearchTool) Index() ToolSearchIndex {
	if t == nil {
		return nil
	}
	return t.index
}

// Execute runs a catalog search and returns a structured JSON snapshot.
func (t *SearchTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	_ = ctx
	query := ""
	limit := 8
	if params != nil {
		if raw, ok := params["query"]; ok {
			query = strings.TrimSpace(fmt.Sprint(raw))
		}
		if raw, ok := params["limit"]; ok {
			switch typed := raw.(type) {
			case int:
				limit = typed
			case int32:
				limit = int(typed)
			case int64:
				limit = int(typed)
			case float64:
				limit = int(typed)
			case float32:
				limit = int(typed)
			case json.Number:
				if n, err := typed.Int64(); err == nil {
					limit = int(n)
				}
			case string:
				var n int
				if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &n); err == nil {
					limit = n
				}
			}
		}
	}
	if limit <= 0 {
		limit = 8
	}
	if limit > 25 {
		limit = 25
	}
	if strings.TrimSpace(query) == "" {
		return &ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Content:    "search_tool requires a non-empty query",
			Error:      fmt.Errorf("search_tool requires a non-empty query"),
			Metadata: map[string]interface{}{
				toolresult.MetadataOutcomeKey: toolresult.OutcomeFailed,
			},
		}, fmt.Errorf("search_tool requires a non-empty query")
	}

	var snapshot SearchSnapshot
	if t != nil && t.index != nil {
		snapshot = t.index.SearchSnapshot(query, limit)
	} else {
		snapshot = SearchSnapshot{
			Query:   query,
			IsReady: true,
		}
	}

	payload, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return nil, err
	}
	meta := map[string]interface{}{
		toolresult.MetadataOutcomeKey: toolresult.OutcomeSuccess,
		"query":                       snapshot.Query,
		"result_count":                len(snapshot.Results),
		"total_tools":                 snapshot.TotalTools,
		"total_hidden_tools":          snapshot.TotalHiddenTools,
		"is_ready":                    snapshot.IsReady,
	}
	if len(snapshot.Results) == 0 {
		meta[toolresult.MetadataEmptyResultKey] = true
		meta[toolresult.MetadataOutcomeKey] = toolresult.OutcomeEmpty
		meta[toolresult.MetadataNextActionKey] = "No tools matched the query. Broaden keywords (capability verbs, product names) or list without relying on an exact tool name."
	}
	return &ToolResult{
		Success:    true,
		OutputKind: toolresult.KindStructured,
		Content:    string(payload),
		Metadata:   meta,
	}, nil
}

// SearchToolDefinition returns the model-facing Tool schema for search_tool.
func SearchToolDefinition() map[string]interface{} {
	tool := NewSearchTool(nil)
	return map[string]interface{}{
		"name":        tool.Name(),
		"description": tool.Description(),
		"parameters":  tool.Parameters(),
		"metadata":    tool.DefinitionMetadata(),
	}
}
