package skills

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
)

// StreamSessionRuntimeEvents streams runtime events for a session via SSE.
//
// Durable path (default): polls / watches the session event store.
// Optional live path: query live=1|true|yes (or include_live_progress=1)
// also subscribes to the in-process runtime event bus for high-frequency
// live-only events such as tool.progress. Live events are marked with
// payload["live"]=true and are never written to the durable store by this path.
func (h *Handler) StreamSessionRuntimeEvents(w http.ResponseWriter, r *http.Request) {
	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	store := h.getSessionEventStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session event store not configured"))
		return
	}

	afterSeq := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("after")); raw != "" {
		if parsed, err := parseInt64(raw); err == nil && parsed >= 0 {
			afterSeq = parsed
		} else {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid after value"))
			return
		}
	}

	pollInterval := 500 * time.Millisecond
	if raw := strings.TrimSpace(r.URL.Query().Get("poll_ms")); raw != "" {
		if parsed, err := time.ParseDuration(raw + "ms"); err == nil && parsed > 0 {
			pollInterval = parsed
		}
	}

	includeLive := parseTruthyQueryFlag(r.URL.Query().Get("live")) ||
		parseTruthyQueryFlag(r.URL.Query().Get("include_live_progress"))

	h.prepareSSEHeaders(w)
	emitter := newSSEEmitter(w)

	ctx := r.Context()
	var eventWake <-chan runtimeevents.Event
	unwatch := func() {}
	if watcher, ok := store.(chat.EventWatcherStore); ok && watcher != nil {
		eventWake, unwatch = watcher.WatchEvents(ctx, sessionID)
	}
	defer unwatch()

	fallbackInterval := pollInterval
	if eventWake != nil {
		fallbackInterval = 5 * time.Second
	}
	ticker := time.NewTicker(fallbackInterval)
	defer ticker.Stop()

	var liveCh <-chan runtimeevents.Event
	var unsubLive func()
	if includeLive {
		liveCh, unsubLive = h.subscribeSessionLiveRuntimeEvents(sessionID)
		if unsubLive != nil {
			defer unsubLive()
		}
	}

	sendEvents := func(events []runtimeevents.Event) error {
		for _, event := range events {
			emitter.Emit("runtime_event", buildSessionRuntimeEventView(event))
			if event.Payload != nil {
				if seqRaw, ok := event.Payload["seq"]; ok {
					if seq, ok := asInt64(seqRaw); ok && seq > afterSeq {
						afterSeq = seq
					}
				}
			}
		}
		return nil
	}

	// Initial dump.
	if events, err := store.ListEvents(ctx, sessionID, afterSeq, 0); err == nil && len(events) > 0 {
		_ = sendEvents(events)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case liveEvent, ok := <-liveCh:
			if !ok {
				liveCh = nil
				continue
			}
			emitter.Emit("runtime_event", buildSessionRuntimeLiveEventView(liveEvent))
		case <-eventWake:
			events, err := store.ListEvents(ctx, sessionID, afterSeq, 0)
			if err != nil {
				emitter.Emit("error", map[string]interface{}{"error": err.Error()})
				return
			}
			if len(events) > 0 {
				_ = sendEvents(events)
			}
		case <-ticker.C:
			events, err := store.ListEvents(ctx, sessionID, afterSeq, 0)
			if err != nil {
				emitter.Emit("error", map[string]interface{}{"error": err.Error()})
				return
			}
			if len(events) > 0 {
				_ = sendEvents(events)
			}
		}
	}
}

// subscribeSessionLiveRuntimeEvents fans out bus events for one session onto a
// buffered channel. Non-blocking send drops when the consumer is slow so Publish
// never stalls tool execution. Only live-only types are forwarded (currently
// tool.progress); durable types continue via the store path.
func (h *Handler) subscribeSessionLiveRuntimeEvents(sessionID string) (<-chan runtimeevents.Event, func()) {
	sessionID = strings.TrimSpace(sessionID)
	if h == nil || sessionID == "" {
		return nil, func() {}
	}
	bus := h.getRuntimeEventBus()
	if bus == nil {
		return nil, func() {}
	}

	ch := make(chan runtimeevents.Event, 64)
	unsub := bus.SubscribeCancelable(toolprotocol.EventTypeProgress, func(event runtimeevents.Event) {
		if strings.TrimSpace(event.SessionID) != sessionID {
			return
		}
		if !isSessionLiveOnlyRuntimeEvent(event) {
			return
		}
		select {
		case ch <- event:
		default:
			// Drop when the SSE consumer lags; progress is best-effort.
		}
	})

	return ch, func() {
		if unsub != nil {
			unsub()
		}
		// Do not close ch: handlers may still race after unsubscribe until Publish
		// returns; GC reclaims the channel when the handler exits.
	}
}

func isSessionLiveOnlyRuntimeEvent(event runtimeevents.Event) bool {
	switch strings.TrimSpace(event.Type) {
	case toolprotocol.EventTypeProgress:
		return true
	default:
		return false
	}
}

func buildSessionRuntimeLiveEventView(event runtimeevents.Event) map[string]interface{} {
	view := buildSessionRuntimeEventView(event)
	// Mark live so clients can distinguish bus-forwarded progress from durable rows.
	view["live"] = true
	if payload, ok := view["payload"].(map[string]interface{}); ok && payload != nil {
		// Shallow clone so we do not mutate the bus-retained event payload.
		cloned := make(map[string]interface{}, len(payload)+1)
		for key, value := range payload {
			cloned[key] = value
		}
		cloned["live"] = true
		view["payload"] = cloned
	} else {
		view["payload"] = map[string]interface{}{"live": true}
	}
	return view
}

func parseTruthyQueryFlag(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseInt64(raw string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
}

func asInt64(raw interface{}) (int64, bool) {
	switch v := raw.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	case json.Number:
		if parsed, err := v.Int64(); err == nil {
			return parsed, true
		}
	case string:
		parsed, err := parseInt64(v)
		return parsed, err == nil
	}
	return 0, false
}
