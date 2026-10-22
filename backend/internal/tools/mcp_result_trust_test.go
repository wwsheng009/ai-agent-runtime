package tools

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
)

func TestSanitizeMCPResultRemoteFoldsMaliciousSkip(t *testing.T) {
	body := strings.Repeat("x", 256*1024)
	metadata := map[string]interface{}{
		toolcoreoutput.KeySkipRenderTruncation: true,
		toolcoreoutput.KeyModelVisibleBudget:   1 << 30,
	}
	gotBody, gotMetadata := sanitizeMCPResult(body, metadata, &MCPResultContext{
		Server:         "238",
		Tool:           "view",
		ExecutionScope: "remote",
		TrustLevel:     "trusted_remote",
	})
	if len(gotBody) > toolcoreoutput.ViewOutputBudgetBytes {
		t.Fatalf("body = %d bytes, want <= %d", len(gotBody), toolcoreoutput.ViewOutputBudgetBytes)
	}
	if gotMetadata[toolcoreoutput.KeyModelVisibleBudget] != toolcoreoutput.ViewOutputBudgetBytes {
		t.Fatalf("budget = %v", gotMetadata[toolcoreoutput.KeyModelVisibleBudget])
	}
}

func TestSanitizeMCPResultLocalIsUntouched(t *testing.T) {
	body := strings.Repeat("x", 256*1024)
	metadata := map[string]interface{}{
		toolcoreoutput.KeySkipRenderTruncation: true,
		"custom":                               "kept",
	}
	gotBody, gotMetadata := sanitizeMCPResult(body, metadata, &MCPResultContext{
		Server:         "toolkit",
		Tool:           "view",
		ExecutionScope: "local",
		TrustLevel:     "local",
	})
	if gotBody != body {
		t.Fatal("local result must not be folded")
	}
	if gotMetadata["custom"] != "kept" {
		t.Fatal("local metadata must be preserved")
	}
}

func TestMCPContextOriginUsesConfiguredScope(t *testing.T) {
	context := &MCPResultContext{Server: "238", ExecutionScope: "remote", TrustLevel: "trusted_remote"}
	origin := context.Origin()
	if origin.Scope != "remote" || origin.Target != "238" || origin.TrustLevel != "trusted_remote" {
		t.Fatalf("unexpected origin: %+v", origin)
	}
	if origin != (toolcore.ResultOrigin{Scope: "remote", Target: "238", TrustLevel: "trusted_remote"}) {
		t.Fatalf("unexpected origin equality: %+v", origin)
	}
}
