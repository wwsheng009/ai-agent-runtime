//go:build windows

package localio

import (
	"context"
	"path/filepath"
	"strings"
)

// FilesystemIdentity names the volume a path lives on. Windows has no cheap st_dev, and the
// drive letter is the boundary users actually mean by "one file system" (a junction to
// another volume still has the same letter, so it is followed - the same trade the local
// tool has always made).
//
// Moved out of the search core unchanged, and note that unlike the Unix build this one
// reads nothing from the disk: it is a string operation on the path. That asymmetry is the
// host's, not the core's business, which is why it lives here.
func FilesystemIdentity(path string) (string, error) {
	abs := filepath.Clean(path)
	volume := strings.ToLower(filepath.VolumeName(abs))
	if volume == "" {
		volume = strings.ToLower(abs)
	}
	return volume, nil
}

// FilesystemIdentity is the seam's answer for this machine's filesystem.
func (osAccess) FilesystemIdentity(_ context.Context, path string) (string, error) {
	return FilesystemIdentity(path)
}
