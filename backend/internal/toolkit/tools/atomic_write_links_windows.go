//go:build windows

package tools

import "os"

// hasMultipleHardLinks cannot be answered portably from os.FileInfo on Windows.
// The shared mutatecore atomic writer keeps the same Windows fallback.
func hasMultipleHardLinks(os.FileInfo) bool { return false }
