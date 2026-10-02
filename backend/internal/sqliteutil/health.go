package sqliteutil

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// HealthProbeEnv controls the optional read-only SQLite integrity probe used
// before store migrations.  The probe is deliberately opt-in because
// PRAGMA quick_check scans the entire database.
const HealthProbeEnv = "AICLI_SQLITE_HEALTH_PROBE"

// HealthProbeEnabled reports whether callers should run the opt-in quick
// integrity probe.
func HealthProbeEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(HealthProbeEnv)), "quick")
}

// QuickCheck runs SQLite's read-only quick integrity check and returns the
// first diagnostic row.  Callers should treat any result other than "ok" as
// corruption and must not continue with migrations or writes.
func QuickCheck(ctx context.Context, db *sql.DB) (string, error) {
	if db == nil {
		return "", fmt.Errorf("database is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return "", err
	}
	return result, nil
}
