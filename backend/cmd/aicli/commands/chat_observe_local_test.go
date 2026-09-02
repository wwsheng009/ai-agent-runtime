package commands

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	config "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimeobserve "github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// newLocalObserveTestHost 构造一个具备 EventBus/SessionHub 的本地 host，
// 模拟 aicli 本地 in-process 模式（initializeLocalChatRuntimeHost 产物）。
func newLocalObserveTestHost(t *testing.T, observeEnabled bool) *localChatRuntimeHost {
	t.Helper()
	cfg := config.DefaultRuntimeConfig()
	cfg.Observe.Enabled = observeEnabled
	bus := runtimeevents.NewBusWithRetention(2048)
	hub := runtimechat.NewBoundedSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		return nil, errors.New("no actor in test host")
	})
	host := &localChatRuntimeHost{
		RuntimeConfig: cfg,
		EventBus:      bus,
		SessionHub:    hub,
	}
	t.Cleanup(func() { host.Close() })
	return host
}

// registerObserveTestDisplayProvider 注册"当前活动会话"provider 并返回
// 还原函数，供 observe HTTP handler 与端点清单测试使用。
func registerObserveTestDisplayProvider(session *ChatSession) func() {
	prev := chatDebugDisplaySessionProvider
	RegisterChatDebugDisplayProvider(func() *ChatSession { return session })
	return func() { chatDebugDisplaySessionProvider = prev }
}

func TestEnsureLocalObserveService_EnabledWithPprofOn(t *testing.T) {
	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "http://127.0.0.1:43210/debug/pprof/" })

	// 默认配置 Observe.Enabled=false，但 --pprof on 时应默认开启本地 observe。
	host := newLocalObserveTestHost(t, false)
	svc := ensureLocalObserveService(host)
	if svc == nil {
		t.Fatal("expected local observe service with --pprof on, got nil")
	}
	if !svc.Enabled() {
		t.Fatalf("expected local observe service enabled, got %+v", svc.Capabilities())
	}
	caps := svc.Capabilities()
	if !caps.Enabled {
		t.Fatal("expected capabilities.Enabled=true for local observe service")
	}
	if caps.SchemaVersion != runtimeobserve.SchemaVersionResponse {
		t.Fatalf("unexpected schema version: %s", caps.SchemaVersion)
	}
	// 服务应已订阅 host.EventBus：发布白名单事件后 ring 应有序列推进。
	// collector 异步消费 bus 事件，因此轮询等待序列推进。
	bus := host.EventBus
	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventRuntimeStarted, SessionID: "session-1", Payload: map[string]interface{}{}})
	var seq int64
	for i := 0; i < 50; i++ {
		snapshot, err := svc.BuildSnapshot(t.Context(), true)
		if err != nil {
			t.Fatalf("build snapshot: %v", err)
		}
		seq = snapshot.Cursor.ObservationSeq
		if seq > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if seq <= 0 {
		t.Fatalf("expected cursor observation seq > 0 after publish, got %d", seq)
	}
}

func TestEnsureLocalObserveService_ExplicitConfigEnable(t *testing.T) {
	// 无 pprof（provider 返回空串）但 Observe.Enabled=true：也应构建。
	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "" })

	host := newLocalObserveTestHost(t, true)
	svc := ensureLocalObserveService(host)
	if svc == nil {
		t.Fatal("expected local observe service with Observe.Enabled=true, got nil")
	}
	if !svc.Enabled() {
		t.Fatal("expected service enabled")
	}
}

func TestEnsureLocalObserveService_DisabledWithoutPprof(t *testing.T) {
	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "" })

	host := newLocalObserveTestHost(t, false)
	svc := ensureLocalObserveService(host)
	if svc != nil {
		t.Fatalf("expected nil local observe service (observe off, no pprof), got %+v", svc)
	}
}

func TestEnsureLocalObserveService_NoBusOrHub(t *testing.T) {
	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "http://127.0.0.1:43210/debug/pprof/" })

	cfg := config.DefaultRuntimeConfig()
	cfg.Observe.Enabled = true
	host := &localChatRuntimeHost{RuntimeConfig: cfg} // 无 EventBus/SessionHub
	if svc := ensureLocalObserveService(host); svc != nil {
		t.Fatalf("expected nil service without bus/hub, got %+v", svc)
	}
}

