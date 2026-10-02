//go:build win7compat

package sqliteutil

// The win7compat toolchain is pinned to the old Wasm SQLite driver and cannot
// use the fixed Windows shared-memory implementation.  Rollback journaling
// avoids the -wal/-shm wal-index path entirely.
func FileJournalMode() string { return "DELETE" }

func FileJournalUsesWAL() bool { return false }
