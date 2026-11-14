package memoryruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

func TestRecordTurnDigestEnforcesProducerEligibility(t *testing.T) {
	for _, tc := range []struct {
		name      string
		workspace string
		subagent  bool
		ephemeral bool
		mode      memorystore.SessionMode
		wantError bool
		wantRows  int
	}{
		{"allowed", "/workspace", false, false, memorystore.SessionModeEnabled, false, 1},
		{"other_workspace", "/other-project", false, false, memorystore.SessionModeEnabled, false, 0},
		{"unknown_workspace", "", false, false, memorystore.SessionModeEnabled, true, 0},
		{"subagent", "/workspace", true, false, memorystore.SessionModeEnabled, false, 0},
		{"ephemeral", "/workspace", false, true, memorystore.SessionModeEnabled, false, 0},
		{"disabled", "/workspace", false, false, memorystore.SessionModeDisabled, false, 0},
		{"polluted", "/workspace", false, false, memorystore.SessionModePolluted, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			source := &fakeSource{sessions: []SessionRef{{
				ID: "s1", WorkspacePath: tc.workspace, IsSubagent: tc.subagent, Ephemeral: tc.ephemeral,
			}}}
			rt, store := newRuntimeForTest(t, "on", source, &fakeExtractor{})
			rt.Config.TurnDigestEnabled = true
			rt.Config.TurnDigestStoreText = true
			// No injected mode reader: persisted opt-out must still be honored.
			rt.Modes = nil
			require.NoError(t, store.SetSessionMode(ctx, "s1", tc.mode, store.ProjectKey()))
			err := rt.RecordTurnDigest(ctx, chat.TurnDigest{
				SessionID: "s1", TurnID: "t1", Prompt: "private project content", AssistantOutput: "answer",
			})
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			rows, err := store.ListPendingTurnDigests(ctx, "s1", 10)
			require.NoError(t, err)
			require.Len(t, rows, tc.wantRows)
		})
	}
}

type failingDigestModes struct{}

func (failingDigestModes) GetSessionMode(context.Context, string) (memorystore.SessionMode, error) {
	return "", errors.New("mode lookup unavailable")
}

func TestRecordTurnDigestFailsClosedOnIdentityOrModeFailure(t *testing.T) {
	ctx := context.Background()
	rt, store := newRuntimeForTest(t, "on", &fakeSource{}, &fakeExtractor{})
	rt.Config.TurnDigestEnabled = true
	digest := chat.TurnDigest{SessionID: "s1", TurnID: "t1", Prompt: "must not be stored"}
	require.Error(t, rt.RecordTurnDigest(ctx, digest))
	rt.Source = &fakeSource{sessions: []SessionRef{{ID: "s1", WorkspacePath: "/workspace"}}}
	rt.Modes = failingDigestModes{}
	require.Error(t, rt.RecordTurnDigest(ctx, digest))
	rt.Modes = store
	rt.Config.ProjectKey = "another-project"
	require.Error(t, rt.RecordTurnDigest(ctx, digest))
	rt.Config.ProjectKey = store.ProjectKey()
	rt.Config.IncludeSubagents = true
	rt.Source = &fakeSource{sessions: []SessionRef{{ID: "s1", WorkspacePath: "/workspace", IsSubagent: true}}}
	require.Error(t, rt.RecordTurnDigest(ctx, digest), "included subagents still need trusted provenance")
	rt.Config.Workspace = ""
	require.Error(t, rt.RecordTurnDigest(ctx, digest), "missing producer workspace must fail closed")
	rt.Source = nil
	require.Error(t, rt.RecordTurnDigest(ctx, digest))
	rows, err := store.ListPendingTurnDigests(ctx, "s1", 10)
	require.NoError(t, err)
	require.Empty(t, rows)
}

type identityOnlyStorage struct {
	chat.SessionStorage
	session *chat.Session
	reads   int
}

func (s *identityOnlyStorage) LoadMetadata(context.Context, string) (*chat.Session, error) {
	s.reads++
	return s.session, nil
}

func (*identityOnlyStorage) Load(context.Context, string) (*chat.Session, error) {
	return nil, errors.New("identity lookup must not load transcript history")
}

func TestRecordTurnDigestUsesMetadataAndPreservesSubagentProvenance(t *testing.T) {
	ctx := context.Background()
	session := chat.NewSession("user-1")
	session.SetContext(sessionmeta.WorkspacePath, "/workspace")
	session.SetContext(agentcontrol.SessionContextParentSessionID, "parent")
	session.SetContext(agentcontrol.SessionContextAgentType, "worker")
	storage := &identityOnlyStorage{SessionStorage: chat.NewInMemoryStorage(), session: session}
	manager := chat.NewSessionManager(storage, nil)
	defer manager.Stop()
	rt, store := newRuntimeForTest(t, "on", &fakeSource{}, &fakeExtractor{})
	rt.Source = &ChatSessionSource{Manager: manager, UserID: "user-1"}
	rt.Config.TurnDigestEnabled = true
	rt.Config.IncludeSubagents = true
	require.NoError(t, rt.RecordTurnDigest(ctx, chat.TurnDigest{
		SessionID: session.ID, TurnID: "t1", Prompt: "child result",
	}))
	require.Equal(t, 1, storage.reads)
	row, err := store.GetTurnDigestByTurn(ctx, session.ID, "t1")
	require.NoError(t, err)
	require.Equal(t, "parent", row.ParentSessionID)
	require.Equal(t, "worker", row.AgentType)
	rt.Source = &ChatSessionSource{Manager: manager, UserID: "other-user"}
	require.Error(t, rt.RecordTurnDigest(ctx, chat.TurnDigest{SessionID: session.ID, TurnID: "t2"}))
}
