package usageledger

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

// ListExplorationAttribution 返回 created_at >= since 的归因行（按时间升序）。
//
// 与 AppendExplorationAttribution 成对：写入口径见 ADR-0003 §4.1，本读取口
// 供 Phase1-shadow 的 M1–M4 复算（ADR-0003 §4.5 / §8）使用。limit <= 0 时
// 使用默认上限，防止误调用把整表拉进内存。
func (s *SQLiteStore) ListExplorationAttribution(ctx context.Context, since time.Time, limit int) ([]*entity.ExplorationAttribution, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("usageledger: list exploration attribution: store is not open")
	}
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(session_id, ''), COALESCE(turn_id, ''), COALESCE(request_id, ''),
		       tool, COALESCE(query_hash, ''), COALESCE(project_id, ''),
		       baseline_n, baseline_files_n, candidate_n, overlap_n, overlap_files_n,
		       baseline_tokens, candidate_tokens,
		       coverage, economy, usable, COALESCE(source, ''), knowledge_mode, created_at
		FROM exploration_attribution
		WHERE created_at >= ?
		ORDER BY created_at, id
		LIMIT ?`, since.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("usageledger: list exploration attribution: %w", err)
	}
	defer rows.Close()

	var out []*entity.ExplorationAttribution
	for rows.Next() {
		rec := &entity.ExplorationAttribution{}
		var (
			coverage  sql.NullFloat64
			economy   sql.NullFloat64
			usable    int
			createdAt string
		)
		if err := rows.Scan(
			&rec.ID, &rec.SessionID, &rec.TurnID, &rec.RequestID,
			&rec.Tool, &rec.QueryHash, &rec.ProjectID,
			&rec.BaselineN, &rec.BaselineFilesN, &rec.CandidateN, &rec.OverlapN, &rec.OverlapFilesN,
			&rec.BaselineTokens, &rec.CandidateTokens,
			&coverage, &economy, &usable, &rec.Source, &rec.KnowledgeMode, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("usageledger: scan exploration attribution: %w", err)
		}
		if coverage.Valid {
			v := coverage.Float64
			rec.Coverage = &v
		}
		if economy.Valid {
			v := economy.Float64
			rec.Economy = &v
		}
		rec.Usable = usable != 0
		ts, err := parseAttributionTime(createdAt)
		if err != nil {
			return nil, err
		}
		rec.CreatedAt = ts
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("usageledger: list exploration attribution: %w", err)
	}
	return out, nil
}

// parseAttributionTime 兼容历史行的时间格式（写入端固定 RFC3339Nano）。
func parseAttributionTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("usageledger: parse exploration attribution created_at %q", raw)
}
