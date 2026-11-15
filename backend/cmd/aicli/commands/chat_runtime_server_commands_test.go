package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

type runtimeServerCommandTestExecutor struct {
	serverURL   string
	schedule    []schedule.CatalogEntry
	scheduleErr error
}

func (e *runtimeServerCommandTestExecutor) Execute(context.Context, *ChatSession, string) (string, error) {
	return "", nil
}

func (e *runtimeServerCommandTestExecutor) RuntimeDescriptor() aicliRuntimeExecutorDescriptor {
	return newAICLIActorRuntimeDescriptor(aicliRuntimeTransportHTTP)
}

func (e *runtimeServerCommandTestExecutor) ToolAvailable(*ChatSession, string) bool {
	return false
}

func (e *runtimeServerCommandTestExecutor) RuntimeServerURL() string {
	return e.serverURL
}

func (e *runtimeServerCommandTestExecutor) RuntimeServerScheduleCatalog(context.Context, *ChatSession) ([]schedule.CatalogEntry, error) {
	return e.schedule, e.scheduleErr
}

func TestRemoteSupervisionRequiresControlCaller(t *testing.T) {
	for _, command := range []string{"/supervision"} {
		session := &ChatSession{ChatExecutor: &runtimeServerCommandTestExecutor{serverURL: "http://127.0.0.1:8101"}}
		result, handled, err := tryExecuteStructuredChatCommand(session, command)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", command, err)
		}
		if !handled {
			t.Fatalf("%s: command was not claimed by the structured dispatcher", command)
		}
		if len(result.Blocks) == 0 {
			t.Fatalf("%s: missing command result", command)
		}
		text := ui.RenderDocumentPlain(result.Blocks[0].Document)
		if !strings.Contains(text, "当前执行器不支持 runtime-server 控制命令") {
			t.Fatalf("%s: expected explicit control-caller refusal, got %q", command, text)
		}
	}
}

