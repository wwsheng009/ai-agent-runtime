package tools

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestWriteTool_EmitsPatchMetadata(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "write-tool-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	path := tmpFile.Name()
	_ = tmpFile.Close()
	defer os.Remove(path)

	tool := NewWriteTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": path,
		"content":   "hello\nworld\n",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	patch, _ := result.Metadata["patch"].(string)
	if !strings.Contains(patch, "+++ b/") || !strings.Contains(patch, "hello") {
		t.Fatalf("expected unified diff patch metadata, got %q", patch)
	}
}

func TestEditTool_EmitsPatchMetadata(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "edit-tool-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	path := tmpFile.Name()
	if _, err := tmpFile.WriteString("line1\nline2\n"); err != nil {
		t.Fatal(err)
	}
	_ = tmpFile.Close()
	defer os.Remove(path)

	tool := NewEditTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  path,
		"old_string": "line2",
		"new_string": "LINE2",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	patch, _ := result.Metadata["patch"].(string)
	if !strings.Contains(patch, "-line2") || !strings.Contains(patch, "+LINE2") {
		t.Fatalf("expected edit diff patch metadata, got %q", patch)
	}
}

func TestWriteTool_EmitsMutatedPaths(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "write-tool-mutation-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	path := tmpFile.Name()
	_ = tmpFile.Close()
	defer os.Remove(path)

	tool := NewWriteTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": path,
		"content":   "mutation\n",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	raw, ok := result.Metadata["mutated_paths"]
	if !ok {
		t.Fatalf("expected mutated_paths metadata, got %#v", result.Metadata)
	}
	paths, ok := raw.([]string)
	if !ok {
		rawList, ok := raw.([]interface{})
		if !ok {
			t.Fatalf("expected mutated_paths slice, got %#v", raw)
		}
		paths = make([]string, 0, len(rawList))
		for _, item := range rawList {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				paths = append(paths, text)
			}
		}
	}
	if len(paths) == 0 {
		t.Fatalf("expected mutated_paths metadata, got %#v", raw)
	}
}

func TestWriteTool_RejectsTruncatedArguments(t *testing.T) {
	tool := NewWriteTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"_raw":         `{"file_path":"E:\\projects\\ai\\ai-agent-runtime\\backend\\out.txt","content":"hello`,
		"_parse_error": "unexpected end of JSON input",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Fatal("expected truncated arguments to fail")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "截断") {
		t.Fatalf("expected truncated-arguments error, got %#v", result.Error)
	}
}

func TestEditTool_RejectsTruncatedArguments(t *testing.T) {
	tool := NewEditTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"_raw":         `{"file_path":"E:\\projects\\ai\\ai-agent-runtime\\backend\\out.txt","old_string":"hello`,
		"_parse_error": "unexpected end of JSON input",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Fatal("expected truncated arguments to fail")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "截断") {
		t.Fatalf("expected truncated-arguments error, got %#v", result.Error)
	}
}

func TestMultieditTool_RejectsTruncatedArguments(t *testing.T) {
	tool := NewMultieditTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"_raw":         `{"file_path":"E:\\projects\\ai\\ai-agent-runtime\\backend\\out.txt","edits":[{"old_string":"hello"`,
		"_parse_error": "unexpected end of JSON input",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Fatal("expected truncated arguments to fail")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "截断") {
		t.Fatalf("expected truncated-arguments error, got %#v", result.Error)
	}
}

func TestWriteTool_RejectsOversizedContent(t *testing.T) {
	tool := NewWriteTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": os.TempDir() + "\\write-oversized.txt",
		"content":   strings.Repeat("A", maxInlineFileMutationFieldBytes+1),
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Fatal("expected oversized content to fail")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "参数过大") {
		t.Fatalf("expected oversized-content error, got %#v", result.Error)
	}
}

func TestEditTool_RejectsOversizedPayload(t *testing.T) {
	tool := NewEditTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  os.TempDir() + "\\edit-oversized.txt",
		"old_string": strings.Repeat("A", maxInlineFileMutationFieldBytes+1),
		"new_string": "B",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Fatal("expected oversized edit payload to fail")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "参数过大") {
		t.Fatalf("expected oversized-edit error, got %#v", result.Error)
	}
}

