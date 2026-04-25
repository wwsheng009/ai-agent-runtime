package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestSummarizePromptLayoutForEvent_IncludesLayersAndSources(t *testing.T) {
	messages := []types.Message{
		{Role: "system", Content: "Base guardrail", Metadata: types.Metadata{"prompt_layer": "base", "prompt_source": "system.md"}},
		{Role: "developer", Content: "Prefer rg", Metadata: types.Metadata{"prompt_layer": "developer", "prompt_source": "tools.md"}},
		{Role: "developer", Content: "Read AGENTS", Metadata: types.Metadata{"prompt_layer": "user", "prompt_source": "AGENTS.md"}},
	}

	summary := summarizePromptLayoutForEvent(messages, nil)
	assert.Contains(t, summary.Summary, "layers=base/system -> developer/developer -> user/developer")
	assert.Contains(t, summary.Summary, "sources=system.md, tools.md, AGENTS.md")
	assert.Greater(t, summary.InstructionChars, 0)
	assert.Equal(t, summary.TotalChars, summary.InstructionChars)
	assert.Equal(t, 0, summary.InstructionTokens)
}

func TestSummarizePromptLayoutForEvent_DistinguishesInstructionFromTotal(t *testing.T) {
	messages := []types.Message{
		{Role: "system", Content: "You are a helpful assistant.", Metadata: types.Metadata{"prompt_layer": "base"}},
		{Role: "developer", Content: "Prefer rg when available.", Metadata: types.Metadata{"prompt_layer": "developer"}},
		{Role: "user", Content: "What is the capital of France?"},
		{Role: "assistant", Content: "Paris."},
		{Role: "user", Content: "And Germany?"},
	}

	summary := summarizePromptLayoutForEvent(messages, nil)
	assert.Contains(t, summary.Summary, "layers=base/system -> developer/developer")
	assert.Greater(t, summary.InstructionChars, 0)
	assert.Greater(t, summary.TotalChars, summary.InstructionChars)
}

func TestSummarizePromptLayoutForEvent_WithTokenCounter(t *testing.T) {
	messages := []types.Message{
		{Role: "system", Content: "You are a helpful assistant.", Metadata: types.Metadata{"prompt_layer": "base"}},
		{Role: "user", Content: "Hello!"},
	}

	// Simple token counter: 1 token per 4 chars
	tokenCountFunc := func(text string) int { return len(text) / 4 }

	summary := summarizePromptLayoutForEvent(messages, tokenCountFunc)
	assert.Greater(t, summary.InstructionTokens, 0)
	assert.Greater(t, summary.TotalTokens, summary.InstructionTokens)
}
