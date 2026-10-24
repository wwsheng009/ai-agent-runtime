//go:build windows

package searchcore

import (
	"path/filepath"
	"strings"
)

// FilesystemIdentityFunc is the probe one_file_system uses to decide whether a
// directory still belongs to the search root's volume. See the Unix build for
// the rationale; only tests reassign it.
var FilesystemIdentityFunc = filesystemIdentity

// filesystemIdentity names the volume a path lives on. Windows has no cheap
// st_dev, and the drive letter is the boundary users actually mean by "one file
// system" (a junction to another volume still has the same letter, so it is
// followed - the same trade the local tool has always made).
func filesystemIdentity(path string) (string, error) {
	abs := filepath.Clean(path)
	volume := strings.ToLower(filepath.VolumeName(abs))
	if volume == "" {
		volume = strings.ToLower(abs)
	}
	return volume, nil
}
