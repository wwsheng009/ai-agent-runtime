package compactruntime

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/factledger"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func remoteFactsRuntime(t *testing.T, response *llm.RemoteCompactResponse) (*llm.LLMRuntime, *compactRemoteProvider) {
	t.Helper()
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactRemoteProvider{
		compactTestProvider: &compactTestProvider{
			name: "provider-a",
			capabilities: map[string]agentconfig.ModelCapabilitySpec{
				"gpt-5": {AutoCompactTokenLimit: 150, SupportsRemoteCompact: true},
			},
		},
		response: response,
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))
	return runtime, provider
}

func remoteFactsHistory() []types.Message {
	return []types.Message{
		*types.NewSystemMessage("You are a helpful assistant."),
		*types.NewUserMessage("decision: keep the ledger declarative"),
		*types.NewUserMessage("you must not hand-edit generated files"),
		*types.NewToolMessage("call-1", "Exit code: 0 Output: test result: ok. 666 passed; 0 failed"),
		*types.NewAssistantMessage("The build failed: error[E0308] mismatched types"),
		*types.NewUserMessage("Continue from the summary."),
	}
}

func remoteFactsRequest(sessionID string, history []types.Message) Request {
	return Request{
		SessionID:          sessionID,
		TaskID:             sessionID,
		Model:              "gpt-5",
		History:            history,
		KeepRecentMessages: 1,
		Phase:              PhasePreTurn,
		CountTokens:        func(messages []types.Message) int { return len(messages) * 60 },
	}
}

func listLedgerFacts(t *testing.T, store *artifact.Store, sessionID string) []factledger.Fact {
	t.Helper()
	facts, err := factledger.New(store).ListActive(context.Background(), factledger.Query{SessionID: sessionID})
	require.NoError(t, err)
	return facts
}

// A remote provider replaces history with its own projection, so nothing else
// records what that replacement destroyed. The declarative residue has to be
// captured at this boundary or it is lost for good.
func TestMaybeCompactRemoteRecordsDeclarativeResidueOfDroppedTurns(t *testing.T) {
	store, err := artifact.NewStore(&artifact.StoreConfig{Path: filepath.Join(t.TempDir(), "facts.sqlite")})
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	runtime, provider := remoteFactsRuntime(t, &llm.RemoteCompactResponse{
		ReplacementHistory: []types.Message{
			*types.NewSystemMessage("You are a helpful assistant."),
			*types.NewAssistantMessage("Compacted context from remote provider."),
			*types.NewUserMessage("Continue from the summary."),
		},
		CompactedMessages: 3,
	})
	compactor := New(runtime, contextmgr.NewManager(contextmgr.DefaultBudget(), store))

	result, status, err := compactor.MaybeCompact(context.Background(), remoteFactsRequest("session-remote-facts", remoteFactsHistory()))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, ModeRemote, status.Mode)
	require.Equal(t, 1, provider.remoteCallCount)

	facts := listLedgerFacts(t, store, "session-remote-facts")
	kinds := make([]string, 0, len(facts))
	joined := ""
	for _, fact := range facts {
		kinds = append(kinds, fact.Kind)
		joined += fmt.Sprintf("%v|", fact.Value)
	}
	require.ElementsMatch(t, []string{"decision", "constraint"}, kinds)

	// A turn the provider kept is still readable, so recording it as a fact would
	// be duplication, not preservation.
	require.NotContains(t, joined, "Continue from the summary")
	require.NotContains(t, joined, "helpful assistant")
	// Tool payloads stay out of the ledger even when the text contains "failed".
	require.NotContains(t, joined, "666")
	require.NotContains(t, joined, "Exit code:")
}

// Crossing the same boundary twice must not pile up copies of one statement.
func TestMaybeCompactRemoteFactResidueIsIdempotent(t *testing.T) {
	store, err := artifact.NewStore(&artifact.StoreConfig{Path: filepath.Join(t.TempDir(), "facts.sqlite")})
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	response := &llm.RemoteCompactResponse{
		ReplacementHistory: []types.Message{
			*types.NewSystemMessage("You are a helpful assistant."),
			*types.NewAssistantMessage("Compacted context from remote provider."),
		},
		CompactedMessages: 4,
	}
	runtime, _ := remoteFactsRuntime(t, response)
	compactor := New(runtime, contextmgr.NewManager(contextmgr.DefaultBudget(), store))

	for round := 0; round < 3; round++ {
		_, _, err := compactor.MaybeCompact(context.Background(), remoteFactsRequest("session-remote-idem", remoteFactsHistory()))
		require.NoError(t, err)
	}
	// Two user assertions survive; the assistant failure recap is the summary's
	// business, so the harvest stays at two no matter how often the boundary hits.
	require.Len(t, listLedgerFacts(t, store, "session-remote-idem"), 2)
}

// Without a manager there is no durable store at all; the compaction must still
// return the provider replacement rather than crash.
func TestMaybeCompactRemoteWithoutManagerStillCompacts(t *testing.T) {
	runtime, provider := remoteFactsRuntime(t, &llm.RemoteCompactResponse{
		ReplacementHistory: []types.Message{
			*types.NewSystemMessage("You are a helpful assistant."),
			*types.NewUserMessage("Continue from the summary."),
		},
		CompactedMessages: 3,
	})
	compactor := New(runtime, nil)

	result, status, err := compactor.MaybeCompact(context.Background(), remoteFactsRequest("session-remote-nostore", remoteFactsHistory()))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, ModeRemote, status.Mode)
	require.Equal(t, 1, provider.remoteCallCount)
	require.NotEmpty(t, result.ReplacementHistory)
}

// The ledger must record only what actually vanished: a provider that keeps the
// turn changes nothing.
func TestMaybeCompactRemoteRecordsNothingWhenProviderKeepsTurns(t *testing.T) {
	store, err := artifact.NewStore(&artifact.StoreConfig{Path: filepath.Join(t.TempDir(), "facts.sqlite")})
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	history := remoteFactsHistory()
	runtime, _ := remoteFactsRuntime(t, &llm.RemoteCompactResponse{
		// Replacement carries every source turn verbatim.
		ReplacementHistory: history,
		CompactedMessages:  0,
	})
	compactor := New(runtime, contextmgr.NewManager(contextmgr.DefaultBudget(), store))

	_, _, err = compactor.MaybeCompact(context.Background(), remoteFactsRequest("session-remote-kept", history))
	require.NoError(t, err)
	require.Empty(t, listLedgerFacts(t, store, "session-remote-kept"))
}
