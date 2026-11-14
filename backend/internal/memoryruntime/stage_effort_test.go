package memoryruntime

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type memoryStageProvider struct {
	requests []*llm.LLMRequest
}

func (p *memoryStageProvider) Name() string { return "memory-test" }
func (p *memoryStageProvider) Call(_ context.Context, request *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.requests = append(p.requests, request)
	content := "## Facts\n- Preserve canonical evidence."
	if request.Metadata["memory_extraction"] == true {
		content = `{"records":[],"ignored":[]}`
	}
	return &llm.LLMResponse{Model: request.Model, Content: content}, nil
}
func (*memoryStageProvider) Stream(context.Context, *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	return nil, fmt.Errorf("streaming not used in memory tests")
}
func (*memoryStageProvider) CountTokens(text string) int { return len(text) / 4 }
func (*memoryStageProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{}
}
func (*memoryStageProvider) CheckHealth(context.Context) error { return nil }

func TestMemoryStageReasoningOverridesAndLegacyInheritance(t *testing.T) {
	for _, tc := range []struct {
		name, generation, window, summary string
		want                              []string
	}{
		{"legacy", "max", "", "", []string{"max", "max", "max"}},
		{"overrides", "max", "low", "medium", []string{"max", "low", "medium"}},
		{"zero", "", "", "", []string{"low", "low", "low"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &memoryStageProvider{}
			runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultProvider: provider.Name(), DefaultModel: "test-model"})
			require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
			cfg := Config{
				ExtractionReasoningEffort: tc.generation, WindowSummaryReasoningEffort: tc.window,
				ConsolidationReasoningEffort: tc.summary,
			}
			ctx := context.Background()
			_, err := (&LLMExtractor{Runtime: runtime, Config: cfg}).Extract(ctx, SessionRef{ID: "test"}, Transcript{
				Messages: []TranscriptMessage{{ID: "m1", Seq: 1, Role: "user", Content: "Keep canonical evidence."}},
			})
			require.NoError(t, err)
			_, err = (&LLMWindowSummarizer{Runtime: runtime, Config: cfg}).SummarizeWindow(ctx, WindowSummaryInput{
				Session: SessionRef{ID: "test"}, WindowID: "w1", StartDigestSeq: 1, EndDigestSeq: 1,
				Digests: []memorystore.TurnDigest{{DigestSeq: 1, UserText: "Keep canonical evidence."}},
			})
			require.NoError(t, err)
			_, err = (&LLMProjectSummarizer{Runtime: runtime, Config: cfg}).SummarizeProject(ctx, ProjectSummaryInput{
				TokenBudget: 800, Records: []memorystore.Record{{ID: "r1", Kind: "fact", Text: "Keep canonical evidence."}},
			})
			require.NoError(t, err)
			require.Len(t, provider.requests, 3)
			for i, request := range provider.requests {
				require.Equal(t, tc.want[i], request.ReasoningEffort)
			}
		})
	}
}
