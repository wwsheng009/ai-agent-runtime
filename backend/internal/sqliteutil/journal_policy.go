//go:build !win7compat

package sqliteutil

// FileJournalMode is the journal mode used by shared file-backed stores in the
// main build.  The fixed ncruces driver supports the Windows WAL VFS on the
// supported Windows versions, so WAL remains the normal concurrency baseline.
func FileJournalMode() string { return "WAL" }

// FileJournalUsesWAL reports whether WAL-only connection pragmas are valid.
func FileJournalUsesWAL() bool { return true }
