package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 复现 unsee relay 400 "invalid function call parameters"：
// 工具压缩把 required:[] 变成了 required:null，上游据此拒绝整个请求。
func TestStripToolSchemaAnnotationsKeepsEmptyRequiredAsArray(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "workspace path",
			},
		},
		"required": []string{},
	}

	compacted := stripToolSchemaAnnotations(schema)

	require.Contains(t, compacted, "required")
	encoded, err := json.Marshal(compacted)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"required":[]`)
	require.NotContains(t, string(encoded), `"required":null`)

	properties, ok := compacted["properties"].(map[string]interface{})
	require.True(t, ok)
	pathSchema, ok := properties["path"].(map[string]interface{})
	require.True(t, ok)
	require.NotContains(t, pathSchema, "description")
}

func TestStripToolSchemaAnnotationsKeepsNonEmptyRequired(t *testing.T) {
	schema := map[string]interface{}{
		"type":     "object",
		"required": []string{"a", "b"},
	}

	compacted := stripToolSchemaAnnotations(schema)

	encoded, err := json.Marshal(compacted)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"required":["a","b"]`)
}

func TestStripToolSchemaAnnotationsKeepsNestedEmptyRequiredArray(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"nested": map[string]interface{}{
				"type":     "object",
				"required": []string{},
			},
		},
	}

	compacted := stripToolSchemaAnnotations(schema)

	encoded, err := json.Marshal(compacted)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"required":[]`)
	require.NotContains(t, string(encoded), `"required":null`)
}
