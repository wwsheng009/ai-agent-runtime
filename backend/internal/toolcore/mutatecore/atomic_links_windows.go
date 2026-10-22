//go:build windows

package mutatecore

import "os"

// hasMultipleHardLinks cannot be answered portably from os.FileInfo on Windows.
func hasMultipleHardLinks(os.FileInfo) bool { return false }
