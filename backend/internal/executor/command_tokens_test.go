package executor

import (
	"reflect"
	"testing"
)

func TestSplitCommandTokens_PreservesQuotedPathSegments(t *testing.T) {
	tokens := SplitCommandTokens(`cat "frontend/src/pages/setting/runtime file.yaml" | head -n 1`)
	want := []string{
		"cat",
		"frontend/src/pages/setting/runtime file.yaml",
		"|",
		"head",
		"-n",
		"1",
	}
	if !reflect.DeepEqual(tokens, want) {
		t.Fatalf("expected tokens %v, got %v", want, tokens)
	}
}

func TestSplitCommandTokens_HandlesPipeWithoutWhitespace(t *testing.T) {
	tokens := SplitCommandTokens(`git diff -- internal/gateway/handlers/admin_config.go |head -200`)
	want := []string{
		"git",
		"diff",
		"--",
		"internal/gateway/handlers/admin_config.go",
		"|",
		"head",
		"-200",
	}
	if !reflect.DeepEqual(tokens, want) {
		t.Fatalf("expected tokens %v, got %v", want, tokens)
	}
}

func TestHasPipedHeadToken(t *testing.T) {
	if !HasPipedHeadToken(SplitCommandTokens(`git diff | head -200`)) {
		t.Fatal("expected piped head token to be detected")
	}
	if HasPipedHeadToken(SplitCommandTokens(`head -200`)) {
		t.Fatal("did not expect standalone head to count as piped head")
	}
}
