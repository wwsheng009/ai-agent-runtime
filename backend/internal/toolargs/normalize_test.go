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