func TestRemoteControlCommandsReadServerAPIs(t *testing.T) {
	requests := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.Path]++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/runtime/sessions/session-1/plan":
			_, _ = w.Write([]byte(`{"session_id":"session-1","status":"active","active":true}`))
		case "/api/runtime/plans":
			_, _ = w.Write([]byte(`{"plans":[],"count":0}`))
		case "/api/runtime/agent-control/agents":
			_, _ = w.Write([]byte(`{"agents":[],"count":0,"source":"test"}`))
		case "/api/runtime/agent-control/cleanup":
			_, _ = w.Write([]byte(`{"reclaim":"ok","root_session_id":"session-1","dry_run":true,"text":"Agent Registry Cleanup:\n  root_session=session-1 quota_children=0 reclaimable=0 idle_policy=disabled\n  dry_run=true（未执行任何回收）"}`))
		case "/api/runtime/agent-definitions":
			_, _ = w.Write([]byte(`{"action":"list","source":"server_agentdef_registry","count":1,"text":"Agent definitions (1):\nBUILTIN (1)\n  explore              test role"}`))
		case "/api/runtime/sessions/session-1/routing":
			_, _ = w.Write([]byte(`{"session_id":"session-1","routing":{"enabled":true}}`))
		case "/api/runtime/memory/status":
			_, _ = w.Write([]byte(`{"mode":"on","records":0}`))
		case "/api/runtime/memory/records":
			_, _ = w.Write([]byte(`{"id":"mem-1","items":[],"count":0}`))
		case "/api/runtime/supervision/audit":
			_, _ = w.Write([]byte(`{"auto_audit_enabled":false,"pending_wake_count":0}`))
		case "/api/runtime/supervision/watchdog":
			_, _ = w.Write([]byte(`{"available":true,"built":false,"text":"执行看门狗（P0-4）：已就绪（尚未启动）"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "session-1"},
		ChatExecutor:   &aicliRuntimeServerChatExecutor{serverURL: server.URL},
	}
	cases := []struct {
		command string
		want    string
	}{
		{command: "/plan status", want: `"status": "active"`},
		{command: "/plans", want: `"count": 0`},
		{command: "/agents", want: `"source": "test"`},
		{command: "/agents routing", want: `"enabled": true`},
		{command: "/agents defs", want: "Agent definitions (1):"},
		{command: "/agents cleanup --dry-run", want: "dry_run=true"},
		{command: "/memory status", want: `"mode": "on"`},
		{command: "/memory add remembered fact", want: `"id": "mem-1"`},
		{command: "/memory search remembered", want: `"count": 0`},
		{command: "/supervision status", want: `"auto_audit_enabled": false`},
		{command: "/supervision watchdog", want: "执行看门狗"},
		{command: "/debug supervision watchdog", want: "执行看门狗"},
	}
	for _, tc := range cases {
		result, handled, err := tryExecuteStructuredChatCommand(session, tc.command)
		if err != nil {
			t.Fatalf("%s: %v", tc.command, err)
		}
		if !handled || len(result.Blocks) == 0 {
			t.Fatalf("%s: command was not handled", tc.command)
		}
		text := ui.RenderDocumentPlain(result.Blocks[0].Document)
		if !strings.Contains(text, tc.want) {
			t.Fatalf("%s: expected %q in output:\n%s", tc.command, tc.want, text)
		}
	}
	if requests["/api/runtime/sessions/session-1/plan"] != 1 ||
		requests["/api/runtime/plans"] != 1 ||
		requests["/api/runtime/agent-control/agents"] != 1 ||
		requests["/api/runtime/agent-control/cleanup"] != 1 ||
		requests["/api/runtime/agent-definitions"] != 1 ||
		requests["/api/runtime/sessions/session-1/routing"] != 1 ||
		requests["/api/runtime/memory/status"] != 1 ||
		requests["/api/runtime/memory/records"] != 2 ||
		requests["/api/runtime/supervision/audit"] != 1 ||
		requests["/api/runtime/supervision/watchdog"] != 2 {
		t.Fatalf("unexpected server requests: %#v", requests)
	}

	// Unimplemented subcommands still fail closed on the server boundary.
	result, handled, err := tryExecuteStructuredChatCommand(session, "/memory vacuum")
	if err != nil || !handled || len(result.Blocks) == 0 {
		t.Fatalf("unsupported memory subcommand must be handled with an explicit error: handled=%v err=%v", handled, err)
	}
	if text := ui.RenderDocumentPlain(result.Blocks[0].Document); !strings.Contains(text, "未接入 /memory vacuum") {
		t.Fatalf("expected explicit unsupported-subcommand error, got %q", text)
	}
}

func TestRemoteScheduleReadsServerCatalog(t *testing.T) {
	executor := &runtimeServerCommandTestExecutor{
		serverURL: "http://127.0.0.1:8101",
		schedule: []schedule.CatalogEntry{
			{
				Record: schedule.Record{
					ID:           "task-1",
					Title:        "runtime-server task",
					Kind:         schedule.KindAfter,
					ScheduledAt:  time.Now().UTC().Add(time.Minute),
					AfterSeconds: 60,
				},
				SessionID: "session-1",
				Status:    schedule.StatusActive,
			},
		},
	}
	session := &ChatSession{ChatExecutor: executor}
	result := executeStructuredScheduleCommand(session, "/schedule")
	if len(result.Blocks) == 0 {
		t.Fatal("schedule command returned no document")
	}
	text := ui.RenderDocumentPlain(result.Blocks[0].Document)
	if !strings.Contains(text, "runtime-server task") || !strings.Contains(text, "数据源: runtime-server") {
		t.Fatalf("remote schedule document missing expected content:\n%s", text)
	}
}

func TestLocalSessionWithHTTPExecutorIsNotClassifiedAsRemote(t *testing.T) {
	session := &ChatSession{
		LocalRuntimeHost: &localChatRuntimeHost{},
		ChatExecutor:     &runtimeServerCommandTestExecutor{serverURL: "http://127.0.0.1:8101"},
	}
	if chatSessionUsesRuntimeServer(session) {
		t.Fatal("a session with a local runtime host must not be classified as remote")
	}
}
