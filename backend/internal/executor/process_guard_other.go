//go:build !windows

package executor

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// platformGuard tracks a Unix command tree via its own process group: the shell
// is started with Setpgid so every descendant stays in that group and can be
// killed with kill(-pgid).
type platformGuard struct {
	group bool
}

func newPlatformGuard() platformGuard {
	return platformGuard{}
}

func (p *platformGuard) bind(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	p.group = true
	return nil
}

func (p *platformGuard) attach(*os.Process) error { return nil }

func (p *platformGuard) terminate(pid int) TerminationReport {
	if pid <= 0 {
		return TerminationReport{}
	}
	rep := TerminationReport{}
	if p.group {
		if err := syscall.Kill(-pid, syscall.SIGKILL); err == nil {
			rep.TreeKill = true
			rep.Mode = "process_group"
			rep.Killed = appendPIDUnique(rep.Killed, pid)
			return rep
		} else {
			rep.Err = err.Error()
		}
	}
	if proc, err := os.FindProcess(pid); err == nil {
		if killErr := proc.Kill(); killErr == nil {
			rep.TreeKill = true
			rep.Mode = "direct_kill"
			rep.Killed = appendPIDUnique(rep.Killed, pid)
			return rep
		}
	}
	return rep
}

// livePIDs returns the still-running (non-zombie) members of the guarded
// process group, whose id is rootPID (Setpgid made the command a group
// leader). Linux exposes the process-group id in /proc/<pid>/stat, so one
// directory scan finds every descendant - including grandchildren that were
// reparented to init after the command root exited. On Unix systems without
// /proc the scan degrades to no tracked leftovers.
func (p *platformGuard) livePIDs(rootPID int) []int {
	if !p.group || rootPID <= 0 {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	pids := make([]int, 0, 8)
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 0 || pid == rootPID {
			continue
		}
		state, pgrp, ok := readProcessStat(pid)
		if !ok || pgrp != rootPID {
			continue
		}
		if state == 'Z' || state == 'X' {
			// A zombie already exited; it only lingers until its parent reaps
			// it and can never be terminated again.
			continue
		}
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids
}

// readProcessStat parses the state and process-group id out of
// /proc/<pid>/stat. comm may contain spaces and parentheses, so parsing starts
// after the last ')'.
func readProcessStat(pid int) (state byte, pgrp int, ok bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, false
	}
	text := string(data)
	idx := strings.LastIndexByte(text, ')')
	if idx < 0 || idx+2 >= len(text) {
		return 0, 0, false
	}
	fields := strings.Fields(text[idx+2:])
	if len(fields) < 3 || len(fields[0]) == 0 {
		return 0, 0, false
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, false
	}
	return fields[0][0], group, true
}

func (p *platformGuard) close(bool) {}
