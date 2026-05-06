package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func TestListMailboxHandlerMarksMessagesReadForAgent(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:team-mailbox-handler?mode=memory&cache=shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	_, err = store.InsertMail(ctx, team.MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "*",
		Kind:      "info",
		Body:      "broadcast",
	})
	require.NoError(t, err)
	_, err = store.InsertMail(ctx, team.MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-1",
		Kind:      "question",
		Body:      "direct",
	})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/teams/"+teamID+"/mailbox?to_agent=mate-1&include_broadcast=true&unread_only=true&mark_read=true&agent_id=mate-1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		MarkedRead bool               `json:"marked_read"`
		AgentID    string             `json:"agent_id"`
		Messages   []team.MailMessage `json:"messages"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.True(t, resp.MarkedRead)
	require.Equal(t, "mate-1", resp.AgentID)
	require.Len(t, resp.Messages, 2)

	unread, err := store.ListMail(ctx, team.MailFilter{
		TeamID:           teamID,
		ToAgent:          "mate-1",
		IncludeBroadcast: true,
		UnreadOnly:       true,
	})
	require.NoError(t, err)
	require.Len(t, unread, 0)
}

func TestListMailboxHandlerSupportsAfterSeq(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:team-mailbox-after-seq-handler?mode=memory&cache=shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	firstID, err := store.InsertMail(ctx, team.MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-1",
		Kind:      "info",
		Body:      "first",
	})
	require.NoError(t, err)
	secondID, err := store.InsertMail(ctx, team.MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-1",
		Kind:      "info",
		Body:      "second",
	})
	require.NoError(t, err)

	all, err := store.ListMail(ctx, team.MailFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, secondID, all[0].ID)
	require.Equal(t, firstID, all[1].ID)
	firstSeq := all[1].Seq

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/teams/"+teamID+"/mailbox?to_agent=mate-1&after_seq="+strconv.FormatInt(firstSeq, 10), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Count    int                `json:"count"`
		Messages []team.MailMessage `json:"messages"`
		Filters  struct {
			AfterSeq int64 `json:"after_seq"`
		} `json:"filters"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, 1, resp.Count)
	require.Len(t, resp.Messages, 1)
	require.Equal(t, secondID, resp.Messages[0].ID)
	require.Greater(t, resp.Messages[0].Seq, firstSeq)
	require.Equal(t, firstSeq, resp.Filters.AfterSeq)
}
