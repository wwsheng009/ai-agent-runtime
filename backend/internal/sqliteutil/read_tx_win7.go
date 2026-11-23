//go:build win7compat

package sqliteutil

import "database/sql"

// ReadTxOptions avoids sql.TxOptions.ReadOnly on the win7compat v0.22 driver.
// That driver resets query_only using an uninitialized byte when the DSN has
// no _pragma/init callback: both COMMIT and ROLLBACK then execute an invalid
// "PRAGMA query_only=" statement, fail with "incomplete input", and leave the
// pooled connection in query_only mode. Callers of this option only read, so a
// normal deferred transaction is safe and lets the driver's COMMIT/ROLLBACK
// path stay valid.
var ReadTxOptions = &sql.TxOptions{}