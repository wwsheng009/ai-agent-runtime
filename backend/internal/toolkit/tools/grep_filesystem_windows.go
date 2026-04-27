//go:build windows

package tools

import (
	"path/filepath"
	"strings"
)

func fileSystemIdentity(path string) (string, error) {
	abs := filepath.Clean(path)
	vol := strings.ToLower(filepath.VolumeName(abs))
	if vol == "" {
		vol = strings.ToLower(abs)
	}
	return vol, nil
}
