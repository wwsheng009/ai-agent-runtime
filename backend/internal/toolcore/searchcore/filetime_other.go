//go:build !linux && !windows

package searchcore

import (
	"os"
	"time"
)

// On hosts whose stat layout is not covered by the platform files, the walker
// cannot answer accessed/created ordering. The engine gets the first turn and
// the walker reports the missing capability instead of inventing an order.
func grepFileTimesSupported() bool { return false }

func fileAccessTime(os.FileInfo) (time.Time, bool)   { return time.Time{}, false }
func fileCreationTime(os.FileInfo) (time.Time, bool) { return time.Time{}, false }
