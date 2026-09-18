package sqliteutil

import (
	"context"
	"database/sql"
	"errors"
	"time"

	sqlite3 "github.com/ncruces/go-sqlite3"
)

// WriteTxOptions makes database/sql open SQLite write transactions with
// BEGIN IMMEDIATE: the driver maps sql.LevelSerializable to IMMEDIATE
// (see github.com/ncruces/go-sqlite3/driver: "a serializable transaction is
// always immediate").
//
// Why this is required: a DEFERRED transaction that reads before writing
// (e.g. SELECT MAX(seq) followed by INSERT) must promote its read snapshot to
// a write lock. In WAL mode that promotion fails with SQLITE_BUSY_SNAPSHOT
// (517) as soon as another connection or process has appended to the WAL in
// the meantime - and retrying the SAME transaction can never succeed, because
// its snapshot is already stale. BEGIN IMMEDIATE takes the write lock up
// front, so reads and writes inside the transaction share one uncontended
// snapshot.
//
// Use it for every write transaction, especially the read-then-write ones:
//
//	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
var WriteTxOptions = &sql.TxOptions{Isolation: sql.LevelSerializable}

// WriteRetries is the default number of total attempts (first try + retries)
// performed by RetryWriteTx.
const WriteRetries = 3

// writeRetryBaseDelay is the first backoff step (then doubled per retry:
// 50ms, 100ms).
const writeRetryBaseDelay = 50 * time.Millisecond

// IsBusyError reports whether err is SQLITE_BUSY or one of its extended
// variants (BUSY_RECOVERY / BUSY_SNAPSHOT / BUSY_TIMEOUT). The driver returns
// busy conditions either as sqlite3.ErrorCode values or as *sqlite3.Error;
// (*sqlite3.Error).Is matches both plain and extended codes, and errors.Is
// unwraps fmt.Errorf("...: %w", err) wrappers.
func IsBusyError(err error) bool {
	return err != nil && errors.Is(err, sqlite3.BUSY)
}

// IsBusySnapshotError reports the read->write promotion failure specifically
// (SQLITE_BUSY_SNAPSHOT, extended result code 517). This is the error that
// BEGIN IMMEDIATE prevents and that can never be fixed by retrying the same
// transaction.
func IsBusySnapshotError(err error) bool {
	return err != nil && errors.Is(err, sqlite3.BUSY_SNAPSHOT)
}

// RetryWriteTx runs fn with a fresh transaction attempt when it fails with a
// retryable SQLite busy error (WriteRetries total attempts, 50ms/100ms
// backoff, bounded by ctx).
//
// fn must be self-contained: it opens its own transaction with WriteTxOptions,
// rolls back on error and commits on success. A retry always starts a brand
// new transaction; fn must not reuse the failed one (a stale snapshot cannot
// be repaired). onRetry, when non-nil, is invoked before each retry with the
// busy error and the 1-based retry index.
func RetryWriteTx(ctx context.Context, onRetry func(err error, retry int), fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if fn == nil {
		return nil
	}
	var lastErr error
	for attempt := 0; attempt < WriteRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = fn(ctx)
		if lastErr == nil {
			return nil
		}
		if !IsBusyError(lastErr) || attempt == WriteRetries-1 {
			return lastErr
		}
		if onRetry != nil {
			onRetry(lastErr, attempt+1)
		}
		select {
		case <-time.After(writeRetryBaseDelay << uint(attempt)):
		case <-ctx.Done():
			return lastErr
		}
	}
	return lastErr
}
