package memorystore

import "context"

func (s *Store) OperationalMetrics(ctx context.Context) (OperationalMetrics, error) {
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return OperationalMetrics{}, err
	}
	db, err := s.ensureV2()
	if err != nil {
		return OperationalMetrics{}, err
	}
	metrics := OperationalMetrics{
		TrustCounts:       map[string]int{},
		FeedbackByKind:    map[string]int{},
		PatchesByStatus:   map[string]int{},
		DreamRunsByStatus: map[string]int{},
	}
	readCounts := func(query string, target map[string]int) error {
		rows, err := db.QueryContext(ctx, query, scope.TenantID, scope.ProjectKey)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var value string
			var count int
			if err := rows.Scan(&value, &count); err != nil {
				return err
			}
			target[value] = count
		}
		return rows.Err()
	}
	if err := readCounts(`
SELECT trust_level, COUNT(1) FROM memory_entries
WHERE tenant_id = ? AND project_key = ? AND status = 'active'
GROUP BY trust_level
`, metrics.TrustCounts); err != nil {
		return OperationalMetrics{}, err
	}
	if err := readCounts(`
SELECT kind, COUNT(1) FROM memory_feedback
WHERE tenant_id = ? AND project_key = ?
GROUP BY kind
`, metrics.FeedbackByKind); err != nil {
		return OperationalMetrics{}, err
	}
	if err := readCounts(`
SELECT status, COUNT(1) FROM memory_patches
WHERE tenant_id = ? AND project_key = ?
GROUP BY status
`, metrics.PatchesByStatus); err != nil {
		return OperationalMetrics{}, err
	}
	if err := readCounts(`
SELECT status, COUNT(1) FROM memory_dream_runs
WHERE tenant_id = ? AND project_key = ?
GROUP BY status
`, metrics.DreamRunsByStatus); err != nil {
		return OperationalMetrics{}, err
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_entities WHERE tenant_id = ? AND project_key = ?
`, scope.TenantID, scope.ProjectKey).Scan(&metrics.EntityCount); err != nil {
		return OperationalMetrics{}, err
	}
	return metrics, nil
}
