package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// FR-13：aicli 侧 profile 维度解析——声明名优先，回退绑定 ref，未知一律空。

func TestLocalSessionProfileLookupPrefersDeclaredName(t *testing.T) {
	ctx := context.Background()
	store := runtimechat.NewInMemoryStorage()
	session := runtimechat.NewSession("profile-lookup-user")
	session.Metadata.Context = map[string]interface{}{
		sessionmeta.ProfileRef:  "path/to/ref",
		sessionmeta.ProfileName: "coding",
	}
	require.NoError(t, store.Save(ctx, session))

	require.Equal(t, "coding", localSessionProfileLookup(store)(session.ID))
}

func TestLocalSessionProfileLookupFallsBackToRef(t *testing.T) {
	ctx := context.Background()
	store := runtimechat.NewInMemoryStorage()
	session := runtimechat.NewSession("profile-lookup-user")
	session.Metadata.Context = map[string]interface{}{
		sessionmeta.ProfileRef: "path/to/ref",
	}
	require.NoError(t, store.Save(ctx, session))

	require.Equal(t, "path/to/ref", localSessionProfileLookup(store)(session.ID))
}

func TestLocalSessionProfileLookupUnknownCases(t *testing.T) {
	store := runtimechat.NewInMemoryStorage()
	lookup := localSessionProfileLookup(store)
	require.Equal(t, "", lookup("missing-session"))
	require.Equal(t, "", lookup("   "))
	require.Equal(t, "", localSessionProfileLookup(nil)("any-session"))
}
