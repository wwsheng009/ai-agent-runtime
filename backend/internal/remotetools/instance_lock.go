package remotetools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var errIdentityLocked = errors.New("mount identity is already in use")

// effectiveIdentityPath resolves the identity file that LoadOrCreateMountIdentity
// would use, so the lock covers both explicit and legacy/default identities.
func effectiveIdentityPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path != "" {
		return path, nil
	}
	return DefaultMountIdentityPath()
}

// acquireIdentityLock takes an OS-level exclusive lock next to the identity
// file. The lock is released automatically when the process exits or the file
// is closed, so a crashed serve never strands a stale PID/lock file.
func acquireIdentityLock(identityPath string) (*os.File, error) {
	lockPath := identityPath + ".lock"
	if dir := filepath.Dir(lockPath); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create mount identity dir %q: %w", dir, err)
		}
	}
	file, err := lockIdentityFile(lockPath)
	if err != nil {
		if errors.Is(err, errIdentityLocked) {
			return nil, fmt.Errorf(
				"mount identity %q is already in use by another remote-tools process; "+
					"start this service with --instance <id> to get a distinct identity",
				identityPath,
			)
		}
		return nil, fmt.Errorf("lock mount identity %q: %w", lockPath, err)
	}
	return file, nil
}
