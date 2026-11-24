package memorystore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCloseDeadLetterDigestIsAuditedAndTenantScoped(t *testing.T) {
	tenantA, tenantB := newG2TenantStores(t, "dead-letter-digest-project")
	ctx := context.Background()

	capture := TurnCapture{
		ID: "receipt-dead-letter", ProjectKey: "dead-letter-digest-project",
		SessionID: "session-dead-letter", SessionGeneration: 1, TurnID: "turn-dead-letter",
		MessageSeqStart: 1, MessageSeqEnd: 2, CanonicalRevision: 1, RangeHash: "range-hash",
	}
	materialized, err := tenantA.MaterializeTurnCapture(ctx, capture)
	require.NoError(t, err)

	closed, err := tenantA.CloseDeadLetterDigest(ctx, materialized.DigestID, "not dead-letter yet")
	require.Error(t, err)
	require.False(t, closed)

	require.NoError(t, tenantA.DeadLetterJob(ctx, materialized.JobID, "deterministic json parse failure"))
	closed, err = tenantA.CloseDeadLetterDigest(ctx, materialized.DigestID, "g0-5 json parse terminal")
	require.NoError(t, err)
	require.True(t, closed)

	var status, rollupStatus, lastError, processedAt string
	require.NoError(t, tenantA.db.QueryRowContext(ctx, `
SELECT status, rollup_status, COALESCE(last_error, ''), COALESCE(processed_at, '')
FROM memory_turn_digests WHERE id = ?
`, materialized.DigestID).Scan(&status, &rollupStatus, &lastError, &processedAt))
	require.Equal(t, string(TurnDigestInvalidated), status)
	require.Equal(t, string(TurnDigestRollupSkipped), rollupStatus)
	require.Equal(t, "g0-5 json parse terminal", lastError)
	require.NotEmpty(t, processedAt)

	var audits int
	require.NoError(t, tenantA.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_job_operator_audit
WHERE job_id = ? AND action = 'close_digest' AND new_status = 'invalidated'
`, materialized.JobID).Scan(&audits))
	require.Equal(t, 1, audits)

	closed, err = tenantA.CloseDeadLetterDigest(ctx, materialized.DigestID, "repeat")
	require.NoError(t, err)
	require.False(t, closed)

	closed, err = tenantB.CloseDeadLetterDigest(ctx, materialized.DigestID, "cross tenant")
	require.Error(t, err)
	require.False(t, closed)
}
