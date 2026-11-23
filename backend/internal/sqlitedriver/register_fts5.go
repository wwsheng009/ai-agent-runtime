//go:build !win7compat

package sqlitedriver

import (
	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

// Register FTS5 on the mainline driver. The v0.35 base engine does not include
// FTS5, but the optional extension ships with the go-sqlite3 module and does
// not require CGO or a different database driver.
//
// win7compat is excluded because its pinned v0.22 driver does not provide the
// ext/fts5 package. That driver uses an embedded Wasm SQLite build which
// already includes FTS5, so no registration is needed there; memorystore still
// probes the capability at runtime and keeps LIKE as the fallback.
//
// Keep the registration here rather than in sqliteutil: SQLite consumers such
// as internal/mcp/catalog import sqlitedriver directly and use FTS5 without
// importing sqliteutil.
func init() {
	sqlite3.AutoExtension(fts5.Register)
}
