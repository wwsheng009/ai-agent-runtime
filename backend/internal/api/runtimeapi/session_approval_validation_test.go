package runtimeapi

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

func TestValidateApproveToolDecision(t *testing.T) {
	valid := []chat.ApproveToolDecision{
		{Allow: true},
		{Allow: true, RememberScope: "once"},
		{Allow: true, RememberScope: "session"},
		{Allow: true, RememberScope: "PROJECT"},
		{Allow: true, RememberScope: " project "},
		{Allow: false, RememberScope: "project"}, // deny ignores the scope
		{Allow: false, Feedback: "不要改这个文件"},
		{Allow: false, Feedback: strings.Repeat("x", approvalFeedbackMaxRunes)},
	}
	for _, decision := range valid {
		if err := validateApproveToolDecision(decision); err != nil {
			t.Fatalf("decision %+v rejected: %v", decision, err)
		}
	}

	invalid := []chat.ApproveToolDecision{
		{Allow: true, RememberScope: "global"},  // typo must not become session
		{Allow: true, RememberScope: "forever"}, // nor a durable grant
		{Allow: false, Feedback: strings.Repeat("字", approvalFeedbackMaxRunes+1)},
	}
	for _, decision := range invalid {
		if err := validateApproveToolDecision(decision); err == nil {
			t.Fatalf("decision %+v accepted, want validation error", decision)
		}
	}
}
