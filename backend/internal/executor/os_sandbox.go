package executor

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// Optional OS-level sandbox modes. Application-layer SandboxConfig remains the
// always-on policy; OS backends only strengthen process isolation when available.
const (
	// OSSandboxModeOff never attempts OS isolation (default; safe for Windows/CI).
	OSSandboxModeOff = "off"
	// OSSandboxModeAuto uses an OS backend when available; otherwise continues
	// with application-layer policy and returns explicit degrade warnings.
	OSSandboxModeAuto = "auto"
	// OSSandboxModeRequire fails closed when no OS backend can enforce isolation.
	OSSandboxModeRequire = "require"
)

// OSSandboxBackend wraps process launches with optional OS-level isolation.
// Implementations must never silently claim isolation they cannot provide.
type OSSandboxBackend interface {
	// Name identifies the backend (e.g. "bubblewrap", "stub").
	Name() string
	// Available reports whether this host can actually enforce OS isolation.
	Available(ctx context.Context) bool
	// Wrap rewrites a command launch to run under OS isolation.
	// Callers must only invoke Wrap when Available is true, or accept an error.
	Wrap(ctx context.Context, req OSSandboxRequest) (OSSandboxLaunch, error)
}

// OSSandboxRequest describes a process launch candidate for OS wrapping.
type OSSandboxRequest struct {
	Command string
	Args    []string
	WorkDir string
	Env     []string
	// Config is the application-layer policy used to derive binds / network mode.
	Config SandboxConfig
}

// OSSandboxLaunch is the rewritten (or passthrough) process launch.
type OSSandboxLaunch struct {
	Command  string
	Args     []string
	Env      []string
	WorkDir  string
	// Backend is the backend name that produced this launch (empty when off).
	Backend string
	// Applied is true only when OS isolation was actually applied.
	Applied bool
	// Warnings carry explicit degrade / incomplete-enforcement notices.
	Warnings []string
}

// NormalizeOSSandboxMode canonicalizes optional OS sandbox mode values.
// Empty input defaults to off so existing deployments stay application-layer only.
func NormalizeOSSandboxMode(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", OSSandboxModeOff, "disabled", "none", "false":
		return OSSandboxModeOff, nil
	case OSSandboxModeAuto, "optional", "best-effort", "besteffort":
		return OSSandboxModeAuto, nil
	case OSSandboxModeRequire, "required", "must", "fail-closed", "failclosed":
		return OSSandboxModeRequire, nil
	default:
		return "", fmt.Errorf("invalid os sandbox mode %q (want off|auto|require)", raw)
	}
}

// DefaultOSSandboxBackend returns the platform default OS backend.
// On Linux this is bubblewrap when present; elsewhere a non-enforcing stub.
func DefaultOSSandboxBackend() OSSandboxBackend {
	return defaultOSSandboxBackend()
}

// PrepareOSCommand applies optional OS-level wrapping to a process launch.
// Application-layer validation remains the caller's responsibility.
//
// Behaviour by SandboxConfig.OSSandbox:
//   - off/empty: passthrough (Applied=false)
//   - auto: wrap when Available; otherwise passthrough + explicit warning
//   - require: wrap when Available; otherwise error (fail-closed)
func (s *Sandbox) PrepareOSCommand(ctx context.Context, command string, args []string, workDir string, env []string) (OSSandboxLaunch, error) {
	passthrough := OSSandboxLaunch{
		Command: command,
		Args:    cloneStrings(args),
		Env:     cloneStrings(env),
		WorkDir: workDir,
	}
	if s == nil {
		return passthrough, nil
	}

	mode, err := NormalizeOSSandboxMode(s.config.OSSandbox)
	if err != nil {
		return OSSandboxLaunch{}, err
	}
	if mode == OSSandboxModeOff {
		return passthrough, nil
	}

	backend := s.osBackend
	if backend == nil {
		backend = DefaultOSSandboxBackend()
	}
	backendName := strings.TrimSpace(backend.Name())
	if backendName == "" {
		backendName = "unknown"
	}

	if !backend.Available(ctx) {
		msg := fmt.Sprintf(
			"os sandbox mode %s: backend %q unavailable on this host; application-layer policy remains active",
			mode, backendName,
		)
		if mode == OSSandboxModeRequire {
			return OSSandboxLaunch{}, fmt.Errorf("%s (fail-closed)", msg)
		}
		passthrough.Backend = backendName
		passthrough.Warnings = append(passthrough.Warnings, msg)
		return passthrough, nil
	}

	launch, err := backend.Wrap(ctx, OSSandboxRequest{
		Command: command,
		Args:    cloneStrings(args),
		WorkDir: workDir,
		Env:     cloneStrings(env),
		Config:  s.Config(),
	})
	if err != nil {
		msg := fmt.Sprintf("os sandbox backend %q wrap failed: %v", backendName, err)
		if mode == OSSandboxModeRequire {
			return OSSandboxLaunch{}, fmt.Errorf("%s (fail-closed)", msg)
		}
		passthrough.Backend = backendName
		passthrough.Warnings = append(passthrough.Warnings, msg+"; continuing with application-layer policy only")
		return passthrough, nil
	}

	if strings.TrimSpace(launch.Backend) == "" {
		launch.Backend = backendName
	}
	if !launch.Applied {
		// Backend returned a non-applied launch — treat as explicit degrade.
		if mode == OSSandboxModeRequire {
			return OSSandboxLaunch{}, fmt.Errorf(
				"os sandbox mode require: backend %q did not apply isolation (fail-closed)",
				backendName,
			)
		}
		if len(launch.Warnings) == 0 {
			launch.Warnings = append(launch.Warnings,
				fmt.Sprintf("os sandbox mode auto: backend %q did not apply isolation; application-layer policy remains active", backendName))
		}
	}
	return launch, nil
}

// WithOSBackend returns a shallow copy of the sandbox using the given OS backend.
// A nil backend restores platform default resolution on PrepareOSCommand.
func (s *Sandbox) WithOSBackend(backend OSSandboxBackend) *Sandbox {
	if s == nil {
		return NewSandbox(nil).WithOSBackend(backend)
	}
	clone := *s
	clone.osBackend = backend
	return &clone
}

// OSBackend returns the explicitly configured OS backend, if any.
func (s *Sandbox) OSBackend() OSSandboxBackend {
	if s == nil {
		return nil
	}
	return s.osBackend
}

// CollectOSSandboxWarnings is a convenience helper for callers that only need
// degrade notices without rewriting argv (e.g. readiness probes).
func (s *Sandbox) CollectOSSandboxWarnings(ctx context.Context) []string {
	launch, err := s.PrepareOSCommand(ctx, "true", nil, "", nil)
	if err != nil {
		return []string{err.Error()}
	}
	return append([]string(nil), launch.Warnings...)
}

func cleanAbsPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("path is empty")
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
