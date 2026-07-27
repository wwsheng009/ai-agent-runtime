package chat

import (
	"testing"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestAppendSessionActorUsagePayloadIncludesSource(t *testing.T) {
	payload := map[string]interface{}{}
	appendSessionActorUsagePayload(payload, &runtimetypes.TokenUsage{
		PromptTokens: 10,
		TotalTokens:  12,
		UsageSource:  "provider_reported",
	})
	if payload["usage_source"] != "provider_reported" {
		t.Fatalf("usage source missing from session payload: %#v", payload)
	}
}
