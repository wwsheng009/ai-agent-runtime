package toolcore

import (
	"encoding/json"
	"testing"
)

func TestFilterSchemaParamsRemovesOptionalProperty(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"file_path":{"type":"string"},
			"symbol":{"type":"string"}
		},
		"required":["file_path","symbol"]
	}`)
	filtered, err := FilterSchemaParams(schema, ParamSymbol)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(filtered, &document); err != nil {
		t.Fatal(err)
	}
	properties := document["properties"].(map[string]any)
	if _, exists := properties["symbol"]; exists {
		t.Fatalf("symbol must be removed: %#v", properties)
	}
	if _, exists := properties["file_path"]; !exists {
		t.Fatalf("file_path must remain: %#v", properties)
	}
	required := document["required"].([]any)
	if len(required) != 1 || required[0] != "file_path" {
		t.Fatalf("required not filtered: %#v", required)
	}
}