// observeTestRequest 对本地 observe handler 发起 GET 请求并解码 envelope。
func observeTestRequest(t *testing.T, session *ChatSession, target string) (*httptest.ResponseRecorder, runtimeobserve.Envelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	HandleChatDebugObserveRequest(rec, req)
	var env runtimeobserve.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope for %s: %v (body=%s)", target, err, rec.Body.String())
	}
	return rec, env
}

func TestHandleChatDebugObserveRequest_Endpoints(t *testing.T) {
	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "http://127.0.0.1:43210/debug/pprof/" })

	host := newLocalObserveTestHost(t, false)
	session := &ChatSession{ProviderName: "test", Model: "test-model", LocalRuntimeHost: host}
	restore := registerObserveTestDisplayProvider(session)
	defer restore()

	prefix := ChatDebugObservePrefix()

	// capabilities
	rec, env := observeTestRequest(t, session, prefix+"/capabilities")
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !env.OK || env.Data == nil {
		t.Fatalf("capabilities envelope not ok: %+v", env)
	}

	// snapshot
	rec, env = observeTestRequest(t, session, prefix+"/snapshot?include_sessions=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !env.OK || env.Data == nil {
		t.Fatalf("snapshot envelope not ok: %+v", env)
	}

	// events
	rec, env = observeTestRequest(t, session, prefix+"/events?limit=5")
	if rec.Code != http.StatusOK {
		t.Fatalf("events status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !env.OK || env.Data == nil {
		t.Fatalf("events envelope not ok: %+v", env)
	}

	// sessions/{id}：未知 session → 404 + observe_session_not_found
	rec, env = observeTestRequest(t, session, prefix+"/sessions/does-not-exist")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("sessions status=%d body=%s", rec.Code, rec.Body.String())
	}
	if env.OK || env.Error == nil || env.Error.Code != runtimeobserve.ErrCodeSessionNotFound {
		t.Fatalf("sessions error envelope mismatch: %+v", env)
	}

	// 未知路径 → 404
	req := httptest.NewRequest(http.MethodGet, prefix+"/nope", nil)
	rec = httptest.NewRecorder()
	HandleChatDebugObserveRequest(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path status=%d", rec.Code)
	}
}

func TestHandleChatDebugObserveRequest_Disabled(t *testing.T) {
	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "" })

	host := newLocalObserveTestHost(t, false)
	session := &ChatSession{ProviderName: "test", Model: "test-model", LocalRuntimeHost: host}
	restore := registerObserveTestDisplayProvider(session)
	defer restore()

	rec, env := observeTestRequest(t, session, ChatDebugObservePrefix()+"/capabilities")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("disabled capabilities status=%d body=%s", rec.Code, rec.Body.String())
	}
	if env.OK || env.Error == nil || env.Error.Code != runtimeobserve.ErrCodeDisabled {
		t.Fatalf("disabled error envelope mismatch: %+v", env)
	}
}

func TestHandleChatDebugObserveRequest_NoSession(t *testing.T) {
	restore := registerObserveTestDisplayProvider(nil)
	defer restore()

	req := httptest.NewRequest(http.MethodGet, ChatDebugObservePrefix()+"/capabilities", nil)
	rec := httptest.NewRecorder()
	HandleChatDebugObserveRequest(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no-session status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), runtimeobserve.ErrCodeDisabled) {
		t.Fatalf("expected disabled error code, body=%s", rec.Body.String())
	}
}

func TestChatDebugEndpointList_LocalObserveUsesLoopbackBase(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "http://127.0.0.1:43210/debug/pprof/" })

	// 默认配置 observe off + --pprof on：本地 observe 默认开启，
	// observe base 应指向本地 loopback 地址，而不是 fallback 8101。
	host := newLocalObserveTestHost(t, false)
	session := &ChatSession{ProviderName: "test", Model: "test-model", LocalRuntimeHost: host}
	restore := registerObserveTestDisplayProvider(session)
	defer restore()

	text := BuildChatDebugEndpointsText()
	for _, expected := range []string{
		"  Base: http://127.0.0.1:43210/api/runtime/observe/v1\n",
		"GET http://127.0.0.1:43210/api/runtime/observe/v1/capabilities  [enabled]",
		"GET http://127.0.0.1:43210/api/runtime/observe/v1/snapshot  [enabled]",
		"GET http://127.0.0.1:43210/api/runtime/observe/v1/sessions/{session_id}  [enabled]",
		"GET http://127.0.0.1:43210/api/runtime/observe/v1/events  [enabled]",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected endpoints text to contain %q, got:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "http://127.0.0.1:8101") {
		t.Fatalf("local observe must not fall back to runtime-server base, got:\n%s", text)
	}
}
