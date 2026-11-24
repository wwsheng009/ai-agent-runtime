package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
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
	req.RemoteAddr = "127.0.0.1:4321"
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
	req.RemoteAddr = "127.0.0.1:4321"
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

func TestAckMailboxHandlerScopesReceiptsAndDeduplicatesReplayEvents(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:team-mailbox-ack-contract?mode=memory&cache=shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	agentMessageID, err := store.InsertMail(ctx, team.MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "*",
		Kind:      "info",
		Body:      "broadcast-for-agent-receipt",
	})
	require.NoError(t, err)
	globalMessageID, err := store.InsertMail(ctx, team.MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "*",
		Kind:      "info",
		Body:      "global",
	})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	ack := func(messageID, agentID string) *httptest.ResponseRecorder {
		t.Helper()
		target := "/api/runtime/teams/" + teamID + "/mailbox/" + messageID + "/ack"
		if agentID != "" {
			target += "?agent_id=" + agentID
		}
		request := httptest.NewRequest(http.MethodPost, target, nil)
		request.RemoteAddr = "127.0.0.1:4321"
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	decodeAck := func(recorder *httptest.ResponseRecorder) struct {
		MessageID string `json:"message_id"`
		TeamID    string `json:"team_id"`
		AgentID   string `json:"agent_id"`
		Replayed  bool   `json:"replayed"`
	} {
		t.Helper()
		var response struct {
			MessageID string `json:"message_id"`
			TeamID    string `json:"team_id"`
			AgentID   string `json:"agent_id"`
			Replayed  bool   `json:"replayed"`
		}
		require.NoError(t, json.NewDecoder(recorder.Body).Decode(&response))
		return response
	}
	ackEvents := func() []runtimeevents.Event {
		return handler.getRuntimeEventBus().Query(runtimeevents.QueryFilter{
			EventType: "team.mailbox.acked",
			TeamID:    teamID,
			Limit:     20,
		})
	}

	firstAgentAck := ack(agentMessageID, "mate-a")
	require.Equal(t, http.StatusOK, firstAgentAck.Code, firstAgentAck.Body.String())
	firstAgentPayload := decodeAck(firstAgentAck)
	require.Equal(t, agentMessageID, firstAgentPayload.MessageID)
	require.Equal(t, teamID, firstAgentPayload.TeamID)
	require.Equal(t, "mate-a", firstAgentPayload.AgentID)
	require.False(t, firstAgentPayload.Replayed)

	agentAUnread, err := store.ListMail(ctx, team.MailFilter{
		TeamID:           teamID,
		ToAgent:          "mate-a",
		IncludeBroadcast: true,
		UnreadOnly:       true,
	})
	require.NoError(t, err)
	require.Len(t, agentAUnread, 1)
	require.Equal(t, globalMessageID, agentAUnread[0].ID)
	agentBUnread, err := store.ListMail(ctx, team.MailFilter{
		TeamID:           teamID,
		ToAgent:          "mate-b",
		IncludeBroadcast: true,
		UnreadOnly:       true,
	})
	require.NoError(t, err)
	require.Len(t, agentBUnread, 2, "one agent's receipt must not acknowledge mail for another agent")

	agentReceipt, err := store.ListMailReceipts(ctx, teamID, agentMessageID)
	require.NoError(t, err)
	require.Len(t, agentReceipt, 1)
	require.Equal(t, "mate-a", agentReceipt[0].AgentID)

	replayedAgentAck := ack(agentMessageID, "mate-a")
	require.Equal(t, http.StatusOK, replayedAgentAck.Code, replayedAgentAck.Body.String())
	require.True(t, decodeAck(replayedAgentAck).Replayed)
	agentReceiptAfterReplay, err := store.ListMailReceipts(ctx, teamID, agentMessageID)
	require.NoError(t, err)
	require.Len(t, agentReceiptAfterReplay, 1)
	require.Equal(t, agentReceipt[0].AckedAt, agentReceiptAfterReplay[0].AckedAt,
		"replaying an acknowledgement must not rewrite the original receipt")
	require.Len(t, ackEvents(), 1, "a replay must not publish a duplicate ack event")

	globalAck := ack(globalMessageID, "")
	require.Equal(t, http.StatusOK, globalAck.Code, globalAck.Body.String())
	globalPayload := decodeAck(globalAck)
	require.Equal(t, globalMessageID, globalPayload.MessageID)
	require.Empty(t, globalPayload.AgentID)
	require.False(t, globalPayload.Replayed)
	globalReceipts, err := store.ListMailReceipts(ctx, teamID, globalMessageID)
	require.NoError(t, err)
	require.Len(t, globalReceipts, 1)
	globalMail, err := store.ListMail(ctx, team.MailFilter{TeamID: teamID})
	require.NoError(t, err)
	for _, message := range globalMail {
		if message.ID == globalMessageID {
			require.NotNil(t, message.AckedAt, "global acknowledgement must update the shared mailbox read model")
		}
	}

	replayedGlobalAck := ack(globalMessageID, "")
	require.Equal(t, http.StatusOK, replayedGlobalAck.Code, replayedGlobalAck.Body.String())
	require.True(t, decodeAck(replayedGlobalAck).Replayed)
	require.Len(t, ackEvents(), 2, "only the first per-agent ack and first global ack publish events")

	missingAck := ack("missing-message", "mate-a")
	require.Equal(t, http.StatusNotFound, missingAck.Code, missingAck.Body.String())
	require.Len(t, ackEvents(), 2, "a rejected acknowledgement must not publish an event")
}
