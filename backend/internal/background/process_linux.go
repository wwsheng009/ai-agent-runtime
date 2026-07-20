//go:build linux

package background

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

type processHealth struct {
	Running  bool
	Zombie   bool
	Identity string
}

func inspectProcess(pid int) processHealth {
	if pid <= 0 {
		return processHealth{}
	}
	err := syscall.Kill(pid, 0)
	if err != nil && err != syscall.EPERM {
		return processHealth{}
	}
	health := processHealth{Running: true}
	data, readErr := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if readErr != nil {
		return health
	}
	closeParen := strings.LastIndexByte(string(data), ')')
	if closeParen < 0 || closeParen+2 >= len(data) {
		return health
	}
	fields := strings.Fields(string(data)[closeParen+2:])
	if len(fields) > 0 && fields[0] == "Z" {
		health.Zombie = true
	}
	// /proc stat field 22 is the process start time; after the comm field it
	// is index 19. It is stable for the lifetime of a PID.
	if len(fields) > 19 {
		health.Identity = fields[19]
	}
	return health
}
