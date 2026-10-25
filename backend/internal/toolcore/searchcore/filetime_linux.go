//go:build linux

package searchcore

import (
	"os"
	"syscall"
	"time"
)

// grepFileTimesSupported reports that linux exposes both access and creation
// time through Stat_t, so the walker can implement every --sort key.
func grepFileTimesSupported() bool { return true }

func fileAccessTime(info os.FileInfo) (time.Time, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return time.Time{}, false
	}
	return time.Unix(stat.Atim.Sec, stat.Atim.Nsec), true
}

func fileCreationTime(info os.FileInfo) (time.Time, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return time.Time{}, false
	}
	return time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec), true
}
