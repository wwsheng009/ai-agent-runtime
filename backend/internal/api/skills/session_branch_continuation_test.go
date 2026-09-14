package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// branchContinuationProvider is a scripted LLM provider that records every
// request it serves, so the test can assert on the exact context handed to the
// model without contacting a real provider.
type branchContinuationProvider struct {
	name string

	mu       sync.Mutex
	requests []*llm.LLMRequest
}

func (p *branchContinuationProvider) Name() string { return p.name }

func (p *branchContinuationProvider) Call(_ context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.mu.Lock()
	if req != nil {
		p.requests = append(p.requests, branchContinuationCloneRequest(req))
	}
	p.mu.Unlock()
	return &llm.LLMResponse{Content: "branch-continuation-reply", Model: p.name}, nil
}

func (p *branchContinuationProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	response, err := p.Call(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan llm.StreamChunk, 2)
	go func() {
		defer close(ch)
		ch <- llm.StreamChunk{Type: llm.EventTypeText, Content: response.Content}
		ch <- llm.StreamChunk{Type: llm.EventTypeDone, Done: true}
	}()
	return ch, nil
}

func (p *branchContinuationProvider) CountTokens(text string) int { return len(text) / 4 }

func (p *branchContinuationProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
	}
}

func (p *branchContinuationProvider) CheckHealth(context.Context) error { return nil }

// capturedRequests returns a snapshot copy of the recorded requests.
func (p *branchContinuationProvider) capturedRequests() []*llm.LLMRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*llm.LLMRequest(nil), p.requests...)
}

func branchContinuationCloneRequest(req *llm.LLMRequest) *llm.LLMRequest {
	cloned := &llm.LLMRequest{
		Provider: req.Provider,
		Model:    req.Model,
	}
	if len(req.Messages) > 0 {
		cloned.Messages = make([]runtimetypes.Message, len(req.Messages))
		for index := range req.Messages {
			cloned.Messages[index] = *req.Messages[index].Clone()
		}
	}
	return cloned
}

// branchContinuationMessageIndex resolves the first message with the given role
// and content, or -1. Message order is the assertion target: the cloned prefix
// must precede the freshly submitted prompt.
func branchContinuationMessageIndex(messages []runtimetypes.Message, role, content string) int {
	for index, message := range messages {
		if message.Role == role && strings.Contains(message.Content, content) {
			return index
		}
	}
	return -1
}

