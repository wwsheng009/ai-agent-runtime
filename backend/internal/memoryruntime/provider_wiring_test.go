package memoryruntime

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type routingTestProvider struct {
	name     string
	requests []*llm.LLMRequest
}

func (p *routingTestProvider) Name() string {
	return p.name
}

func (p *routingTestProvider) Call(_ context.Context, request *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.requests = append(p.requests, request)
	content := "## Project decisions\n- Keep the configured memory provider."
	if request.Metadata["memory_extraction"] == true {
		content = `{"records":[],"ignored":[]}`
	} else if request.Metadata["memory_turn_window"] == true {
		content = "Keep the configured memory provider."
	}
	return &llm.LLMResponse{Model: request.Model, Content: content}, nil
}

func (*routingTestProvider) Stream(context.Context, *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	return nil, fmt.Errorf("streaming not used in memory tests")
}

func (*routingTestProvider) CountTokens(text string) int {
	return len(text) / 4
}

func (*routingTestProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{}
}

func (*routingTestProvider) CheckHealth(context.Context) error {
	return nil
}

func TestLLMMemoryStagesUseConfiguredProvidersAndModels(t *testing.T) {
	extractionProvider := &routingTestProvider{name: "extract-provider"}
	summaryProvider := &routingTestProvider{name: "summary-provider"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: extractionProvider.Name(),
		DefaultModel:    "default-model",
		MaxRetries:      0,
	})
	require.NoError(t, runtime.RegisterProvider(extractionProvider.Name(), extractionProvider))
	require.NoError(t, runtime.RegisterProvider(summaryProvider.Name(), summaryProvider))

	ctx := context.Background()
	extractionCfg := Config{
		ExtractionProvider:        extractionProvider.Name(),
		ExtractionModel:           "extract-model",
		ExtractionReasoningEffort: "low",
		MaxSourceBytes:            4096,
	}
	_, err := (&LLMExtractor{Runtime: runtime, Config: extractionCfg}).Extract(ctx, SessionRef{ID: "session-1"}, Transcript{
		Messages: []TranscriptMessage{{ID: "message-1", Seq: 1, Role: "user", Content: "Keep this decision."}},
	})
	require.NoError(t, err)
	require.Len(t, extractionProvider.requests, 1)
	require.Equal(t, extractionProvider.Name(), extractionProvider.requests[0].Provider)
	require.Equal(t, "extract-model", extractionProvider.requests[0].Model)

	_, err = (&LLMWindowSummarizer{Runtime: runtime, Config: extractionCfg}).SummarizeWindow(ctx, WindowSummaryInput{
		Session:        SessionRef{ID: "session-1"},
		WindowID:       "window-1",
		StartDigestSeq: 1,
		EndDigestSeq:   1,
		Digests:        []memorystore.TurnDigest{{DigestSeq: 1, UserText: "Keep this decision."}},
	})
	require.NoError(t, err)
	require.Len(t, extractionProvider.requests, 2)
	require.Equal(t, extractionProvider.Name(), extractionProvider.requests[1].Provider)
	require.Equal(t, "extract-model", extractionProvider.requests[1].Model)

	summaryCfg := extractionCfg
	summaryCfg.ConsolidationProvider = summaryProvider.Name()
	summaryCfg.ConsolidationModel = "summary-model"
	_, err = (&LLMProjectSummarizer{Runtime: runtime, Config: summaryCfg}).SummarizeProject(ctx, ProjectSummaryInput{
		ProjectKey:  "project-1",
		TokenBudget: 400,
		Records:     []memorystore.Record{{ID: "record-1", Kind: "decision", Text: "Keep this decision."}},
	})
	require.NoError(t, err)
	require.Len(t, summaryProvider.requests, 1)
	require.Equal(t, summaryProvider.Name(), summaryProvider.requests[0].Provider)
	require.Equal(t, "summary-model", summaryProvider.requests[0].Model)
}

func TestLLMProjectSummarizerFallsBackToExtractionProviderAndModel(t *testing.T) {
	provider := &routingTestProvider{name: "extract-provider"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: provider.Name(),
		DefaultModel:    "default-model",
		MaxRetries:      0,
	})
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	_, err := (&LLMProjectSummarizer{
		Runtime: runtime,
		Config: Config{
			ExtractionProvider: provider.Name(),
			ExtractionModel:    "extract-model",
		},
	}).SummarizeProject(context.Background(), ProjectSummaryInput{
		ProjectKey:  "project-1",
		TokenBudget: 400,
		Records:     []memorystore.Record{{ID: "record-1", Kind: "fact", Text: "Keep this fact."}},
	})
	require.NoError(t, err)
	require.Len(t, provider.requests, 1)
	require.Equal(t, provider.Name(), provider.requests[0].Provider)
	require.Equal(t, "extract-model", provider.requests[0].Model)
}

func TestLLMMemoryStagesKeepAutomaticRoutingWhenProviderIsEmpty(t *testing.T) {
	provider := &routingTestProvider{name: "runtime-default"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: provider.Name(),
		DefaultModel:    "runtime-default-model",
		MaxRetries:      0,
	})
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	_, err := (&LLMExtractor{Runtime: runtime, Config: Config{MaxSourceBytes: 4096}}).Extract(
		context.Background(),
		SessionRef{ID: "session-1"},
		Transcript{Messages: []TranscriptMessage{{ID: "message-1", Seq: 1, Role: "user", Content: "Keep this fact."}}},
	)
	require.NoError(t, err)
	require.Len(t, provider.requests, 1)
	require.Equal(t, provider.Name(), provider.requests[0].Provider)
	require.Equal(t, "runtime-default-model", provider.requests[0].Model)
}
