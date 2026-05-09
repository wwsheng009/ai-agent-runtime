package agentcontrol

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistryServiceUsesSingleSQLiteDBForMailboxAndAgents(t *testing.T) {
	ctx := context.Background()
	storePath := filepath.Join(t.TempDir(), "agent-control.sqlite")
	service, err := NewRegistryService(ctx, RegistryServiceConfig{StorePath: storePath})
	if err != nil {
		t.Fatalf("NewRegistryService: %v", err)
	}
	defer service.Close()
	if service.MailboxStore == nil {
		t.Fatal("expected mailbox store")
	}
	if service.AgentStore == nil {
		t.Fatal("expected agent store")
	}

	mailboxRecord, err := service.MailboxStore.AppendPrimaryGlobalMailboxRecord(ctx, MailboxRecord{
		Workflow:  WorkflowSpawnAgent,
		Scope:     MailboxScopeSession,
		SessionID: "root-session",
		MessageID: "message-1",
		FromAgent: "child",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "hello",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("AppendPrimaryGlobalMailboxRecord: %v", err)
	}
	if mailboxRecord.GlobalSeq <= 0 {
		t.Fatalf("expected global mailbox sequence, got %#v", mailboxRecord)
	}

	agentRecord, err := service.AgentStore.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "agent-1",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
		Status:        AgentStatusActive,
	})
	if err != nil {
		t.Fatalf("UpsertAgentControlAgent: %v", err)
	}
	if agentRecord.Seq <= 0 {
		t.Fatalf("expected agent registry sequence, got %#v", agentRecord)
	}

	reopened, err := NewRegistryService(ctx, RegistryServiceConfig{StorePath: storePath})
	if err != nil {
		t.Fatalf("reopen NewRegistryService: %v", err)
	}
	defer reopened.Close()
	mailboxRows, err := reopened.MailboxStore.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{Scope: MailboxScopeSession})
	if err != nil {
		t.Fatalf("ListAgentControlMailboxRecords: %v", err)
	}
	if len(mailboxRows) != 1 || mailboxRows[0].MessageID != "message-1" {
		t.Fatalf("unexpected mailbox rows: %#v", mailboxRows)
	}
	agentRows, err := reopened.AgentStore.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: "root-session", IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListAgentControlAgents: %v", err)
	}
	if len(agentRows) != 1 || agentRows[0].AgentID != "agent-1" {
		t.Fatalf("unexpected agent rows: %#v", agentRows)
	}
}

func TestRegistryServiceAllowsMailboxAndAgentOverrides(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	service, err := NewRegistryService(ctx, RegistryServiceConfig{
		StorePath:        filepath.Join(dir, "agent-control.sqlite"),
		MailboxStorePath: filepath.Join(dir, "mailbox.sqlite"),
		AgentStorePath:   filepath.Join(dir, "agents.sqlite"),
	})
	if err != nil {
		t.Fatalf("NewRegistryService: %v", err)
	}
	defer service.Close()
	if service.MailboxStore == nil || service.AgentStore == nil {
		t.Fatalf("expected both override stores, got mailbox=%T agent=%T", service.MailboxStore, service.AgentStore)
	}
}
