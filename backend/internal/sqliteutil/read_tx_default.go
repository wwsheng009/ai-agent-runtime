//go:build !win7compat

package sqliteutil

import "database/sql"

// ReadTxOptions requests a read-only transaction from the mainline SQLite
// driver. The current ncruces driver handles the query_only reset correctly.
var ReadTxOptions = &sql.TxOptions{ReadOnly: true}