package team

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ProjectTeamRunTerminalState copies the authoritative legacy Team terminal
// state into its compatibility TeamRun view. During the staged migration the
// orchestrator still owns the teams table; this method only advances the
// product-facing run projection and is safe to replay for every terminal
// lifecycle event.
func (s *SQLiteStore) ProjectTeamRunTerminalState(ctx context.Context, legacyTeamID string) error {
	if s == nil {
		return fmt.Errorf("team store is not initialized")
	}
	legacyTeamID = strings.TrimSpace(legacyTeamID)
	if legacyTeamID == "" {
		return fmt.Errorf("legacy team id is required")
	}
	if err := s.ensure(); err != nil {
		return err
	}

	return s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		var (
			status     string
			updatedRaw string
		)
		if err := tx.QueryRowContext(ctx, `
			SELECT status, updated_at
			FROM teams
			WHERE id = ?
		`, legacyTeamID).Scan(&status, &updatedRaw); err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return fmt.Errorf("load terminal Team for Run projection: %w", err)
		}
		if !IsTerminalTeamStatus(TeamStatus(status)) {
			return nil
		}

		var (
			eventSeq       int64
			summaryState   = TeamRunSummaryPending
			summaryType    sql.NullString
			summaryPayload sql.NullString
		)
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(seq), 0)
			FROM team_events
			WHERE team_id = ?
		`, legacyTeamID).Scan(&eventSeq); err != nil {
			return fmt.Errorf("load terminal Team event sequence: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT type, payload_json
			FROM team_events
			WHERE team_id = ? AND type LIKE 'team.summary%'
			ORDER BY seq DESC
			LIMIT 1
		`, legacyTeamID).Scan(&summaryType, &summaryPayload); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("load terminal Team summary event: %w", err)
		}
		if summaryType.Valid {
			payload := decodeMetadata(summaryPayload.String)
			switch strings.TrimSpace(summaryType.String) {
			case "team.summary.failed":
				summaryState = TeamRunSummaryFailed
			case "team.summary":
				if summary, _ := payload["summary"].(string); strings.TrimSpace(summary) != "" {
					if errorType, _ := payload["error_type"].(string); strings.TrimSpace(errorType) != "" {
						summaryState = TeamRunSummaryFailed
					} else {
						summaryState = TeamRunSummaryReady
					}
				}
			}
		}

		var (
			currentRunID    string
			currentStatus   string
			currentPhase    string
			currentSummary  string
			currentEventSeq int64
			currentTerminal sql.NullString
		)
		err := tx.QueryRowContext(ctx, `
			SELECT id, status, phase, summary_state, event_seq, terminal_at
			FROM team_runs
			WHERE legacy_team_id = ?
			ORDER BY created_at ASC, id ASC
			LIMIT 1
		`, legacyTeamID).Scan(
			&currentRunID,
			&currentStatus,
			&currentPhase,
			&currentSummary,
			&currentEventSeq,
			&currentTerminal,
		)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load Team Run for terminal projection: %w", err)
		}
		if currentStatus == status &&
			currentPhase == TeamRunPhaseTerminal &&
			currentSummary == summaryState &&
			currentEventSeq >= eventSeq &&
			currentTerminal.Valid &&
			strings.TrimSpace(currentTerminal.String) != "" {
			return nil
		}

		terminalAt := strings.TrimSpace(updatedRaw)
		if terminalAt == "" {
			terminalAt = formatTime(time.Now().UTC())
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE team_runs
			SET status = ?, phase = ?, event_seq = CASE WHEN event_seq < ? THEN ? ELSE event_seq END,
				summary_state = ?, version = version + 1, terminal_at = COALESCE(terminal_at, ?)
			WHERE id = ? AND legacy_team_id = ?
		`, status, TeamRunPhaseTerminal, eventSeq, eventSeq, summaryState, terminalAt, currentRunID, legacyTeamID); err != nil {
			return fmt.Errorf("project terminal Team state into Team Run: %w", err)
		}
		return nil
	})
}

var _ TeamRunLifecycleProjector = (*SQLiteStore)(nil)
