package sqliteutil

import (
	"errors"
	"strings"

	sqlite3 "github.com/ncruces/go-sqlite3"
)

// IsCorruptError reports whether err indicates that SQLite cannot trust the
// database image.  Drivers and database/sql often wrap the original error, so
// inspect the SQLite primary code first and keep a message fallback for VFS
// errors that lost their code while being re-wrapped.
func IsCorruptError(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr *sqlite3.Error
	if errors.As(err, &sqliteErr) {
		switch int(sqliteErr.Code()) & 0xff {
		case int(sqlite3.CORRUPT), int(sqlite3.NOTADB):
			return true
		}
	}
	message := strings.ToLower(err.Error())
	for _, pattern := range []string{
		"database disk image is malformed",
		"file is not a database",
		"not a database",
		"database corrupt",
		"malformed database",
	} {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}
