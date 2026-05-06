package commands

import (
	"context"
	"testing"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestParseChatCommandOptions_YoloImpliesBypassPermissions(t *testing.T) {
	cmd := newChatOptionsTestCommand()
	_ = cmd.Flags().Set("yolo", "true")

	opts, err := parseChatCommandOptions(cmd, &config.Config{})
	if err != nil {
		t.Fatalf("parseChatCommandOptions: %v", err)
	}
	if opts.PermissionMode != runtimepolicy.ModeBypassPermissions {
		t.Fatalf("expected bypass permissions, got %s", opts.PermissionMode)
	}
}

func TestSyncRuntimeSessionFromChat_PersistsAmbientTeamBinding(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	session := &ChatSession{
		ProviderName:   "alpha",
		Provider:       config.Provider{Protocol: "openai"},
		Model:          "gpt-4.1",
		SessionManager: manager,
		RuntimeSession: runtimeSession,
		SessionUserID:  userID,
		SessionDir:     dir,
		PermissionMode: runtimepolicy.ModeBypassPermissions,
		ActiveTeam: &chatTeamBinding{
			TeamID:  "team-1",
			AgentID: "lead",
			TaskID:  "task-1",
		},
	}

	if err := syncRuntimeSessionFromChat(session); err != nil {
		t.Fatalf("syncRuntimeSessionFromChat: %v", err)
	}
	stored, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	if got := runtimeSessionContextString(stored, chatRuntimeContextActiveTeamID); got != "team-1" {
		t.Fatalf("unexpected active team id: %q", got)
	}
	if got := runtimeSessionContextString(stored, chatRuntimeContextActiveAgentID); got != "lead" {
		t.Fatalf("unexpected active agent id: %q", got)
	}
	if got := runtimeSessionContextString(stored, chatRuntimeContextActiveTaskID); got != "task-1" {
		t.Fatalf("unexpected active task id: %q", got)
	}
	if got := runtimeSessionContextString(stored, chatRuntimeContextPermissionMode); got != string(runtimepolicy.ModeBypassPermissions) {
		t.Fatalf("unexpected permission mode: %q", got)
	}
}

func TestChatRuntimeContext_RoundTripsSelectedAgentTarget(t *testing.T) {
	runtimeSession := runtimechat.NewSession("tester")
	session := &ChatSession{
		PermissionMode:      runtimepolicy.ModeAcceptEdits,
		SelectedAgentTarget: " /root/agent-1 ",
	}

	syncChatRuntimeContext(session, runtimeSession)
	if got := runtimeSessionContextString(runtimeSession, chatRuntimeContextSelectedAgent); got != "/root/agent-1" {
		t.Fatalf("unexpected selected agent target context: %q", got)
	}

	restored := &ChatSession{}
	restoreChatRuntimeContext(restored, runtimeSession)
	if restored.SelectedAgentTarget != "/root/agent-1" {
		t.Fatalf("expected selected target to restore, got %q", restored.SelectedAgentTarget)
	}

	session.SelectedAgentTarget = ""
	syncChatRuntimeContext(session, runtimeSession)
	if got := runtimeSessionContextString(runtimeSession, chatRuntimeContextSelectedAgent); got != "" {
		t.Fatalf("expected selected agent target context to be cleared, got %q", got)
	}
}

func TestRestoreAmbientTeamBinding_ClearsMissingTeamAndStaleTask(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-1",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	taskID, err := store.CreateTask(context.Background(), team.Task{
		ID:     "task-1",
		TeamID: teamID,
		Title:  "Task 1",
		Goal:   "Do task 1",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	session := &ChatSession{
		PermissionMode: runtimepolicy.ModeDefault,
		ActiveTeam: &chatTeamBinding{
			TeamID:  teamID,
			AgentID: "lead",
			TaskID:  taskID,
		},
	}
	validateAmbientTeamBinding(session, store)
	if session.ActiveTeam == nil || session.ActiveTeam.TaskID != taskID {
		t.Fatalf("expected valid binding to stay intact: %+v", session.ActiveTeam)
	}

	session.ActiveTeam.TaskID = "missing-task"
	validateAmbientTeamBinding(session, store)
	if session.ActiveTeam == nil {
		t.Fatal("expected team binding to remain after stale task cleanup")
	}
	if session.ActiveTeam.TaskID != "" {
		t.Fatalf("expected stale task id to be cleared, got %+v", session.ActiveTeam)
	}

	session.ActiveTeam = &chatTeamBinding{TeamID: "missing-team", AgentID: "lead"}
	validateAmbientTeamBinding(session, store)
	if session.ActiveTeam != nil {
		t.Fatalf("expected missing team to clear binding, got %+v", session.ActiveTeam)
	}
}

func TestInferAmbientTeamBinding_FromSpawnTeamToolResult(t *testing.T) {
	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ReplaceHistory([]runtimetypes.Message{
		{
			Role: "assistant",
			ToolCalls: []runtimetypes.ToolCall{
				{ID: "call-1", Name: toolbroker.ToolSpawnTeam},
			},
			Metadata: runtimetypes.NewMetadata(),
		},
		{
			Role:       "tool",
			ToolCallID: "call-1",
			Content:    `{"team_id":"team-99","created_team":true}`,
			Metadata:   runtimetypes.NewMetadata(),
		},
	})

	session := &ChatSession{PermissionMode: runtimepolicy.ModeAcceptEdits}
	inferAmbientTeamBinding(session, runtimeSession)

	if session.ActiveTeam == nil {
		t.Fatal("expected inferred active team")
	}
	if session.ActiveTeam.TeamID != "team-99" || session.ActiveTeam.AgentID != "lead" {
		t.Fatalf("unexpected inferred binding: %+v", session.ActiveTeam)
	}
}

func TestInferAmbientTeamBinding_FromSpawnTeamToolMetadata(t *testing.T) {
	runtimeSession := runtimechat.NewSession("tester")
	messageMeta := runtimetypes.NewMetadata()
	messageMeta["tool_metadata"] = map[string]interface{}{
		"team_id": "team-42",
		"task_id": "task-42",
	}
	runtimeSession.ReplaceHistory([]runtimetypes.Message{
		{
			Role: "assistant",
			ToolCalls: []runtimetypes.ToolCall{
				{ID: "call-1", Name: toolbroker.ToolSpawnTeam},
			},
			Metadata: runtimetypes.NewMetadata(),
		},
		{
			Role:       "tool",
			ToolCallID: "call-1",
			Content:    "Parsed JSON object with 2 keys.\nKeys: team_id, task_id",
			Metadata:   messageMeta,
		},
	})

	session := &ChatSession{PermissionMode: runtimepolicy.ModeAcceptEdits}
	inferAmbientTeamBinding(session, runtimeSession)

	if session.ActiveTeam == nil {
		t.Fatal("expected inferred active team")
	}
	if session.ActiveTeam.TeamID != "team-42" || session.ActiveTeam.TaskID != "task-42" || session.ActiveTeam.AgentID != "lead" {
		t.Fatalf("unexpected inferred binding: %+v", session.ActiveTeam)
	}
}

func newChatOptionsTestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "chat"}
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("agent", "", "")
	cmd.Flags().String("provider", "", "")
	cmd.Flags().String("model", "", "")
	cmd.Flags().Bool("stream", false, "")
	cmd.Flags().Bool("no-interactive", false, "")
	cmd.Flags().String("message", "", "")
	cmd.Flags().String("log-dir", "", "")
	cmd.Flags().String("request-timeout", "", "")
	cmd.Flags().String("reasoning-effort", "", "")
	cmd.Flags().Bool("disable-tools", false, "")
	cmd.Flags().Bool("debug-http", false, "")
	cmd.Flags().Bool("fail-fast", false, "")
	cmd.Flags().StringSlice("skills-dir", nil, "")
	cmd.Flags().Int("skills-top-k", 0, "")
	cmd.Flags().String("skills-mode", "auto", "")
	cmd.Flags().Bool("skills-debug", false, "")
	cmd.Flags().String("permission-mode", "default", "")
	cmd.Flags().Bool("yolo", false, "")
	cmd.Flags().String("output", "", "")
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("envelope", false, "")
	cmd.Flags().String("session", "", "")
	cmd.Flags().Bool("resume", false, "")
	cmd.Flags().Bool("list-sessions", false, "")
	cmd.Flags().String("session-dir", "", "")
	cmd.Flags().String("title", "", "")
	cmd.Flags().String("session-state", "", "")
	cmd.Flags().String("session-provider", "", "")
	cmd.Flags().String("session-model", "", "")
	cmd.Flags().String("session-query", "", "")
	cmd.Flags().Int("session-limit", 20, "")
	return cmd
}
