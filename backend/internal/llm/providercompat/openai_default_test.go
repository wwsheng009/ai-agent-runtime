package providercompat

import "testing"

// TestOpenAIDefault_DeveloperProjectedToSystem locks the default
// OpenAI-compatible wire projection: outgoing developer instructions are
// copied and rewritten to system messages, because strict OpenAI-compatible
// gateways (system/user/assistant/tool only) reject developer with HTTP 400
// (retryable=false). System is the universally accepted equivalent.
func TestOpenAIDefault_DeveloperProjectedToSystem(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "system", "content": "env"},
		{"role": "developer", "content": "be terse"},
		{"role": "user", "content": "hi"},
		{"role": "assistant", "content": "yo"},
		{"role": "developer", "content": "fact ledger"},
	}
	got := NormalizeOpenAICompatibleMessages(Context{Protocol: "openai"}, messages)
	if len(got) != len(messages) {
		t.Fatalf("expected same message count, got %d", len(got))
	}
	if got[0]["role"] != "system" {
		t.Fatalf("expected leading system untouched, got %#v", got[0]["role"])
	}
	if got[1]["role"] != "system" {
		t.Fatalf("expected developer[1] projected to system, got %#v", got[1]["role"])
	}
	if got[1]["content"] != "be terse" {
		t.Fatalf("expected developer content preserved, got %#v", got[1]["content"])
	}
	if got[2]["role"] != "user" || got[3]["role"] != "assistant" {
		t.Fatalf("expected user/assistant unchanged, got %#v %#v", got[2]["role"], got[3]["role"])
	}
	if got[4]["role"] != "system" {
		t.Fatalf("expected developer[4] projected to system, got %#v", got[4]["role"])
	}
}

// TestOpenAIDefault_DeveloperProjectionNeverMutatesCanonicalHistory verifies
// the transform is a send-time copy: the canonical/runtime history keeps the
// developer role for replay on other providers.
func TestOpenAIDefault_DeveloperProjectionNeverMutatesCanonicalHistory(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "developer", "content": "goal"},
		{"role": "user", "content": "hi"},
	}
	got := NormalizeOpenAICompatibleMessages(Context{Protocol: "openai"}, messages)
	if len(got) != 2 || got[0]["role"] != "system" || got[1]["role"] != "user" {
		t.Fatalf("expected projected copy (developer→system), got %#v", got)
	}
	if messages[0]["role"] != "developer" {
		t.Fatalf("expected canonical developer untouched, got %#v", messages[0]["role"])
	}
	if messages[1]["role"] != "user" {
		t.Fatalf("expected canonical user untouched, got %#v", messages[1]["role"])
	}
}

// TestOpenAIDefault_DeveloperProjectionIdempotent verifies messages without a
// developer role are never rewritten (identity return, no copy churn).
func TestOpenAIDefault_DeveloperProjectionIdempotent(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "system", "content": "env"},
		{"role": "user", "content": "hi"},
	}
	got, changed := openAIDefaultAdapter{}.NormalizeOpenAICompatibleMessages(Context{Protocol: "openai"}, messages)
	if changed {
		t.Fatal("expected no change reported for developer-free messages")
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0]["role"] != "system" || got[1]["role"] != "user" {
		t.Fatalf("expected messages unchanged, got %#v", got)
	}
	// Strict idempotence: running the projection twice yields identical output.
	again, changedAgain := openAIDefaultAdapter{}.NormalizeOpenAICompatibleMessages(Context{Protocol: "openai"}, got)
	if changedAgain {
		t.Fatal("expected second pass to report no change")
	}
	if len(again) != 2 || again[0]["role"] != "system" || again[1]["role"] != "user" {
		t.Fatalf("expected second pass output identical, got %#v", again)
	}
}

// TestOpenAIDefault_DeveloperProjectionOnlyForOpenAIProtocol verifies the
// default projection never runs for codex (developer is an official Responses
// API role) or anthropic (handled by its own adapter rules).
func TestOpenAIDefault_DeveloperProjectionOnlyForOpenAIProtocol(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "developer", "content": "goal"},
	}
	got := NormalizeOpenAICompatibleMessages(Context{Protocol: "codex"}, messages)
	if got[0]["role"] != "developer" {
		t.Fatalf("expected codex developer untouched, got %#v", got[0]["role"])
	}
}