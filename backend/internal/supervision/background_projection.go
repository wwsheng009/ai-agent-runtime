package supervision

import (
	"fmt"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
)

// BackgroundJobTerminalInputFromEvent projects a terminal job event into the
// shared supervision input. Hosts only resolve the root scope, command and
// observed flag.
func BackgroundJobTerminalInputFromEvent(
	event background.JobEvent,
	sessionID, rootScopeID, command string,
	observed bool,
	origin background.JobOrigin,
) BackgroundJobTerminalInput {
	return BackgroundJobTerminalInput{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: sessionID,
		JobID:                 event.JobID,
		ParentTurnID:          origin.ParentTurnID,
		ParentEpisodeID:       origin.ParentEpisodeID,
		SettlementID:          origin.SettlementID,
		SettlementCohortID:    origin.SettlementCohortID,
		FinalizationPolicy:    origin.FinalizationPolicy,
		Status:                event.Type,
		Command:               command,
		ExitCode:              BackgroundJobPayloadString(event.Payload["exit_code"]),
		ErrorCode:             BackgroundJobPayloadString(event.Payload["error_code"]),
		Message:               BackgroundJobPayloadString(event.Payload["message"]),
		CancelSource:          BackgroundJobPayloadString(event.Payload["cancel_source"]),
		Observed:              observed,
		Epoch:                 BackgroundJobTerminalEpoch(event),
	}
}

// BackgroundJobMonitorInputFromEvent projects a monitor-check job event.
func BackgroundJobMonitorInputFromEvent(
	event background.JobEvent,
	sessionID, rootScopeID, command string,
	origin background.JobOrigin,
) BackgroundJobMonitorInput {
	return BackgroundJobMonitorInput{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: sessionID,
		JobID:                 event.JobID,
		ParentTurnID:          origin.ParentTurnID,
		ParentEpisodeID:       origin.ParentEpisodeID,
		SettlementID:          origin.SettlementID,
		SettlementCohortID:    origin.SettlementCohortID,
		FinalizationPolicy:    origin.FinalizationPolicy,
		Status:                BackgroundJobPayloadString(event.Payload["status"]),
		Command:               command,
		Elapsed:               BackgroundJobPayloadDuration(event.Payload["elapsed_ms"]),
		CheckAfter:            BackgroundJobPayloadDuration(event.Payload["check_after_ms"]),
		MaxDuration:           BackgroundJobPayloadDuration(event.Payload["max_duration_ms"]),
		Epoch:                 BackgroundJobTerminalEpoch(event),
	}
}

func BackgroundJobPayloadString(value interface{}) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprintf("%v", value)
}

func BackgroundJobPayloadDuration(value interface{}) time.Duration {
	switch typed := value.(type) {
	case time.Duration:
		return typed
	case int64:
		return time.Duration(typed) * time.Millisecond
	case int:
		return time.Duration(typed) * time.Millisecond
	case float64:
		return time.Duration(typed) * time.Millisecond
	default:
		return 0
	}
}

func BackgroundJobTerminalEpoch(event background.JobEvent) int64 {
	if !event.CreatedAt.IsZero() {
		if epoch := event.CreatedAt.UTC().UnixNano(); epoch > 0 {
			return epoch
		}
	}
	return time.Now().UTC().UnixNano()
}
