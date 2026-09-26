package runtimeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	runtimeagent "github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// branchFixture bundles the handler plumbing shared by the session branch tests.
type branchFixture struct {
	handler        *Handler
	router         *mux.Router
	source         *chat.Session
	storage        chat.SessionStorage
	sessionManager *chat.SessionManager
	runtimeStore   *chat.InMemoryRuntimeStore
}

// branchAnchorPayload mirrors the response anchor contract.
type branchAnchorPayload struct {
	SourceMessageID string `json:"source_message_id"`
	TurnIndex       int    `json:"turn_index"`
	Included        bool   `json:"included"`
}

// branchSessionPayload mirrors the session fields the branch tests assert on.
type branchSessionPayload struct {
	ID       string                   `json:"id"`
	UserID   string                   `json:"userId"`
	State    string                   `json:"state"`
	History  []runtimetypes.Message   `json:"history"`
	Metadata branchSessionMetadataDef `json:"metadata"`
}

type branchSessionMetadataDef struct {
	Title   string                 `json:"title"`
	Context map[string]interface{} `json:"context"`
}

type branchResponsePayload struct {
	Session branchSessionPayload `json:"session"`
	Anchor  branchAnchorPayload  `json:"anchor"`
}

// newBranchHandlerFixture mirrors newBacktrackHandlerFixture: an in-memory
// session store plus a live actor for the source session.
func newBranchHandlerFixture(t *testing.T, history []runtimetypes.Message) *branchFixture {
	t.Helper()
	return newBranchHandlerFixtureWith(t, history, nil, nil)
}

// newBranchHandlerFixtureWith allows injecting the session storage and seeding
// runtime state before the actor loads it (used by the busy/rollback tests).
func newBranchHandlerFixtureWith(
	t *testing.T,
	history []runtimetypes.Message,
	storage chat.SessionStorage,
	seed func(ctx context.Context, store *chat.InMemoryRuntimeStore, sessionID string),
) *branchFixture {
	t.Helper()
	ctx := context.Background()
	if storage == nil {
		storage = chat.NewInMemoryStorage()
	}
	sessionManager := chat.NewSessionManager(storage, nil)
	source, err := sessionManager.Create(ctx, "branch-api-user")
	require.NoError(t, err)
	for _, msg := range history {
		source.AddMessage(msg)
	}
	require.NoError(t, storage.Update(ctx, source))

	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	if seed != nil {
		seed(ctx, runtimeStore, source.ID)
	}

	apiAgent := runtimeagent.NewAgent(&runtimeagent.Config{
		Name:  "branch-api-test",
		Model: "test-model",
	}, nil)
	actor, err := chat.NewSessionActor(source.ID, chat.SessionActorConfig{
		Agent:        apiAgent,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)
	t.Cleanup(func() { actor.Stop() })

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)
	handler.sessionHub = chat.NewSessionHub(func(sessionID string) (*chat.SessionActor, error) {
		if sessionID == source.ID {
			return actor, nil
		}
		return nil, fmt.Errorf("unexpected actor build for session %s", sessionID)
	})
	// Pin the runtime store so the endpoint never lazily opens a real one.
	handler.sessionRuntimeStore = runtimeStore
	_, err = handler.sessionHub.GetOrCreate(source.ID)
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return &branchFixture{
		handler:        handler,
		router:         router,
		source:         source,
		storage:        storage,
		sessionManager: sessionManager,
		runtimeStore:   runtimeStore,
	}
}

func (f *branchFixture) postBranch(t *testing.T, sessionID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+sessionID+"/branch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func decodeBranchResponse(t *testing.T, rec *httptest.ResponseRecorder) branchResponsePayload {
	t.Helper()
	var resp branchResponsePayload
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	return resp
}

// branchMessageIDs returns the stable message ids in order, which is the most
// robust way to compare the branch prefix against the source history.
func branchMessageIDs(messages []runtimetypes.Message) []string {
	ids := make([]string, 0, len(messages))
	for _, msg := range messages {
		ids = append(ids, runtimetypes.MessageID(msg))
	}
	return ids
}

// branchSeedFailStorage rejects the branch seed write (identified by its fork
// lineage) to exercise the create/seed rollback path.
type branchSeedFailStorage struct {
	*chat.InMemoryStorage
}

func (s *branchSeedFailStorage) Update(ctx context.Context, session *chat.Session) error {
	if session != nil {
		if _, ok := session.Metadata.Context[chat.ContextForkCreatedAt]; ok {
			return fmt.Errorf("injected branch seed failure")
		}
	}
	return s.InMemoryStorage.Update(ctx, session)
}

