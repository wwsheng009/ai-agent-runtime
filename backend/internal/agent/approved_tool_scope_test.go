package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

func TestBindApprovedArtifactReadScopePreservesLineage(t *testing.T) {
	ctx := toolctx.WithArtifactReadScopeForSessions(context.Background(), "child", "parent")
	bound, ok := toolctx.ArtifactReadScope(bindApprovedArtifactReadScope(ctx, "child"))
	require.True(t, ok)
	require.Equal(t, map[string]bool{"child": true, "parent": true}, bound.SessionIDs)
}

func TestBindApprovedArtifactReadScopeNarrowsUnrestricted(t *testing.T) {
	ctx := toolctx.WithArtifactReadScope(context.Background(), artifact.ReadScope{Unrestricted: true})
	bound, ok := toolctx.ArtifactReadScope(bindApprovedArtifactReadScope(ctx, "child"))
	require.True(t, ok)
	require.False(t, bound.Unrestricted)
	require.Equal(t, map[string]bool{"child": true}, bound.SessionIDs)
}
