package toolargs

import "testing"

func TestNormalizeUnwrapsRawJSON(t *testing.T) {
	got := Normalize(map[string]interface{}{
		"_raw": `{"file_path":"E:/project/file.go","old_string":"old","new_string":"new"}`,
	})
	if got["file_path"] != "E:/project/file.go" || got["old_string"] != "old" || got["new_string"] != "new" {
		t.Fatalf("unexpected normalized args: %#v", got)
	}
	if _, ok := got["_raw"]; ok {
		t.Fatalf("expected _raw to be removed after successful unwrap: %#v", got)
	}
}

func TestNormalizeUnwrapsNestedRawJSON(t *testing.T) {
	got := Normalize(map[string]interface{}{
		"_raw": `{"_raw":"{\"command\":\"git status\"}"}`,
	})
	if got["command"] != "git status" {
		t.Fatalf("unexpected normalized args: %#v", got)
	}
}

func TestNormalizeUnwrapsJSONStringContainingJSONObject(t *testing.T) {
	got := Normalize(map[string]interface{}{
		"_raw": `"{\"command\":\"git status\"}"`,
	})
	if got["command"] != "git status" {
		t.Fatalf("unexpected normalized args: %#v", got)
	}
}

func TestNormalizePreservesInvalidRawWithParseError(t *testing.T) {
	args := map[string]interface{}{
		"_raw":         `{"command":"git status"`,
		"_parse_error": "unexpected end of JSON input",
	}
	got := Normalize(args)
	if got["_raw"] != args["_raw"] || got["_parse_error"] != args["_parse_error"] {
		t.Fatalf("expected invalid raw args to be preserved, got %#v", got)
	}
}

func TestNormalizeDoesNotOverwriteExplicitArgs(t *testing.T) {
	got := Normalize(map[string]interface{}{
		"command": "git status",
		"_raw":    `{"command":"git diff"}`,
	})
	if got["command"] != "git status" || got["_raw"] == nil {
		t.Fatalf("expected explicit args to be preserved, got %#v", got)
	}
}

func TestDecodeJSONCompletesMissingStructuralDelimiters(t *testing.T) {
	got := DecodeJSON(`{"commands":[{"command":"git status"},{"command":"git diff"}`)
	commands, ok := got["commands"].([]interface{})
	if !ok || len(commands) != 2 {
		t.Fatalf("expected repaired command array, got %#v", got)
	}
	if commands[1].(map[string]interface{})["command"] != "git diff" {
		t.Fatalf("unexpected repaired arguments: %#v", got)
	}
	if _, exists := got["_parse_error"]; exists {
		t.Fatalf("did not expect parse error after structural repair: %#v", got)
	}
}

func TestDecodeJSONDoesNotRepairTruncatedString(t *testing.T) {
	raw := `{"file_path":"E:/project/out.txt","content":"partial`
	got := DecodeJSON(raw)
	if got["_raw"] != raw {
		t.Fatalf("expected original truncated input, got %#v", got)
	}
	if got["_parse_error"] == nil {
		t.Fatalf("expected parse error for truncated string, got %#v", got)
	}
	if _, exists := got["content"]; exists {
		t.Fatalf("truncated content must not become executable args: %#v", got)
	}
}

func TestDecodeJSONUnwrapsProviderRawEnvelope(t *testing.T) {
	got := DecodeJSON(`{"_raw":"{\"cmd\":\"git status\"}"}`)
	if got["cmd"] != "git status" {
		t.Fatalf("expected nested raw arguments to be unwrapped, got %#v", got)
	}
}

func TestBindFreeformBindsSoleRequiredStringField(t *testing.T) {
	raw := "*** Begin Patch\n*** End Patch"
	got := BindFreeform(DecodeFreeform(raw), map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"patch": map[string]interface{}{"type": "string"},
		},
		"required": []string{"patch"},
	})
	if got["patch"] != raw || got["_raw"] != nil || got["_parse_error"] != nil {
		t.Fatalf("expected raw input to bind cleanly to patch, got %#v", got)
	}
}

func TestBindFreeformRejectsAmbiguousOrParsedInputs(t *testing.T) {
	args := map[string]interface{}{"_raw": "payload", "_parse_error": "invalid JSON"}
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"first":  map[string]interface{}{"type": "string"},
			"second": map[string]interface{}{"type": "string"},
		},
		"required": []interface{}{"first", "second"},
	}
	if got := BindFreeform(args, schema); got["_parse_error"] == nil {
		t.Fatalf("expected parse-error input to remain untouched, got %#v", got)
	}

	delete(args, "_parse_error")
	if got := BindFreeform(args, schema); got["first"] != nil || got["second"] != nil {
		t.Fatalf("expected ambiguous schema to remain unbound, got %#v", got)
	}
}
