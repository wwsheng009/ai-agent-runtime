//go:build linux

package executor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// bubblewrapBackend enforces process isolation via bubblewrap (bwrap) when installed.
type bubblewrapBackend struct {
	lookPath func(file string) (string, error)

	mu        sync.Mutex
	resolved  string
	available *bool
}

func defaultOSSandboxBackend() OSSandboxBackend {
	return &bubblewrapBackend{lookPath: exec.LookPath}
}

func (b *bubblewrapBackend) Name() string { return "bubblewrap" }

func (b *bubblewrapBackend) Available(ctx context.Context) bool {
	_ = ctx
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.available != nil {
		return *b.available
	}
	look := b.lookPath
	if look == nil {
		look = exec.LookPath
	}
	path, err := look(bubblewrapBinary)
	ok := err == nil && strings.TrimSpace(path) != ""
	b.available = &ok
	if ok {
		b.resolved = path
	}
	return ok
}

func (b *bubblewrapBackend) Wrap(ctx context.Context, req OSSandboxRequest) (OSSandboxLaunch, error) {
	_ = ctx
	if b == nil {
		return OSSandboxLaunch{}, fmt.Errorf("bubblewrap backend is nil")
	}
	if !b.Available(ctx) {
		return OSSandboxLaunch{}, fmt.Errorf("bubblewrap (bwrap) is not available")
	}

	command, args, err := planBubblewrapArgs(req)
	if err != nil {
		return OSSandboxLaunch{}, err
	}

	// Prefer absolute path resolved by LookPath for stable exec.
	b.mu.Lock()
	resolved := b.resolved
	b.mu.Unlock()
	if strings.TrimSpace(resolved) != "" {
		command = resolved
	}

	return OSSandboxLaunch{
		Command: command,
		Args:    args,
		Env:     cloneStrings(req.Env),
		// WorkDir is applied inside bwrap via --chdir; clear host Dir to avoid
		// double-interpretation surprises when the outer process has no bind yet.
		WorkDir: "",
		Backend: b.Name(),
		Applied: true,
	}, nil
}
