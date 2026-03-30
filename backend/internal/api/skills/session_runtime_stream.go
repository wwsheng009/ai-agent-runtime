package skills

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/gorilla/mux"
)

// StreamSessionRuntimeEvents streams runtime events for a session via SSE.
func (h *Handler) StreamSessionRuntimeEvents(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(mux.Vars(r)["id"])
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

	h.prepareSSEHeaders(w)
	emitter := newSSEEmitter(w)

	ctx := r.Context()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

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

