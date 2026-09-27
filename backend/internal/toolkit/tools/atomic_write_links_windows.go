//go:build windows

package tools

import "os"

// hasMultipleHardLinks cannot be answered portably from os.FileInfo on Windows
// (link count needs GetFileInformationByHandle), so the atomic rename path is
// kept there; symlink targets are still written through writeFileInPlace.
func hasMultipleHardLinks(os.FileInfo) bool { return false }