// TestBranchSessionContinuationWithoutActorPrewarm is the explicit backend
// verification for plan §10 Q9: a session minted by
// POST /api/runtime/sessions/{id}/branch must accept its next prompt through the
// regular runtime command path with no actor pre-warm/pre-registration, and the
// model request for that first turn must carry the cloned history prefix.
//
// Observable facts asserted here:
//  1. after a successful branch the hub holds no actor for the new session and
//     the hub factory was never invoked for it (no warm-up/registration);
//  2. the first submit_prompt returns 200 through the lazily built actor;
//  3. the first LLM request contains the prefix messages cloned by the branch,
//     in order, ahead of the new prompt, and excludes the messages after the
//     anchor;
//  4. the actor is registered only after that first prompt, the persisted
//     branch history keeps the prefix, and the source session is untouched.
func TestBranchSessionContinuationWithoutActorPrewarm(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)

	source, err := sessionManager.Create(ctx, "branch-continuation-user")
	require.NoError(t, err)
	for _, message := range []runtimetypes.Message{
		*runtimetypes.NewUserMessage("branch-prefix-user-one"),
		*runtimetypes.NewAssistantMessage("branch-prefix-assistant-one"),
		*runtimetypes.NewUserMessage("branch-prefix-user-two"),
		*runtimetypes.NewAssistantMessage("branch-prefix-assistant-two"),
	} {
		source.AddMessage(message)
	}
	require.NoError(t, storage.Update(ctx, source))

	// The anchor is the tail of the first turn: the branch prefix is exactly the
	// first two messages, so the second turn must not leak into the branch.
	sourceIDs := branchMessageIDs(source.GetMessages())
	require.Len(t, sourceIDs, 4)
	anchorID := sourceIDs[1]

	provider := &branchContinuationProvider{name: "branch-continuation-model"}
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: provider.Name(),
		MaxRetries:   0,
	})
	require.NoError(t, llmRuntime.RegisterProvider(provider.Name(), provider))

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)
	handler.SetLLMRuntime(llmRuntime)
	// Pin the in-memory runtime/event stores so buildSessionActor never opens a
	// real (file-backed) store during the test.
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	// The hub uses the production actor factory (Handler.buildSessionActor, the
	// same one getSessionHub wires) so the lazily built actor is exactly the one
	// the server would run. hubBuilds records every construction so "not
	// pre-registered" becomes an observable fact rather than an assumption.
	var hubMu sync.Mutex
	hubBuilds := map[string]int{}
	hub := chat.NewBoundedSessionHub(func(sessionID string) (*chat.SessionActor, error) {
		hubMu.Lock()
		hubBuilds[sessionID]++
		hubMu.Unlock()
		return handler.buildSessionActor(sessionID)
	})
	defer hub.StopAll()
	handler.sessionHub = hub

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	// Step 1: branch the source session and capture the new session id.
	branchRequest := httptest.NewRequest(http.MethodPost,
		"/api/runtime/sessions/"+source.ID+"/branch",
		strings.NewReader(fmt.Sprintf(`{"anchor_message_id":%q}`, anchorID)))
	branchRequest.Header.Set("Content-Type", "application/json")
	branchRecorder := httptest.NewRecorder()
	router.ServeHTTP(branchRecorder, branchRequest)
	require.Equal(t, http.StatusCreated, branchRecorder.Code, "body=%s", branchRecorder.Body.String())

	var branchResponse struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	require.NoError(t, json.NewDecoder(branchRecorder.Body).Decode(&branchResponse))
	branchID := strings.TrimSpace(branchResponse.Session.ID)
	require.NotEmpty(t, branchID)
	require.NotEqual(t, source.ID, branchID)

	// Step 2: the branch endpoint must leave the actor hub cold for the new
	// session. This is the "无需预热/注册" half of Q9.
	_, registered := hub.Get(branchID)
	require.False(t, registered, "branch creation must not pre-register a session actor")
	hubMu.Lock()
	buildsBeforePrompt := hubBuilds[branchID]
	hubMu.Unlock()
	require.Zero(t, buildsBeforePrompt, "branch creation must not build a session actor")

	// Step 3: the very first prompt goes through the frontend's runtime command
	// endpoint and must be served by the lazily created actor.
	promptRequest := httptest.NewRequest(http.MethodPost,
		"/api/runtime/sessions/"+branchID+"/runtime/commands",
		strings.NewReader(`{"type":"submit_prompt","prompt":"branch-continuation-follow-up"}`))
	promptRequest.Header.Set("Content-Type", "application/json")
	promptRecorder := httptest.NewRecorder()
	router.ServeHTTP(promptRecorder, promptRequest)
	require.Equal(t, http.StatusOK, promptRecorder.Code, "body=%s", promptRecorder.Body.String())
	require.Contains(t, promptRecorder.Body.String(), "branch-continuation-reply")

	// Step 4: the first model request must contain the cloned prefix in order,
	// ahead of the new prompt, and nothing from after the anchor.
	requests := provider.capturedRequests()
	require.NotEmpty(t, requests, "the first prompt must reach the model")
	firstRequest := requests[0]
	require.NotEmpty(t, firstRequest.Messages, "the model request must carry the assembled context")

	prefixUserIndex := branchContinuationMessageIndex(firstRequest.Messages, "user", "branch-prefix-user-one")
	prefixAssistantIndex := branchContinuationMessageIndex(firstRequest.Messages, "assistant", "branch-prefix-assistant-one")
	promptIndex := branchContinuationMessageIndex(firstRequest.Messages, "user", "branch-continuation-follow-up")
	require.GreaterOrEqual(t, prefixUserIndex, 0,
		"first model request must contain the cloned user prefix; messages=%v", branchContinuationRoles(firstRequest.Messages))
	require.GreaterOrEqual(t, prefixAssistantIndex, 0,
		"first model request must contain the cloned assistant prefix; messages=%v", branchContinuationRoles(firstRequest.Messages))
	require.GreaterOrEqual(t, promptIndex, 0, "first model request must contain the new prompt")
	require.Less(t, prefixUserIndex, promptIndex, "the cloned prefix must precede the new prompt")
	require.Less(t, prefixAssistantIndex, promptIndex, "the cloned assistant reply must precede the new prompt")
	require.Equal(t, -1, branchContinuationMessageIndex(firstRequest.Messages, "user", "branch-prefix-user-two"),
		"messages after the branch anchor must not be part of the branch context")
	require.Equal(t, -1, branchContinuationMessageIndex(firstRequest.Messages, "assistant", "branch-prefix-assistant-two"),
		"messages after the branch anchor must not be part of the branch context")

	// Step 5: registration happens lazily on the first prompt, and the branch
	// keeps both its prefix and its fork lineage across the new turn.
	_, registered = hub.Get(branchID)
	require.True(t, registered, "the hub must register the actor lazily on the first prompt")
	hubMu.Lock()
	buildsAfterPrompt := hubBuilds[branchID]
	hubMu.Unlock()
	require.Equal(t, 1, buildsAfterPrompt, "the branch session actor must be built exactly once, on demand")

	persistedBranch, err := sessionManager.Get(ctx, branchID)
	require.NoError(t, err)
	require.Equal(t, sourceIDs[:2], branchMessageIDs(persistedBranch.GetMessages())[:2],
		"persisted branch history must still start with the cloned prefix")
	lastMessage := persistedBranch.GetMessages()[len(persistedBranch.GetMessages())-1]
	require.Equal(t, "assistant", lastMessage.Role, "the first turn must complete on the branch")
	require.Equal(t, "branch-continuation-reply", lastMessage.Content)
	require.Equal(t, source.ID, sessionmeta.String(persistedBranch.Metadata.Context, chat.ContextForkParentSessionID))
	require.Equal(t, anchorID, sessionmeta.String(persistedBranch.Metadata.Context, chat.ContextForkSourceMessageID))

	// The source session stays untouched by both the branch and the continuation.
	persistedSource, err := sessionManager.Get(ctx, source.ID)
	require.NoError(t, err)
	require.Equal(t, sourceIDs, branchMessageIDs(persistedSource.GetMessages()))
}

// branchContinuationRoles renders the role/content shape of a request, keeping
// failure output readable without dumping full prompts.
func branchContinuationRoles(messages []runtimetypes.Message) []string {
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		content := message.Content
		if len(content) > 40 {
			content = content[:40] + "…"
		}
		roles = append(roles, message.Role+":"+content)
	}
	return roles
}
