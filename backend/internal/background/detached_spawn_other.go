//go:build !windows

package background

import "fmt"

// startDetachedProcess exists so the Windows-specific spawn helper compiles
// everywhere; Unix uses its own setsid/nohup launcher in startDetachedRunner.
func startDetachedProcess(exe string, args []string) (int, error) {
	return 0, fmt.Errorf("detached process spawn helper is not supported on this platform")
}
