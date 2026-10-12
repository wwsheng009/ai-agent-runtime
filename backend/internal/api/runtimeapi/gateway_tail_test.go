package runtimeapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// gatedGatewayController extends the shared fake with the optional P1-b gate
// surface (TailWanted) exactly like *gatewayagent.Controller exposes it, so the
// runtime-server host mapping can be tested with the watch gate on and off.
type gatedGatewayController struct {
	fakeGatewayController
	wanted bool
}

func (g *gatedGatewayController) TailWanted(string) bool { return g.wanted }

// countingGatewayController records how many SESSION_UPDATE pushes the host
// made (fakeGatewayController replaces its slice, so it cannot count pushes).
type countingGatewayController struct {
	fakeGatewayController
	pushes int
}

func (c *countingGatewayController) UpdateSessions(sessions []wire.SessionSummary) {
	c.pushes++
	c.fakeGatewayController.UpdateSessions(sessions)
}

// newTailTestHandler builds a handler with a real in-memory session manager and
// a fake control plane, so the runtime-server mapping can be exercised end to
// end: bus event → appendGatewayTail → shared store → GatewaySessions →
// UpdateSessions.
func newTailTestHandler(t *testing.T, controller GatewayController) (*Handler, *chat.Session) {
	t.Helper()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetGatewayController(controller)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	t.Cleanup(handler.getSessionHub().StopAll)
	handler.SetSessionManager(sessionManager)
	session, err := sessionManager.Create(context.Background(), "user-gateway-tail")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	handler.EnableGatewayAttention()
	return handler, session
}

// TestGatewayTailFromDeltaEventsAppliesSharedCore verifies the runtime-server
// host mapping over the shared core (internal/gateway/agent, the aicli twin):
// assistant deltas accumulate, the display tail is bounded to the newest 512
// runes, the 1s throttle keeps the cadence, and every push carries StatusTail
// to the gateway through the same SessionState.Apply projection as aicli.
func TestGatewayTailFromDeltaEventsAppliesSharedCore(t *testing.T) {
	controller := &countingGatewayController{}
	handler, session := newTailTestHandler(t, controller)
	bus := handler.getRuntimeEventBus()

	big := strings.Repeat("0123456789", 80) // 800 runes
	bus.Publish(runtimeevents.Event{Type: "assistant_delta", SessionID: session.ID,
		Payload: map[string]interface{}{"delta": big}})

	st := handler.testGatewayState(session.ID)
	if n := len([]rune(st.Tail)); n != 512 {
		t.Fatalf("tail runes = %d, want shared bound 512", n)
	}
	if !strings.HasSuffix(st.Tail, "0123456789") {
		t.Fatalf("tail must keep the newest runes, got %q", st.Tail)
	}
	if st.TailAt.IsZero() {
		t.Fatalf("TailAt not stamped on push")
	}
	if controller.pushes != 1 || len(controller.sessions) != 1 || controller.sessions[0].StatusTail != st.Tail {
		t.Fatalf("pushed summary must carry StatusTail, got %+v", controller.sessions)
	}

	// A delta inside the 1s cadence accumulates but must not push: the stored
	// tail and the last pushed summary stay untouched.
	bus.Publish(runtimeevents.Event{Type: "assistant.delta", SessionID: session.ID,
		Payload: map[string]interface{}{"content": "LATE"}})
	if got := handler.testGatewayState(session.ID).Tail; got != st.Tail {
		t.Fatalf("throttled delta changed the stored tail: %q", got)
	}
	if controller.pushes != 1 {
		t.Fatalf("throttled delta must not push, pushes = %d", controller.pushes)
	}

	// Past the cadence the accumulated window (including the delta swallowed by
	// the throttle) is pushed through the shared store.
	time.Sleep(1100 * time.Millisecond)
	bus.Publish(runtimeevents.Event{Type: "assistant_delta", SessionID: session.ID,
		Payload: map[string]interface{}{"text": "END"}})
	got := handler.testGatewayState(session.ID)
	if !strings.HasSuffix(got.Tail, "LATEEND") {
		t.Fatalf("tail after cadence = %q, want suffix LATEEND", got.Tail)
	}
	if controller.pushes != 2 || len(controller.sessions) != 1 || controller.sessions[0].StatusTail != got.Tail {
		t.Fatalf("second push must carry StatusTail, got %+v", controller.sessions)
	}
}

// TestGatewayTailHonorsTailWantedGate pins the same P1-b gate contract aicli
// enforces through chatGatewayTailWanted: a controller advertising the watch
// gate suppresses tail pushes while the session is not watched (and the gate is
// evaluated before the throttle, so a closed gate consumes no push budget).
func TestGatewayTailHonorsTailWantedGate(t *testing.T) {
	closed := &gatedGatewayController{wanted: false}
	handlerClosed, sessionClosed := newTailTestHandler(t, closed)
	handlerClosed.getRuntimeEventBus().Publish(runtimeevents.Event{Type: "assistant_delta",
		SessionID: sessionClosed.ID, Payload: map[string]interface{}{"delta": "hidden"}})
	if st := handlerClosed.testGatewayState(sessionClosed.ID); st.Tail != "" {
		t.Fatalf("closed gate must not push, tail = %q", st.Tail)
	}

	open := &gatedGatewayController{wanted: true}
	handlerOpen, sessionOpen := newTailTestHandler(t, open)
	handlerOpen.getRuntimeEventBus().Publish(runtimeevents.Event{Type: "assistant_delta",
		SessionID: sessionOpen.ID, Payload: map[string]interface{}{"delta": "visible"}})
	if st := handlerOpen.testGatewayState(sessionOpen.ID); st.Tail != "visible" {
		t.Fatalf("open gate must push, tail = %q", st.Tail)
	}
}

// TestGatewaySessionsCarryStatusTail pins the summary projection: the session
// list sent to the gateway carries the live tail through SessionState.Apply,
// the same call the aicli host uses in chatGatewaySessions.
func TestGatewaySessionsCarryStatusTail(t *testing.T) {
	handler, session := newTailTestHandler(t, &fakeGatewayController{})
	handler.gatewayStateStore().SetTail(session.ID, "console live tail", time.Now())

	summaries := handler.GatewaySessions()
	if len(summaries) != 1 {
		t.Fatalf("summaries = %+v", summaries)
	}
	if summaries[0].SessionID != session.ID || summaries[0].StatusTail != "console live tail" {
		t.Fatalf("summary must carry the shared tail, got %+v", summaries[0])
	}
}
