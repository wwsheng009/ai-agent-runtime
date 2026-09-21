package usageledger

import (
	"fmt"
	"log"
	"strconv"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

// Service is the generic, EventBus-attached usage ledger recorder.
//
// It is the shared counterpart to runtime-server's skillsapi.Handler.appendUsageLedger:
// whereas the handler records skill-execution usage (subsystem=skill_runtime),
// this Service observes the runtime EventBus and records LLM-request usage
// emitted by the agent loop (subsystem=llm_runtime) from llm.request.finished
// events. Both write to the same token_usage_history table via SQLiteStore, so
// aicli and runtime-server populate the ledger uniformly from their respective
// execution paths (see docs §04:69 / §1064 / 06 §338 "use existing usageledger").
type Service struct {
	store  *SQLiteStore
	unsubs []func()
	now    func() time.Time
}

// NewService constructs a ledger Service bound to the given SQLiteStore.
// Returns nil if the store is nil.
func NewService(store *SQLiteStore) *Service {
	if store == nil {
		return nil
	}
	return &Service{store: store, now: time.Now}
}

// Attach subscribes the Service to the runtime EventBus so that
// llm.request.finished events are recorded into the ledger.
func (s *Service) Attach(bus *runtimeevents.Bus) {
	if s == nil || bus == nil {
		return
	}
	s.unsubs = append(s.unsubs,
		bus.SubscribeCancelable("llm.request.finished", s.onRequestFinished),
		bus.SubscribeCancelable("llm_request_finished", s.onRequestFinished),
	)
}

// Close unsubscribes from the EventBus and closes the underlying store.
func (s *Service) Close() {
	if s == nil {
		return
	}
	for _, unsub := range s.unsubs {
		if unsub != nil {
			unsub()
		}
	}
	s.unsubs = nil
	_ = s.store.Close()
}

// onRequestFinished records a token_usage_history row from an llm.request.finished
// event. The event is emitted on both the success and failure paths (loop.go),
// so success is read from the payload; usage tokens are only present on success.
func (s *Service) onRequestFinished(event runtimeevents.Event) {
	defer func() { _ = recover() }()

	payload := event.Payload
	llmRequestID := payloadString(payload, "llm_request_id")
	if llmRequestID == "" {
		return
	}
	sessionID := event.SessionID
	if sessionID == "" {
		sessionID = payloadString(payload, "session_id")
	}
	if sessionID == "" {
		// Requests without a session cannot be grouped; skip.
		return
	}

	success := payloadBool(payload, "success", true)
	record := &entity.TokenUsageHistory{
		RequestID:    llmRequestID,
		ModelID:      payloadString(payload, "model"),
		ProviderID:   payloadString(payload, "provider"),
		InputTokens:  int(payloadInt64OrZero(payload, "usage_prompt_tokens")),
		OutputTokens: int(payloadInt64OrZero(payload, "usage_completion_tokens")),
		TotalTokens:  int(payloadInt64OrZero(payload, "usage_total_tokens")),
		Success:      success,
		StatusCode:   statusCodeFor(success),
		Metadata: entity.JSONMap{
			"subsystem":  "llm_runtime",
			"session_id": sessionID,
			"trace_id":   event.TraceID,
			"turn_id":    payloadString(payload, "logical_turn_id", "turn_id"),
			"step":       int(payloadInt64OrZero(payload, "step")),
		},
		CreatedAt: entity.Time(s.now().UTC()),
	}
	if err := s.store.Create(record); err != nil {
		// Ledger writes are best-effort: never block the event bus.
		log.Printf("usageledger: failed to persist llm.request.finished record (request_id=%s): %v", llmRequestID, err)
	}
}

// statusCodeFor maps a success flag to an HTTP-style status code, mirroring
// runtime-server's appendUsageLedger (200 on success, 500 on failure).
func statusCodeFor(success bool) int {
	if success {
		return 200
	}
	return 500
}

// payloadString returns the first non-empty string value for any of the given
// keys. Mirrors the unexported helper in usageanalytics.
func payloadString(payload map[string]interface{}, keys ...string) string {
	if payload == nil {
		return ""
	}
	for _, key := range keys {
		value, ok := payload[key]
		if !ok || value == nil {
			continue
		}
		switch v := value.(type) {
		case string:
			return v
		case fmt.Stringer:
			return v.String()
		default:
			return fmt.Sprint(v)
		}
	}
	return ""
}

// payloadInt64OrZero returns the int64 value for key, or 0 if absent/invalid.
func payloadInt64OrZero(payload map[string]interface{}, key string) int64 {
	if payload == nil {
		return 0
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return 0
	}
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return int64(f)
		}
		return 0
	default:
		return 0
	}
}

// payloadBool returns the bool value for key, or def if absent/invalid.
func payloadBool(payload map[string]interface{}, key string, def bool) bool {
	if payload == nil {
		return def
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return def
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
		return def
	default:
		return def
	}
}
