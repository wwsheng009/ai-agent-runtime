package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorypolicy"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestLocalChatMemoryReadPolicyMatchesInjectionStore(t *testing.T) {
	for _, behavior := range []string{"warn", "exclude"} {
		t.Run(behavior, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			cfg := runtimecfg.DefaultRuntimeConfig()
			cfg.Memory.Mode = "on"
			cfg.Memory.Injection.Enabled = true
			cfg.Memory.Injection.Mode = "notes"
			cfg.Memory.Freshness.StaleBehavior = behavior
			admin, err := memorystore.New(memorystore.Config{
				ProjectRoot: root, Mode: "on", InjectionMode: "notes",
				StaleBehavior: memorypolicy.StaleBehavior(cfg.Memory),
			})
			require.NoError(t, err)
			defer admin.Close()
			record, err := admin.InsertCandidate(ctx, memorystore.Candidate{
				SessionID: "source", Kind: "workflow", Text: "release checklist workflow",
				Confidence: 0.9, SourceHash: "hash", SourceRevision: 1,
				RevalidateAfter: time.Now().Add(-time.Hour),
			})
			require.NoError(t, err)
			_, err = admin.PromoteCandidate(ctx, record.ID)
			require.NoError(t, err)
			_, err = admin.SweepFreshness(ctx, time.Now())
			require.NoError(t, err)
			opts := memorystore.InjectOptions{Query: "release checklist", Limit: 10, TokenBudget: 1000}
			expected, err := admin.SelectForInjectV2(opts)
			require.NoError(t, err)
			if behavior == "warn" {
				require.Contains(t, expected.IDs, record.ID)
			} else {
				require.Empty(t, expected.IDs)
			}
			ac := &agent.Config{
				Name: "memory-policy-test", Model: "test-model",
				Options: map[string]interface{}{"workspace_path": root},
			}
			applyLocalChatMemoryOptions(ac, cfg)
			a := agent.NewAgentWithLLM(ac, nil, nil)
			store, ok := a.GetContextManager().ProjectMemory.(*memorystore.Store)
			require.True(t, ok)
			defer store.Close()
			actual, err := store.SelectForInjectV2(opts)
			require.NoError(t, err)
			require.Equal(t, expected.IDs, actual.IDs, "diagnostics and real chat injection must agree")
		})
	}
}
