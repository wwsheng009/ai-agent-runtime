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
