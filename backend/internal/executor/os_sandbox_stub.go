//go:build !linux

package executor

import (
	"context"
	"fmt"
)

// stubOSSandboxBackend is the non-Linux OS sandbox backend.
// It never claims isolation; callers must treat Available=false as explicit degrade.
type stubOSSandboxBackend struct{}

func defaultOSSandboxBackend() OSSandboxBackend {
	return stubOSSandboxBackend{}
}

func (stubOSSandboxBackend) Name() string { return "stub" }

func (stubOSSandboxBackend) Available(context.Context) bool { return false }

func (stubOSSandboxBackend) Wrap(context.Context, OSSandboxRequest) (OSSandboxLaunch, error) {
	return OSSandboxLaunch{}, fmt.Errorf("os sandbox backend stub cannot enforce isolation on this platform")
}
