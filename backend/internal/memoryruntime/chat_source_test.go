package memoryruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestChatSessionSourceListsAndReadsCanonicalTranscript(t *testing.T) {
	manager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer manager.Stop()
	ctx := context.Background()
	session, err := manager.Create(ctx, "user-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	session.Metadata.Context[sessionmeta.WorkspacePath] = "/workspace/demo"
	session.AddMessage(*types.NewUserMessage("remember this project decision"))
	session.AddMessage(*types.NewToolMessage("call-1", strings.Repeat("x", 2000)))
	if err := manager.Update(ctx, session); err != nil {
		t.Fatalf("Update: %v", err)
	}
	source := &ChatSessionSource{Manager: manager, UserID: "user-1", MaxToolOutputRunes: 100}
	sessions, err := source.ListSessions(ctx, "/workspace/demo", 10)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("ListSessions = %d, %v", len(sessions), err)
	}
	if sessions[0].WorkspacePath != "/workspace/demo" || sessions[0].IsSubagent {
		t.Fatalf("unexpected session ref: %+v", sessions[0])
	}
	transcript, err := source.ReadTranscript(ctx, sessions[0], 10)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if len(transcript.Messages) != 2 || transcript.Hash == "" || transcript.Revision < 2 {
		t.Fatalf("unexpected transcript: %+v", transcript)
	}
	if !transcript.Messages[1].Truncated {
		t.Fatal("large tool output must be truncated")
	}
	if len([]rune(transcript.Messages[1].Content)) > 140 {
		t.Fatalf("tool output not bounded: %d runes", len([]rune(transcript.Messages[1].Content)))
	}
}

func TestChatSessionSourceMarksSubagent(t *testing.T) {
	manager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer manager.Stop()
	ctx := context.Background()
	session, err := manager.Create(ctx, "user-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	session.Metadata.Context[sessionmeta.WorkspacePath] = "/workspace/demo"
	session.Metadata.Context[agentcontrol.SessionContextParentSessionID] = "parent"
	session.Metadata.Context[agentcontrol.SessionContextAgentType] = "worker"
	if err := manager.Update(ctx, session); err != nil {
		t.Fatalf("Update: %v", err)
	}
	source := &ChatSessionSource{Manager: manager, UserID: "user-1"}
	sessions, err := source.ListSessions(ctx, "/workspace/demo", 10)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("ListSessions = %d, %v", len(sessions), err)
	}
	if !sessions[0].IsSubagent {
		t.Fatalf("expected subagent ref: %+v", sessions[0])
	}
}

func TestChatSessionSourceReadsTranscriptWindow(t *testing.T) {
	manager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer manager.Stop()
	ctx := context.Background()
	session, err := manager.Create(ctx, "user-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	session.Metadata.Context[sessionmeta.WorkspacePath] = "/workspace/demo"
	session.AddMessage(*types.NewUserMessage("first question"))
	session.AddMessage(*types.NewAssistantMessage("first answer"))
	session.AddMessage(*types.NewUserMessage("second question"))
	session.AddMessage(*types.NewAssistantMessage("second answer"))
	if err := manager.Update(ctx, session); err != nil {
		t.Fatalf("Update: %v", err)
	}
	source := &ChatSessionSource{Manager: manager, UserID: "user-1"}
	transcript, err := source.ReadTranscriptWindow(ctx, SessionRef{
		ID: session.ID, WorkspacePath: "/workspace/demo",
	}, 3, 4, 10)
	if err != nil {
		t.Fatalf("ReadTranscriptWindow: %v", err)
	}
	if transcript.Revision != 4 {
		t.Fatalf("revision = %d, want 4", transcript.Revision)
	}
	if len(transcript.Messages) != 2 {
		t.Fatalf("window messages = %d, want 2: %+v", len(transcript.Messages), transcript.Messages)
	}
	if transcript.Messages[0].Role != "user" || transcript.Messages[0].Content != "second question" {
		t.Fatalf("unexpected first window message: %+v", transcript.Messages[0])
	}
	if transcript.Messages[1].Role != "assistant" || transcript.Messages[1].Content != "second answer" {
		t.Fatalf("unexpected second window message: %+v", transcript.Messages[1])
	}
	if transcript.Hash == "" {
		t.Fatal("window transcript hash is empty")
	}
}
