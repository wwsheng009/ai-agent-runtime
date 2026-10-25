//go:build windows

package searchcore

import (
	"os"
	"syscall"
	"time"
)

// grepFileTimesSupported reports that Windows exposes both access and creation
// time in the file attribute data, so the walker can implement every --sort key.
func grepFileTimesSupported() bool { return true }

func fileAccessTime(info os.FileInfo) (time.Time, bool) {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || data == nil {
		return time.Time{}, false
	}
	return time.Unix(0, data.LastAccessTime.Nanoseconds()), true
}

func fileCreationTime(info os.FileInfo) (time.Time, bool) {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || data == nil {
		return time.Time{}, false
	}
	return time.Unix(0, data.CreationTime.Nanoseconds()), true
}