func TestMultieditTool_RejectsOversizedPayload(t *testing.T) {
	tool := NewMultieditTool()
	half := maxInlineFileMutationPayloadBytes/2 + 128
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": os.TempDir() + "\\multiedit-oversized.txt",
		"edits": []interface{}{
			map[string]interface{}{
				"old_string": strings.Repeat("A", half),
				"new_string": "B",
			},
			map[string]interface{}{
				"old_string": strings.Repeat("C", half),
				"new_string": "D",
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Fatal("expected oversized multiedit payload to fail")
	}
	if result.Error == nil || (!strings.Contains(result.Error.Error(), "参数过大") && !strings.Contains(result.Error.Error(), "总大小过大")) {
		t.Fatalf("expected oversized-multiedit error, got %#v", result.Error)
	}
}

func TestWriteTool_DescriptionGuidesChunkedWrites(t *testing.T) {
	tool := NewWriteTool()

	desc := tool.Description()
	if !strings.Contains(desc, "拆分") || !strings.Contains(desc, "截断") {
		t.Fatalf("expected write tool description to guide chunked writes, got %q", desc)
	}

	params := tool.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties in schema, got %#v", params)
	}
	contentSchema, ok := props["content"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected content schema in properties, got %#v", props)
	}
	contentDesc, _ := contentSchema["description"].(string)
	if !strings.Contains(contentDesc, "拆分") || !strings.Contains(contentDesc, "截断") {
		t.Fatalf("expected content description to guide chunked writes, got %q", contentDesc)
	}
}

func TestEditTool_DescriptionGuidesChunkedWrites(t *testing.T) {
	tool := NewEditTool()

	desc := tool.Description()
	if !strings.Contains(desc, "拆分") || !strings.Contains(desc, "截断") {
		t.Fatalf("expected edit tool description to guide chunked writes, got %q", desc)
	}

	params := tool.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties in schema, got %#v", params)
	}
	oldSchema, ok := props["old_string"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected old_string schema in properties, got %#v", props)
	}
	oldDesc, _ := oldSchema["description"].(string)
	if !strings.Contains(oldDesc, "拆分") || !strings.Contains(oldDesc, "截断") {
		t.Fatalf("expected old_string description to guide chunked writes, got %q", oldDesc)
	}
	newSchema, ok := props["new_string"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected new_string schema in properties, got %#v", props)
	}
	newDesc, _ := newSchema["description"].(string)
	if !strings.Contains(newDesc, "拆分") || !strings.Contains(newDesc, "逐步") {
		t.Fatalf("expected new_string description to guide chunked writes, got %q", newDesc)
	}
}

func TestMultieditTool_DescriptionGuidesChunkedWrites(t *testing.T) {
	tool := NewMultieditTool()

	desc := tool.Description()
	if !strings.Contains(desc, "拆分") || !strings.Contains(desc, "截断") {
		t.Fatalf("expected multiedit tool description to guide chunked writes, got %q", desc)
	}

	params := tool.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties in schema, got %#v", params)
	}
	editsSchema, ok := props["edits"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected edits schema in properties, got %#v", props)
	}
	editsDesc, _ := editsSchema["description"].(string)
	if !strings.Contains(editsDesc, "拆分") || !strings.Contains(editsDesc, "截断") {
		t.Fatalf("expected edits description to guide chunked writes, got %q", editsDesc)
	}
}

func TestAppendWriteTool_AppendsChunks(t *testing.T) {
	path := os.TempDir() + "\\append-write-tool.txt"
	_ = os.Remove(path)
	defer os.Remove(path)

	tool := NewAppendWriteTool()
	first, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":      path,
		"content":        "第一段",
		"truncate_first": true,
	})
	if err != nil {
		t.Fatalf("first Execute returned error: %v", err)
	}
	if !first.Success {
		t.Fatalf("expected first chunk success, got error: %v", first.Error)
	}

	second, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": path,
		"content":   "\n第二段",
	})
	if err != nil {
		t.Fatalf("second Execute returned error: %v", err)
	}
	if !second.Success {
		t.Fatalf("expected second chunk success, got error: %v", second.Error)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "第一段\n第二段" {
		t.Fatalf("unexpected appended content: %q", string(data))
	}
	if got := second.Metadata["transport_backend"]; got != "local_filetransport" {
		t.Fatalf("expected transport_backend metadata, got %#v", got)
	}
}

func TestApplyPatchTool_DefinitionMetadata_AdvertisesFreeformGrammar(t *testing.T) {
	tool := NewApplyPatchTool()

	metadata := tool.DefinitionMetadata()
	freeform, ok := metadata["freeform"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected freeform metadata, got %#v", metadata)
	}
	if got := freeform["syntax"]; got != "lark" {
		t.Fatalf("expected freeform syntax=lark, got %#v", got)
	}
	if definition, _ := freeform["definition"].(string); !strings.Contains(definition, "*** Begin Patch") {
		t.Fatalf("expected apply_patch grammar definition, got %q", definition)
	}
}
