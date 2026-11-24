package runtimeapi

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

const legacyTeamCompatibilityMode = "legacy_team_view"

// teamCompatibilityEnvelope describes the read contract of an existing Team
// record without presenting it as an independent Definition or Run resource.
// These capability values are intentionally conservative: a route or enum is
// not enough to claim that a versioned lifecycle operation exists.
func teamCompatibilityEnvelope(
	ctx context.Context,
	store team.Store,
	record *team.Team,
	resourceID string,
	requestID string,
	traceID string,
) map[string]interface{} {
	teamID := strings.TrimSpace(resourceID)
	status := ""
	summaryState := "unknown"
	if record != nil {
		if teamID == "" {
			teamID = strings.TrimSpace(record.ID)
		}
		status = string(record.Status)
		summaryState = "not_applicable"
		if team.IsTerminalTeamStatus(record.Status) {
			summaryState = "pending"
		}
	}

	eventSeq, eventSeqAvailable, err := teamEventHighWaterMark(ctx, store, teamID)
	if err != nil {
		eventSeqAvailable = false
	}
	if record != nil && team.IsTerminalTeamStatus(record.Status) {
		if summaryState, err = persistedTeamSummaryState(ctx, store, teamID); err != nil {
			summaryState = "unknown"
		}
	}

	capabilities := legacyTeamCapabilities()

	envelope := map[string]interface{}{
		"team_id":             teamID,
		"compatibility_mode":  legacyTeamCompatibilityMode,
		"team_version":        nil,
		"version_supported":   false,
		"team_status":         status,
		"event_seq":           nil,
		"event_seq_available": eventSeqAvailable,
		"cursor_scope":        "team_local",
		"summary_state":       summaryState,
		"capabilities":        capabilities,
	}
	if eventSeqAvailable {
		envelope["event_seq"] = eventSeq
	}
	if requestID = strings.TrimSpace(requestID); requestID != "" {
		envelope["request_id"] = requestID
	}
	if traceID = strings.TrimSpace(traceID); traceID != "" {
		envelope["trace_id"] = traceID
	}
	return envelope
}

func legacyTeamCapabilities() map[string]string {
	return map[string]string{
		"preflight":                      "supported",
		"team_event_polling":             "supported",
		"structured_task_outcome":        "supported",
		"opaque_result_reference":        "supported",
		"team_draft_create":              "unsupported",
		"controlled_team_start":          "unsupported",
		"worker_session_liveness":        "unsupported",
		"independent_definition":         "unsupported",
		"independent_run_history":        "unsupported",
		"team_pause":                     "unsupported",
		"team_resume":                    "unsupported",
		"team_cancel":                    "unsupported",
		"budget_hard_stop":               "unsupported",
		"event_stream":                   "unsupported",
		"artifact_download":              "unsupported",
		"artifact_retention":             "unsupported",
		"attempt_lineage":                "unsupported",
		"workspace_scoped_authorization": "unknown",
	}
}

func teamEventHighWaterMark(ctx context.Context, store team.Store, teamID string) (int64, bool, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return 0, true, nil
	}
	if sequenceStore, ok := store.(team.TeamEventSequenceStore); ok {
		seq, err := sequenceStore.LastTeamEventSeq(ctx, teamID)
		return seq, err == nil, err
	}

	// TeamEventSequenceStore is an optional optimization. Keep the cursor
	// truthful for compatible stores by deriving the high-water mark from their
	// durable event records rather than returning a misleading zero.
	events, err := store.ListTeamEvents(ctx, team.TeamEventFilter{TeamID: teamID})
	if err != nil {
		return 0, false, err
	}
	var latest int64
	for _, event := range events {
		if event.Seq > latest {
			latest = event.Seq
		}
	}
	return latest, true, nil
}

func persistedTeamSummaryState(ctx context.Context, store team.Store, teamID string) (string, error) {
	const pageSize = 256
	teamID = strings.TrimSpace(teamID)
	afterSeq := int64(0)
	var latestSummary *team.TeamEventRecord
	for {
		events, err := store.ListTeamEvents(ctx, team.TeamEventFilter{
			TeamID:    teamID,
			AfterSeq:  afterSeq,
			Limit:     pageSize,
			EventType: "team.summary*",
		})
		if err != nil {
			return "unknown", err
		}
		if len(events) == 0 {
			break
		}
		sort.SliceStable(events, func(i, j int) bool {
			return events[i].Seq < events[j].Seq
		})
		lastSeq := afterSeq
		for index := range events {
			event := events[index]
			if event.Seq <= afterSeq {
				continue
			}
			if event.Seq > lastSeq {
				lastSeq = event.Seq
			}
			if strings.HasPrefix(strings.TrimSpace(event.Type), "team.summary") &&
				(latestSummary == nil || event.Seq > latestSummary.Seq) {
				copy := event
				latestSummary = &copy
			}
		}
		if lastSeq <= afterSeq {
			return "unknown", errors.New("team summary event pagination did not advance")
		}
		if len(events) < pageSize {
			break
		}
		afterSeq = lastSeq
	}
	if latestSummary == nil {
		return "pending", nil
	}

	latest := *latestSummary
	switch strings.TrimSpace(latest.Type) {
	case "team.summary.failed":
		return "failed", nil
	case "team.summary":
		if strings.TrimSpace(stringPayloadValue(latest.Payload, "error_type")) != "" {
			return "failed", nil
		}
		if strings.TrimSpace(stringPayloadValue(latest.Payload, "summary")) != "" {
			return "ready", nil
		}
		return "pending", nil
	default:
		return "pending", nil
	}
}

func stringPayloadValue(payload map[string]interface{}, key string) string {
	value, _ := payload[key].(string)
	return value
}
