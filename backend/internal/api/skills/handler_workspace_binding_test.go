package skills

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspaceregistry"
)

// TestAgentChatInvalidWorkspacePathDoesNotBindSession locks in the ordering
// fix: workspace_path is only materialized on the durable session after the
// workspace scan succeeded. Previously an invalid path was bound before
// validation, so every later turn without an explicit workspace_path reused
// the bad directory and failed again (the session stayed stuck at 400).
func TestAgentChatInvalidWorkspacePathDoesNotBindSession(t *testing.T) {
	storage, err := chat.NewFileStorage(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	manager := chat.NewSessionManager(storage, chat.DefaultSessionManagerConfig())
	defer manager.Stop()

	session, err := manager.Create(context.Background(), "workspace-user")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	handler := NewHandler(nil, nil, nil)
	handler.SetSessionManager(manager)

	missingDir := filepath.Join(t.TempDir(), "missing-workspace")
	body := fmt.Sprintf(
		`{"messages":[{"role":"user","content":"hello"}],"session_id":%q,"user_id":"workspace-user","workspace_path":%q}`,
		session.ID, missingDir,
	)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.AgentChat(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid workspace_path, got %d body=%s", rec.Code, rec.Body.String())
	}

	reloaded, err := manager.Get(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if bound := sessionmeta.String(reloaded.Metadata.Context, sessionmeta.WorkspacePath); bound != "" {
		t.Fatalf("invalid workspace_path must not be bound to the session, got %q", bound)
	}
}

// TestWorkspaceDirectoryRegistryConcurrentAccess exercises the lazy registry
// accessor from multiple goroutines (run with -race): the previous fast-path
// field read raced with the assignment performed inside sync.Once.Do.
func TestWorkspaceDirectoryRegistryConcurrentAccess(t *testing.T) {
	handler := NewHandler(nil, nil, nil)

	const workers = 8
	results := make([]*workspaceregistry.Store, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index] = handler.workspaceDirectoryRegistry()
		}(i)
	}
	wg.Wait()

	for i, store := range results {
		if store == nil {
			t.Fatalf("worker %d got nil registry", i)
		}
		if store != results[0] {
			t.Fatalf("worker %d observed a different registry instance", i)
		}
	}
}