func TestBranchSessionFromTurnTailAnchor(t *testing.T) {
	ctx := context.Background()
	f := newBranchHandlerFixture(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
		*runtimetypes.NewUserMessage("second"),
		*runtimetypes.NewAssistantMessage("a2"),
	})
	f.source.UpdateTitle("Source Title")
	f.source.SetContext(sessionmeta.WorkspacePath, "/tmp/branch-workspace")
	require.NoError(t, f.storage.Update(ctx, f.source))

	sourceIDs := branchMessageIDs(f.source.GetMessages())
	require.Len(t, sourceIDs, 4)
	anchorID := sourceIDs[1]

	rec := f.postBranch(t, f.source.ID, fmt.Sprintf(`{"anchor_message_id":%q}`, anchorID))
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
	resp := decodeBranchResponse(t, rec)

	require.NotEmpty(t, resp.Session.ID)
	require.NotEqual(t, f.source.ID, resp.Session.ID)
	require.Equal(t, "branch-api-user", resp.Session.UserID)
	require.Equal(t, "Source Title (branch)", resp.Session.Metadata.Title)
	require.Equal(t, sourceIDs[:2], branchMessageIDs(resp.Session.History),
		"branch history must be the source prefix up to and including the anchor")
	require.Equal(t, branchAnchorPayload{SourceMessageID: anchorID, TurnIndex: 0, Included: true}, resp.Anchor)

	// Lineage metadata, written under metadata.context.
	require.Equal(t, f.source.ID, sessionmeta.String(resp.Session.Metadata.Context, chat.ContextForkParentSessionID))
	require.Equal(t, f.source.ID, sessionmeta.String(resp.Session.Metadata.Context, chat.ContextForkRootSessionID))
	require.Equal(t, anchorID, sessionmeta.String(resp.Session.Metadata.Context, chat.ContextForkSourceMessageID))
	require.Equal(t, "Source Title", sessionmeta.String(resp.Session.Metadata.Context, chat.ContextForkOriginTitle))
	createdAt := sessionmeta.String(resp.Session.Metadata.Context, chat.ContextForkCreatedAt)
	parsed, err := time.Parse(time.RFC3339, createdAt)
	require.NoError(t, err, "fork_created_at=%q", createdAt)
	require.WithinDuration(t, time.Now().UTC(), parsed, 2*time.Minute)

	// Workspace binding is inherited from the source session.
	require.Equal(t, "/tmp/branch-workspace", sessionmeta.String(resp.Session.Metadata.Context, sessionmeta.WorkspacePath))
	// agent-control must not treat a branch as a spawned child agent.
	_, hasAgentParent := resp.Session.Metadata.Context["agent_parent_session_id"]
	require.False(t, hasAgentParent)

	// The branch is persisted with the same prefix (not just echoed).
	persisted, err := f.sessionManager.Get(ctx, resp.Session.ID)
	require.NoError(t, err)
	require.Equal(t, sourceIDs[:2], branchMessageIDs(persisted.GetMessages()))
	require.Equal(t, "Source Title (branch)", persisted.Metadata.Title)
	require.Equal(t, anchorID, sessionmeta.String(persisted.Metadata.Context, chat.ContextForkSourceMessageID))
	require.Equal(t, "Source Title", sessionmeta.String(persisted.Metadata.Context, chat.ContextForkOriginTitle))

	// The source session is untouched: same history, title, workspace, no lineage.
	require.Equal(t, sourceIDs, branchMessageIDs(f.source.GetMessages()))
	require.Equal(t, "Source Title", f.source.Metadata.Title)
	require.Equal(t, "/tmp/branch-workspace", sessionmeta.String(f.source.Metadata.Context, sessionmeta.WorkspacePath))
	_, parentSet := f.source.Metadata.Context[chat.ContextForkParentSessionID]
	require.False(t, parentSet)
	_, rootSet := f.source.Metadata.Context[chat.ContextForkRootSessionID]
	require.False(t, rootSet)
	_, sourceMessageSet := f.source.Metadata.Context[chat.ContextForkSourceMessageID]
	require.False(t, sourceMessageSet)
	_, originTitleSet := f.source.Metadata.Context[chat.ContextForkOriginTitle]
	require.False(t, originTitleSet)
	_, createdAtSet := f.source.Metadata.Context[chat.ContextForkCreatedAt]
	require.False(t, createdAtSet)

	// The branch is discoverable as a normal session of the same user.
	siblings, err := f.sessionManager.List(ctx, "branch-api-user")
	require.NoError(t, err)
	require.Len(t, siblings, 2)
}

func TestBranchSessionIncludeAnchorFalseStopsBeforeAnchor(t *testing.T) {
	f := newBranchHandlerFixture(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
		*runtimetypes.NewUserMessage("second"),
		*runtimetypes.NewAssistantMessage("a2"),
	})
	sourceIDs := branchMessageIDs(f.source.GetMessages())
	anchorID := sourceIDs[1]

	rec := f.postBranch(t, f.source.ID, fmt.Sprintf(`{"anchor_message_id":%q,"include_anchor":false}`, anchorID))
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
	resp := decodeBranchResponse(t, rec)

	require.Equal(t, sourceIDs[:1], branchMessageIDs(resp.Session.History),
		"include_anchor=false must stop the prefix before the anchor message")
	require.Equal(t, branchAnchorPayload{SourceMessageID: anchorID, TurnIndex: 0, Included: false}, resp.Anchor)
}

