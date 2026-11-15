package runtimehost

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// ResolveEventPersistSettings is the single mapping from runtime config to the
// session event persistence buffer settings used by every host.
//
// P0-1 semantics: batching and async dispatch are enabled by default for both
// aicli and runtime-server. `disable: true` is the authoritative rollback to
// the synchronous per-event path. The legacy batchingEnabled/asyncDispatch
// fields are kept for config round-trip compatibility but are no longer the
// source of truth for the default state.
func ResolveEventPersistSettings(cfg runtimecfg.EventPersistConfig) chat.EventPersistSettings {
	enabled := !cfg.Disable
	return chat.EventPersistSettings{
		Enabled:         enabled,
		BatchSize:       cfg.BatchSize,
		FlushInterval:   cfg.FlushInterval,
		QueueLimit:      cfg.QueueLimit,
		QueueBytesLimit: cfg.QueueBytesLimit,
		ShutdownTimeout: cfg.ShutdownTimeout,
		FailMode:        cfg.FailMode,
		AsyncDispatch:   enabled,
		// Critical events (approval / session terminal / tool completion /
		// checkpoint) must flush immediately. Rebuildable stream deltas are the
		// only events allowed to be dropped when the bounded queue overflows.
		CriticalTypes:     runtimeevents.IsPersistCriticalEventType,
		OverflowDropTypes: runtimeevents.IsPersistStreamDeltaEventType,
	}
}

// PrepareSessionStoreEvent is the single A-channel (event bus -> session event
// store) admission and type-mapping rule used by runtime-server and aicli.
//
// It deliberately returns the alias-mapped event even when persist is false so
// callers that only need the store type can share the same mapping without
// re-implementing SessionStoreTypeAlias. The persist decision covers:
//   - session-scoped events only (empty SessionID is dropped);
//   - producer-persisted events (payload["seq"] already written) are skipped;
//   - only event types whose declarative contract includes ChannelSessionStore.
func PrepareSessionStoreEvent(event runtimeevents.Event) (runtimeevents.Event, bool) {
	mapped := event
	mapped.Type = runtimeevents.SessionStoreTypeAlias(event.Type)

	if strings.TrimSpace(event.SessionID) == "" {
		return mapped, false
	}
	if runtimeevents.ProducerPersistedEvent(event) {
		return mapped, false
	}
	if !runtimeevents.IsPersistedEventType(event.Type) {
		return mapped, false
	}
	return mapped, true
}
