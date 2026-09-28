//go:build !windows && !unix

package background

import (
	"fmt"
	"os/exec"
)

// processTree is unsupported on this platform; jobs fall back to single-pid
// kills with reaper verification.
type processTree struct{}

func attachProcessTree(pid int, killOnClose bool) (*processTree, error) {
	return nil, fmt.Errorf("process trees are not supported on this platform")
}

func (t *processTree) Terminate() error { return nil }

func (t *processTree) Release() {}

func (t *processTree) GroupID() int { return 0 }

func applyProcessGroup(cmd *exec.Cmd) {}