func TestBranchSessionDefaultAnchorUsesWholeSession(t *testing.T) {
	f := newBranchHandlerFixture(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
		*runtimetypes.NewUserMessage("second"),
		*runtimetypes.NewAssistantMessage("a2"),
	})
	sourceIDs := branchMessageIDs(f.source.GetMessages())

	rec := f.postBranch(t, f.source.ID, `{}`)
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
	resp := decodeBranchResponse(t, rec)

	require.Equal(t, sourceIDs, branchMessageIDs(resp.Session.History),
		"a missing anchor_message_id copies the whole session")
	// Default anchor shape: source_message_id empty, turn_index = last user turn.
	require.Equal(t, branchAnchorPayload{SourceMessageID: "", TurnIndex: 1, Included: true}, resp.Anchor)

	// With no anchor message there is nothing to drop, so include_anchor=false
	// keeps the documented whole-session default.
	rec = f.postBranch(t, f.source.ID, `{"include_anchor":false}`)
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
	resp = decodeBranchResponse(t, rec)
	require.Equal(t, sourceIDs, branchMessageIDs(resp.Session.History))
	require.Equal(t, branchAnchorPayload{SourceMessageID: "", TurnIndex: 1, Included: true}, resp.Anchor)
}

func TestBranchSessionRejectsAnchorInsideTurn(t *testing.T) {
	f := newBranchHandlerFixture(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
		*runtimetypes.NewAssistantMessage("a2"),
	})
	sourceIDs := branchMessageIDs(f.source.GetMessages())

	rec := f.postBranch(t, f.source.ID, fmt.Sprintf(`{"anchor_message_id":%q}`, sourceIDs[1]))
	require.Equal(t, http.StatusConflict, rec.Code, "body=%s", rec.Body.String())
	require.Contains(t, rec.Body.String(), "last message of a completed turn",
		"the conflict must explain that the anchor is not the turn tail")
}

func TestBranchSessionRejectsUserMessageAnchor(t *testing.T) {
	ctx := context.Background()
	f := newBranchHandlerFixture(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
		*runtimetypes.NewUserMessage("second"),
		*runtimetypes.NewAssistantMessage("a2"),
	})
	sourceIDs := branchMessageIDs(f.source.GetMessages())

	rec := f.postBranch(t, f.source.ID, fmt.Sprintf(`{"anchor_message_id":%q}`, sourceIDs[2]))
	require.Equal(t, http.StatusConflict, rec.Code, "body=%s", rec.Body.String())
	require.Contains(t, rec.Body.String(), "user message")

	// A rejected branch must not create a session.
	sessions, err := f.sessionManager.List(ctx, "branch-api-user")
	require.NoError(t, err)
	require.Len(t, sessions, 1)
}

