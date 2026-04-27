package tools

import (
	"strings"
	"testing"
)

func TestViewTool_DescriptionGuidesSingleFileFocus(t *testing.T) {
	tool := NewViewTool()

	desc := tool.Description()
	if !strings.Contains(desc, "拆分") || !strings.Contains(desc, "每次只聚焦一个文件") {
		t.Fatalf("expected view description to guide single-file focus, got %q", desc)
	}

	params := tool.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties in schema, got %#v", params)
	}
	pathSchema, ok := props["file_path"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected file_path schema in properties, got %#v", props)
	}
	pathDesc, _ := pathSchema["description"].(string)
	if !strings.Contains(pathDesc, "拆分") || !strings.Contains(pathDesc, "多个文件") {
		t.Fatalf("expected file_path description to guide single-file focus, got %q", pathDesc)
	}
}
