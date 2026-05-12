package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func TestListSessionUsersAggregatesUsersFromSessionStore(t *testing.T) {
	ctx := context.Background()
	manager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer manager.Stop()

	first, err := manager.Create(ctx, "thinkbook14\\wangweisheng")
	if err != nil {
		t.Fatalf("create first session: %v", err)
	}
	second, err := manager.Create(ctx, "thinkbook14\\wangweisheng")
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}
	second.UpdateState(chat.StateArchived)
	if err := manager.Update(ctx, second); err != nil {
		t.Fatalf("archive second session: %v", err)
	}
	other, err := manager.Create(ctx, "web-console:local:client")
	if err != nil {
		t.Fatalf("create other session: %v", err)
	}
	other.UpdateState(chat.StateIdle)
	if err := manager.Update(ctx, other); err != nil {
		t.Fatalf("idle other session: %v", err)
	}
	if first.ID == "" {
		t.Fatal("expected created session id")
	}

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Sessions.DefaultUserID = "anonymous"
	handler := NewHandler(nil, nil, nil)
	handler.SetSessionManager(manager)
	handler.SetRuntimeConfig(cfg, "")

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/users", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Users []struct {
			UserID           string `json:"user_id"`
			DisplayName      string `json:"display_name"`
			Source           string `json:"source"`
			SessionCount     int    `json:"session_count"`
			ActiveCount      int    `json:"active_count"`
			IdleCount        int    `json:"idle_count"`
			ArchivedCount    int    `json:"archived_count"`
			RecoverableCount int    `json:"recoverable_count"`
			LatestUpdatedAt  string `json:"latest_updated_at"`
		} `json:"users"`
		Count         int    `json:"count"`
		TotalCount    int    `json:"total_count"`
		DefaultUserID string `json:"default_user_id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Count != 2 || resp.TotalCount != 2 || len(resp.Users) != 2 {
		t.Fatalf("expected two user summaries, got %#v", resp)
	}
	if resp.DefaultUserID != "anonymous" {
		t.Fatalf("expected default user id anonymous, got %q", resp.DefaultUserID)
	}

	byUser := make(map[string]struct {
		SessionCount     int
		ActiveCount      int
		IdleCount        int
		ArchivedCount    int
		RecoverableCount int
		LatestUpdatedAt  string
	}, len(resp.Users))
	for _, user := range resp.Users {
		if user.DisplayName != user.UserID {
			t.Fatalf("expected display name to default to user id: %#v", user)
		}
		if user.Source != "session_store" {
			t.Fatalf("expected session_store source, got %#v", user)
		}
		byUser[user.UserID] = struct {
			SessionCount     int
			ActiveCount      int
			IdleCount        int
			ArchivedCount    int
			RecoverableCount int
			LatestUpdatedAt  string
		}{
			SessionCount:     user.SessionCount,
			ActiveCount:      user.ActiveCount,
			IdleCount:        user.IdleCount,
			ArchivedCount:    user.ArchivedCount,
			RecoverableCount: user.RecoverableCount,
			LatestUpdatedAt:  user.LatestUpdatedAt,
		}
	}
	local := byUser["thinkbook14\\wangweisheng"]
	if local.SessionCount != 2 || local.ActiveCount != 1 || local.ArchivedCount != 1 || local.RecoverableCount != 1 {
		t.Fatalf("unexpected local user summary: %#v", local)
	}
	web := byUser["web-console:local:client"]
	if web.SessionCount != 1 || web.IdleCount != 1 || web.RecoverableCount != 1 {
		t.Fatalf("unexpected web user summary: %#v", web)
	}
	if local.LatestUpdatedAt == "" || web.LatestUpdatedAt == "" {
		t.Fatalf("expected latest_updated_at for all users: %#v", byUser)
	}
}
