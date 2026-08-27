package chat

import (
	"fmt"
	"strings"
	"time"
)

const (
	// sqliteLockRetries is how many extra open attempts run after the first
	// one fails with a transient "database is locked" error. A concurrent
	// aicli/runtime-server process can hold the write lock longer than
	// busy_timeout (migration, large session flush, wal_checkpoint(TRUNCATE)),
	// so startup must retry instead of failing the whole program.
	sqliteLockRetries = 10

	sqliteLockRetryBaseWait = 50 * time.Millisecond
	sqliteLockRetryMaxWait  = 500 * time.Millisecond
)

// isSQLiteLockedError reports whether err represents a transient SQLite lock
// failure caused by another connection/process holding the database lock.
func isSQLiteLockedError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", err)))
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked")
}

// sqliteLockRetryWait returns the backoff delay before the attempt at the
// given 0-based retry index (attempt 0 is the first retry).
func sqliteLockRetryWait(retryIndex int) time.Duration {
	wait := sqliteLockRetryBaseWait * time.Duration(retryIndex+1)
	if wait > sqliteLockRetryMaxWait {
		wait = sqliteLockRetryMaxWait
	}
	return wait
}
