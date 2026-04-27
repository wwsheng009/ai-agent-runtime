package tools

import (
	"strings"
	"testing"
)

func TestSourcegraphTool_DescriptionGuidesQuerySplitting(t *testing.T) {
	tool := NewSourcegraphTool()

	desc := tool.Description()
	if !strings.Contains(desc, "拆分") || !strings.Contains(desc, "每次只聚焦一个搜索目标") {
		t.Fatalf("expected sourcegraph description to guide query splitting, got %q", desc)
	}

	params := tool.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties in schema, got %#v", params)
	}
	querySchema, ok := props["query"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected query schema in properties, got %#v", props)
	}
	queryDesc, _ := querySchema["description"].(string)
	if !strings.Contains(queryDesc, "拆分") || !strings.Contains(queryDesc, "每次只聚焦一个搜索目标") {
		t.Fatalf("expected query description to guide query splitting, got %q", queryDesc)
	}
}
