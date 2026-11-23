// FTS5 registration for the shared SQLite driver.
//
// The ncruces/go-sqlite3 module ships FTS5 as an optional extension; without
// this auto-registration every connection answers
// "SQL logic error: no such module: fts5" (see
// memorystore.TestFTS5CapabilityProbe). Registering it here makes the
// capability available to all workspace databases opened through
// internal/sqliteutil, while FTS indexes remain derived data owned by each
// store.
package sqliteutil

import (
	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

func init() {
	sqlite3.AutoExtension(fts5.Register)
}
