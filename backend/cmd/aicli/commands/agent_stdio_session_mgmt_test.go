package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// newSessionMgmtTestHost builds a host whose durable store lives in a temp dir,
// so session/list and session/delete never touch the user's real sessions.
func newSessionMgmtTestHost(t *testing.T) *acpSessionHost {
	t.Helper()
	host := newACPSessionHost(&config.Config{}, &agentStdioOptions{
		ExecOptions: &ExecOptions{SessionDir: t.TempDir()},
	})
	t.Cleanup(host.Close)
	return host
}

func TestACPSessionHostListDeleteClose(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	host := newSessionMgmtTestHost(t)

	host.mu.Lock()
	manager, userID, err := host.sessionStoreLocked()
	host.mu.Unlock()
	if err != nil {
		t.Fatalf("sessionStoreLocked: %v", err)
	}
	created, err := manager.Create(ctx, userID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := manager.SetTitle(ctx, created.ID, "hello-title"); err != nil {
		t.Fatalf("set title: %v", err)
	}

	resp, err := host.ListSessions(ctx, acp.SessionListRequest{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(resp.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d: %+v", len(resp.Sessions), resp.Sessions)
	}
	if resp.Sessions[0].SessionID != created.ID || resp.Sessions[0].Title != "hello-title" {
		t.Fatalf("summary = %+v, want id=%s title=hello-title", resp.Sessions[0], created.ID)
	}

	// A cwd filter must not hide sessions whose workspace predates cwd recording.
	resp, err = host.ListSessions(ctx, acp.SessionListRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("ListSessions with cwd: %v", err)
	}
	if len(resp.Sessions) != 1 {
		t.Fatalf("cwd filter hid an unknown-workspace session: %+v", resp.Sessions)
	}

	// Invalid cursors are client errors, not internal errors.
	if _, err := host.ListSessions(ctx, acp.SessionListRequest{Cursor: "abc"}); err == nil {
		t.Fatal("expected invalid cursor error")
	}

	// Closing an unattached session is idempotent.
	if err := host.CloseSession(ctx, acp.SessionCloseRequest{SessionID: "not_attached"}); err != nil {
		t.Fatalf("CloseSession(unknown) = %v, want nil", err)
	}

	// Deleting a session that exists neither live nor on disk is a silent
	// success: ACP v1 defines delete as idempotent, so a client retrying after
	// a reconnect must not see a spurious not-found failure.
	if err := host.DeleteSession(ctx, acp.SessionDeleteRequest{SessionID: "missing"}); err != nil {
		t.Fatalf("DeleteSession(unknown) = %v, want nil", err)
	}
	// An empty sessionId is still a client error: it can never name a session.
	if err := host.DeleteSession(ctx, acp.SessionDeleteRequest{SessionID: "  "}); err == nil {
		t.Fatal("expected invalid params for empty sessionId")
	}

	if err := host.DeleteSession(ctx, acp.SessionDeleteRequest{SessionID: created.ID}); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	resp, err = host.ListSessions(ctx, acp.SessionListRequest{})
	if err != nil {
		t.Fatalf("ListSessions after delete: %v", err)
	}
	if len(resp.Sessions) != 0 {
		t.Fatalf("session survived delete: %+v", resp.Sessions)
	}
}

func TestACPSessionHostListMergesLiveSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	host := newSessionMgmtTestHost(t)

	// A live session without a runtime session row must still be listed so a
	// client sees a session it just created before the first prompt.
	host.mu.Lock()
	host.sess["acp_live_only"] = &acpHostSession{id: "acp_live_only", chat: &ChatSession{}}
	host.mu.Unlock()

	resp, err := host.ListSessions(ctx, acp.SessionListRequest{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(resp.Sessions) != 1 || resp.Sessions[0].SessionID != "acp_live_only" {
		t.Fatalf("live session missing from list: %+v", resp.Sessions)
	}

	// Deleting a live-only session detaches it and succeeds even though the
	// durable store never saw it.
	if err := host.DeleteSession(ctx, acp.SessionDeleteRequest{SessionID: "acp_live_only"}); err != nil {
		t.Fatalf("DeleteSession(live-only): %v", err)
	}
	host.mu.Lock()
	_, stillThere := host.sess["acp_live_only"]
	host.mu.Unlock()
	if stillThere {
		t.Fatal("live session was not detached by DeleteSession")
	}
}

func TestACPSessionHostDeleteRequiresSessionID(t *testing.T) {
	t.Parallel()

	host := newSessionMgmtTestHost(t)
	err := host.DeleteSession(context.Background(), acp.SessionDeleteRequest{SessionID: "   "})
	if err == nil || !strings.Contains(err.Error(), "sessionId") {
		t.Fatalf("error = %v, want sessionId validation", err)
	}
}

// TestACPAvailableCommandsCatalog pins that the advertised catalog only names
// commands the headless host can actually serve.
func TestACPAvailableCommandsCatalog(t *testing.T) {
	t.Parallel()

	commands := acpAvailableCommands()
	if len(commands) == 0 {
		t.Fatal("catalog must not be empty")
	}
	seen := make(map[string]bool, len(commands))
	for _, command := range commands {
		if strings.TrimSpace(command.Name) == "" {
			t.Fatalf("command without name: %+v", command)
		}
		if seen[command.Name] {
			t.Fatalf("duplicate command %q", command.Name)
		}
		seen[command.Name] = true
	}
	for _, required := range []string{"help", "status", "model", "mode"} {
		if !seen[required] {
			t.Fatalf("catalog missing %q: %+v", required, commands)
		}
	}
}

// TestEmitACPSessionUsageSkipsUnknownWindow pins the "do not send bogus usage"
// contract: without a known context window the update is omitted entirely.
func TestEmitACPSessionUsageSkipsUnknownWindow(t *testing.T) {
	t.Parallel()

	emit := &recordingACPEmitter{}
	emitACPSessionUsage(emit, "sess_1", &ChatSession{ContextTokenCount: 100, ContextWindowTokenCount: 0})
	if updates := emit.snapshot(); len(updates) != 0 {
		t.Fatalf("usage_update emitted without a window size: %+v", updates)
	}

	emit = &recordingACPEmitter{}
	emitACPSessionUsage(emit, "sess_1", &ChatSession{ContextTokenCount: 100, ContextWindowTokenCount: 1000})
	updates := emit.snapshot()
	if len(updates) != 1 || updates[0].SessionUpdate != acp.SessionUpdateUsage {
		t.Fatalf("usage_update = %+v", updates)
	}
	if updates[0].Used != 100 || updates[0].Size != 1000 {
		t.Fatalf("usage payload = used:%d size:%d, want 100/1000", updates[0].Used, updates[0].Size)
	}
}

// TestACPSessionHostListFiltersByWorkspace pins that cwd narrowing works for
// sessions that recorded a workspace, while unknown-workspace sessions stay
// visible (see TestACPSessionHostListDeleteClose).
func TestACPSessionHostListFiltersByWorkspace(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	host := newSessionMgmtTestHost(t)

	host.mu.Lock()
	host.sess["acp_ws"] = &acpHostSession{
		id: "acp_ws",
		chat: &ChatSession{RuntimeSession: &runtimechat.Session{
			ID:       "acp_ws",
			Metadata: runtimechat.SessionMetadata{Context: map[string]interface{}{"cwd": `C:\repo\a`}},
		}},
	}
	host.mu.Unlock()

	resp, err := host.ListSessions(ctx, acp.SessionListRequest{Cwd: `c:\repo\a`})
	if err != nil {
		t.Fatalf("ListSessions(same cwd): %v", err)
	}
	if len(resp.Sessions) != 1 || resp.Sessions[0].SessionID != "acp_ws" {
		t.Fatalf("same-workspace list = %+v, want acp_ws", resp.Sessions)
	}

	resp, err = host.ListSessions(ctx, acp.SessionListRequest{Cwd: `C:\repo\b`})
	if err != nil {
		t.Fatalf("ListSessions(other cwd): %v", err)
	}
	if len(resp.Sessions) != 0 {
		t.Fatalf("different-workspace session leaked into list: %+v", resp.Sessions)
	}
}

// TestACPSessionHostSessionInfoDedupe pins §4.3/§11-B4: an unchanged title is
// pushed once, a changed title is pushed again, and session/load (which calls
// resetSessionInfoDedupe) lets the same title reach a freshly attached client.
func TestACPSessionHostSessionInfoDedupe(t *testing.T) {
	t.Parallel()

	host := newSessionMgmtTestHost(t)
	chat := &ChatSession{RuntimeSession: &runtimechat.Session{
		ID:       "sess-info",
		Metadata: runtimechat.SessionMetadata{Title: "hello title"},
	}}
	emit := &recordingACPEmitter{}

	host.emitSessionInfo(emit, "sess-info", chat)
	host.emitSessionInfo(emit, "sess-info", chat)
	if got := len(emit.snapshot()); got != 1 {
		t.Fatalf("unchanged title pushed %d times, want 1", got)
	}

	// An auto-generated title change must reach the client.
	chat.RuntimeSession.Metadata.Title = "generated title"
	host.emitSessionInfo(emit, "sess-info", chat)
	if got := len(emit.snapshot()); got != 2 {
		t.Fatalf("changed title pushed %d times total, want 2", got)
	}

	// A (re)attaching client needs the current title even when unchanged.
	host.resetSessionInfoDedupe("sess-info")
	host.emitSessionInfo(emit, "sess-info", chat)
	updates := emit.snapshot()
	if len(updates) != 3 {
		t.Fatalf("after reset the title must be re-emitted: total %d, want 3", len(updates))
	}
	if last := updates[len(updates)-1]; last.SessionUpdate != acp.SessionUpdateSessionInfo || last.Title != "generated title" {
		t.Fatalf("last update = %+v, want session_info_update with the current title", last)
	}

	// An empty title must never hit the wire: omitempty would leave a noise
	// frame that strict clients render as a blank header.
	empty := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "sess-empty"}}
	host.emitSessionInfo(emit, "sess-empty", empty)
	if got := len(emit.snapshot()); got != 3 {
		t.Fatalf("empty title must not emit: total %d, want 3", got)
	}
}
