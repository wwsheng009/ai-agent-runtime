package supervision

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ReadResumeAdmissionMetrics aggregates the durable exactly-once ledger:
// claim states plus wake consumption kinds. It is intentionally a separate
// optional interface so the core Store contract stays usable by lightweight
// fakes and the metrics readout can degrade with a note.
func (s *SQLiteSupervisionStore) ReadResumeAdmissionMetrics(ctx context.Context, since, until time.Time) (ResumeAdmissionMetrics, error) {
	if s == nil {
		return ResumeAdmissionMetrics{}, nil
	}
	db, err := s.dbOrErr()
	if err != nil {
		return ResumeAdmissionMetrics{}, err
	}

	claimWhere, claimArgs := metricsTimeWindow("claimed_at", since, until)
	rows, err := db.QueryContext(ctx, `
		SELECT state, COUNT(*) FROM supervision_resume_claims
		WHERE `+claimWhere+`
		GROUP BY state
	`, claimArgs...)
	if err != nil {
		return ResumeAdmissionMetrics{}, fmt.Errorf("read resume claim states: %w", err)
	}
	var metrics ResumeAdmissionMetrics
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			_ = rows.Close()
			return ResumeAdmissionMetrics{}, fmt.Errorf("scan resume claim state: %w", err)
		}
		switch ResumeClaimState(strings.TrimSpace(state)) {
		case ResumeClaimStateClaimed:
			metrics.Claimed += count
		case ResumeClaimStateSubmitted:
			metrics.Submitted += count
		case ResumeClaimStateReleased:
			metrics.Released += count
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return ResumeAdmissionMetrics{}, fmt.Errorf("iterate resume claim states: %w", err)
	}
	_ = rows.Close()

	recoveredWhere, recoveredArgs := metricsTimeWindow("claimed_at", since, until)
	recoveredWhere += " AND model_turn_id = 'recovered_unknown'"
	recoveredQuery := `SELECT COUNT(*) FROM supervision_resume_claims WHERE ` + recoveredWhere
	if err := db.QueryRowContext(ctx, recoveredQuery, recoveredArgs...).Scan(&metrics.Recovered); err != nil {
		return ResumeAdmissionMetrics{}, fmt.Errorf("read recovered resume claims: %w", err)
	}

	deliveryWhere, deliveryArgs := metricsTimeWindow("delivered_at", since, until)
	deliveryRows, err := db.QueryContext(ctx, `
		SELECT consumption, COUNT(*) FROM supervision_wake_delivered
		WHERE `+deliveryWhere+`
		GROUP BY consumption
	`, deliveryArgs...)
	if err != nil {
		return ResumeAdmissionMetrics{}, fmt.Errorf("read wake consumption metrics: %w", err)
	}
	defer deliveryRows.Close()
	for deliveryRows.Next() {
		var consumption string
		var count int
		if err := deliveryRows.Scan(&consumption, &count); err != nil {
			return ResumeAdmissionMetrics{}, fmt.Errorf("scan wake consumption: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(consumption)) {
		case "", "model_turn":
			metrics.ModelTurns += count
		case "notification_only":
			metrics.NotificationOnly += count
		case "stale":
			metrics.Stale += count
		case "observed":
			metrics.Observed += count
		}
	}
	if err := deliveryRows.Err(); err != nil {
		return ResumeAdmissionMetrics{}, fmt.Errorf("iterate wake consumption metrics: %w", err)
	}
	return metrics, nil
}

func metricsTimeWindow(column string, since, until time.Time) (string, []interface{}) {
	clauses := []string{"1=1"}
	args := make([]interface{}, 0, 2)
	if !since.IsZero() {
		clauses = append(clauses, column+" >= ?")
		args = append(args, formatSupervisionTime(since.UTC()))
	}
	if !until.IsZero() {
		clauses = append(clauses, column+" <= ?")
		args = append(args, formatSupervisionTime(until.UTC()))
	}
	return strings.Join(clauses, " AND "), args
}