func TestBranchSessionRejectsUnknownAnchor(t *testing.T) {
	ctx := context.Background()
	f := newBranchHandlerFixture(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
	})

	rec := f.postBranch(t, f.source.ID, `{"anchor_message_id":"msg_does_not_exist"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
	require.Contains(t, rec.Body.String(), "not part of the session history")

	sessions, err := f.sessionManager.List(ctx, "branch-api-user")
	require.NoError(t, err)
	require.Len(t, sessions, 1, "an unresolvable anchor must not leave a session behind")
}

func TestBranchSessionMissingSourceReturnsNotFound(t *testing.T) {
	f := newBranchHandlerFixture(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
	})

	rec := f.postBranch(t, "session_that_does_not_exist", `{}`)
	require.Equal(t, http.StatusNotFound, rec.Code, "body=%s", rec.Body.String())
}

func TestBranchSessionRejectsBusySource(t *testing.T) {
	ctx := context.Background()
	history := []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
	}
	busyState := func(ctx context.Context, store *chat.InMemoryRuntimeStore, sessionID string) {
		require.NoError(t, store.SaveState(ctx, &chat.RuntimeState{
			SessionID:     sessionID,
			Status:        chat.SessionRunning,
			CurrentTurnID: "busy",
			UpdatedAt:     time.Now().UTC(),
		}))
	}

	t.Run("actor state", func(t *testing.T) {
		f := newBranchHandlerFixtureWith(t, history, nil, busyState)
		sourceIDs := branchMessageIDs(f.source.GetMessages())
		rec := f.postBranch(t, f.source.ID, fmt.Sprintf(`{"anchor_message_id":%q}`, sourceIDs[1]))
		require.Equal(t, http.StatusConflict, rec.Code, "body=%s", rec.Body.String())
		require.Contains(t, rec.Body.String(), "generating")
	})

	t.Run("persisted runtime state without a loaded actor", func(t *testing.T) {
		f := newBranchHandlerFixture(t, history)
		// The actor is idle; only the shared runtime store reports the turn.
		busyState(ctx, f.runtimeStore, f.source.ID)
		sourceIDs := branchMessageIDs(f.source.GetMessages())
		rec := f.postBranch(t, f.source.ID, fmt.Sprintf(`{"anchor_message_id":%q}`, sourceIDs[1]))
		require.Equal(t, http.StatusConflict, rec.Code, "body=%s", rec.Body.String())
	})
}

func TestBranchSessionPreservesForkRoot(t *testing.T) {
	ctx := context.Background()
	f := newBranchHandlerFixture(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
		*runtimetypes.NewUserMessage("second"),
		*runtimetypes.NewAssistantMessage("a2"),
	})
	// The source is itself a branch of "root-session-1".
	f.source.SetContext(chat.ContextForkParentSessionID, "root-session-1")
	f.source.SetContext(chat.ContextForkRootSessionID, "root-session-1")
	require.NoError(t, f.storage.Update(ctx, f.source))

	sourceIDs := branchMessageIDs(f.source.GetMessages())
	rec := f.postBranch(t, f.source.ID, fmt.Sprintf(`{"anchor_message_id":%q}`, sourceIDs[3]))
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
	resp := decodeBranchResponse(t, rec)

	require.Equal(t, f.source.ID, sessionmeta.String(resp.Session.Metadata.Context, chat.ContextForkParentSessionID))
	require.Equal(t, "root-session-1", sessionmeta.String(resp.Session.Metadata.Context, chat.ContextForkRootSessionID),
		"branching a branch keeps the original fork root")
	require.Equal(t, sourceIDs[3], sessionmeta.String(resp.Session.Metadata.Context, chat.ContextForkSourceMessageID))
	require.Equal(t, sourceIDs, branchMessageIDs(resp.Session.History))
}

func TestBranchSessionTitleDeduplicatesWithinParent(t *testing.T) {
	ctx := context.Background()
	f := newBranchHandlerFixture(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
	})
	f.source.UpdateTitle("Source Title")
	require.NoError(t, f.storage.Update(ctx, f.source))
	sourceIDs := branchMessageIDs(f.source.GetMessages())
	anchorBody := fmt.Sprintf(`{"anchor_message_id":%q}`, sourceIDs[1])

	var generated []string
	for i := 0; i < 2; i++ {
		rec := f.postBranch(t, f.source.ID, anchorBody)
		require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
		generated = append(generated, decodeBranchResponse(t, rec).Session.Metadata.Title)
	}
	require.Equal(t, []string{"Source Title (branch)", "Source Title (branch) (2)"}, generated)

	// Client-supplied titles dedup the same way.
	var explicit []string
	for i := 0; i < 2; i++ {
		rec := f.postBranch(t, f.source.ID, fmt.Sprintf(`{"anchor_message_id":%q,"title":"Custom"}`, sourceIDs[1]))
		require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
		explicit = append(explicit, decodeBranchResponse(t, rec).Session.Metadata.Title)
	}
	require.Equal(t, []string{"Custom", "Custom (2)"}, explicit)
}

func TestBranchSessionHonorsExplicitUserID(t *testing.T) {
	f := newBranchHandlerFixture(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
	})
	sourceIDs := branchMessageIDs(f.source.GetMessages())

	rec := f.postBranch(t, f.source.ID,
		fmt.Sprintf(`{"anchor_message_id":%q,"user_id":"explicit-branch-user"}`, sourceIDs[1]))
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
	resp := decodeBranchResponse(t, rec)
	require.Equal(t, "explicit-branch-user", resp.Session.UserID)
}

func TestBranchSessionRollsBackWhenSeedWriteFails(t *testing.T) {
	ctx := context.Background()
	storage := &branchSeedFailStorage{InMemoryStorage: chat.NewInMemoryStorage()}
	f := newBranchHandlerFixtureWith(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
	}, storage, nil)

	before, err := f.sessionManager.List(ctx, "branch-api-user")
	require.NoError(t, err)
	require.Len(t, before, 1)

	sourceIDs := branchMessageIDs(f.source.GetMessages())
	rec := f.postBranch(t, f.source.ID, fmt.Sprintf(`{"anchor_message_id":%q}`, sourceIDs[1]))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body=%s", rec.Body.String())

	after, err := f.sessionManager.List(ctx, "branch-api-user")
	require.NoError(t, err)
	require.Len(t, after, 1, "a failed seed write must not leave a shell session behind")
	require.Equal(t, f.source.ID, after[0].ID)
}
