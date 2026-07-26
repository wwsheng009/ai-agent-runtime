package executor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fakeOSBackend struct {
	name      string
	available bool
	wrap      func(ctx context.Context, req OSSandboxRequest) (OSSandboxLaunch, error)
}

func (f fakeOSBackend) Name() string { return f.name }

func (f fakeOSBackend) Available(context.Context) bool { return f.available }

func (f fakeOSBackend) Wrap(ctx context.Context, req OSSandboxRequest) (OSSandboxLaunch, error) {
	if f.wrap != nil {
		return f.wrap(ctx, req)
	}
	return OSSandboxLaunch{
		Command: "wrapped",
		Args:    append([]string{"--", req.Command}, req.Args...),
		Env:     cloneStrings(req.Env),
		WorkDir: req.WorkDir,
		Backend: f.name,
		Applied: true,
	}, nil
}

func TestNormalizeOSSandboxMode(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", OSSandboxModeOff, false},
		{"off", OSSandboxModeOff, false},
		{"AUTO", OSSandboxModeAuto, false},
		{"best-effort", OSSandboxModeAuto, false},
		{"require", OSSandboxModeRequire, false},
		{"fail-closed", OSSandboxModeRequire, false},
		{"weird", "", true},
	}
	for _, tc := range cases {
		got, err := NormalizeOSSandboxMode(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("NormalizeOSSandboxMode(%q) expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("NormalizeOSSandboxMode(%q) unexpected err: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("NormalizeOSSandboxMode(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestPrepareOSCommand_ModeOffPassthrough(t *testing.T) {
	s := NewSandbox(&SandboxConfig{Enabled: true, OSSandbox: OSSandboxModeOff})
	s = s.WithOSBackend(fakeOSBackend{name: "fake", available: true})
	launch, err := s.PrepareOSCommand(context.Background(), "echo", []string{"hi"}, "/tmp", []string{"A=1"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if launch.Applied || launch.Command != "echo" {
		t.Fatalf("expected passthrough, got %#v", launch)
	}
	if len(launch.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", launch.Warnings)
	}
}

func TestPrepareOSCommand_AutoDegradesWhenUnavailable(t *testing.T) {
	s := NewSandbox(&SandboxConfig{Enabled: true, OSSandbox: OSSandboxModeAuto})
	s = s.WithOSBackend(fakeOSBackend{name: "fake", available: false})
	launch, err := s.PrepareOSCommand(context.Background(), "echo", []string{"hi"}, "", nil)
	if err != nil {
		t.Fatalf("auto should not fail-closed, got %v", err)
	}
	if launch.Applied {
		t.Fatal("expected Applied=false on degrade")
	}
	if len(launch.Warnings) == 0 || !strings.Contains(launch.Warnings[0], "unavailable") {
		t.Fatalf("expected unavailable warning, got %#v", launch.Warnings)
	}
	if launch.Command != "echo" {
		t.Fatalf("expected original command, got %q", launch.Command)
	}
}

func TestPrepareOSCommand_RequireFailsClosedWhenUnavailable(t *testing.T) {
	s := NewSandbox(&SandboxConfig{Enabled: true, OSSandbox: OSSandboxModeRequire})
	s = s.WithOSBackend(fakeOSBackend{name: "fake", available: false})
	_, err := s.PrepareOSCommand(context.Background(), "echo", nil, "", nil)
	if err == nil || !strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("expected fail-closed error, got %v", err)
	}
}

func TestPrepareOSCommand_AutoWrapsWhenAvailable(t *testing.T) {
	s := NewSandbox(&SandboxConfig{Enabled: true, OSSandbox: "auto"})
	s = s.WithOSBackend(fakeOSBackend{name: "fake", available: true})
	launch, err := s.PrepareOSCommand(context.Background(), "echo", []string{"hi"}, "/work", []string{"A=1"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !launch.Applied || launch.Command != "wrapped" || launch.Backend != "fake" {
		t.Fatalf("expected applied wrap, got %#v", launch)
	}
}

func TestPrepareOSCommand_AutoDegradesOnWrapError(t *testing.T) {
	s := NewSandbox(&SandboxConfig{Enabled: true, OSSandbox: OSSandboxModeAuto})
	s = s.WithOSBackend(fakeOSBackend{
		name:      "fake",
		available: true,
		wrap: func(context.Context, OSSandboxRequest) (OSSandboxLaunch, error) {
			return OSSandboxLaunch{}, context.DeadlineExceeded
		},
	})
	launch, err := s.PrepareOSCommand(context.Background(), "echo", nil, "", nil)
	if err != nil {
		t.Fatalf("auto should degrade on wrap error, got %v", err)
	}
	if launch.Applied || len(launch.Warnings) == 0 {
		t.Fatalf("expected degrade warning, got %#v", launch)
	}
}

func TestPrepareOSCommand_RequireFailsOnWrapError(t *testing.T) {
	s := NewSandbox(&SandboxConfig{Enabled: true, OSSandbox: OSSandboxModeRequire})
	s = s.WithOSBackend(fakeOSBackend{
		name:      "fake",
		available: true,
		wrap: func(context.Context, OSSandboxRequest) (OSSandboxLaunch, error) {
			return OSSandboxLaunch{}, context.Canceled
		},
	})
	_, err := s.PrepareOSCommand(context.Background(), "echo", nil, "", nil)
	if err == nil || !strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("expected fail-closed wrap error, got %v", err)
	}
}

func TestDefaultOSSandboxBackend_Platform(t *testing.T) {
	backend := DefaultOSSandboxBackend()
	if backend == nil {
		t.Fatal("expected default backend")
	}
	switch runtime.GOOS {
	case "linux":
		if backend.Name() != "bubblewrap" {
			t.Fatalf("linux default backend name=%q want bubblewrap", backend.Name())
		}
	default:
		if backend.Name() != "stub" {
			t.Fatalf("non-linux default backend name=%q want stub", backend.Name())
		}
		if backend.Available(context.Background()) {
			t.Fatal("stub must not report available")
		}
		if _, err := backend.Wrap(context.Background(), OSSandboxRequest{Command: "echo"}); err == nil {
			t.Fatal("stub Wrap must error")
		}
	}
}

func TestPlanBubblewrapArgs_BlockNetworkAndBinds(t *testing.T) {
	root := t.TempDir()
	ro := filepath.Join(root, "ro")
	rw := filepath.Join(root, "rw")
	if err := os.MkdirAll(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rw, 0o755); err != nil {
		t.Fatal(err)
	}
	roAbs, _ := filepath.Abs(ro)
	rwAbs, _ := filepath.Abs(rw)

	cmd, args, err := planBubblewrapArgs(OSSandboxRequest{
		Command: "git",
		Args:    []string{"status"},
		WorkDir: rw,
		Config: SandboxConfig{
			BlockNetwork:  true,
			AllowedPaths:  []string{rw},
			ReadOnlyPaths: []string{ro},
		},
	})
	if err != nil {
		t.Fatalf("plan error: %v", err)
	}
	if cmd != bubblewrapBinary {
		t.Fatalf("command=%q want %q", cmd, bubblewrapBinary)
	}
	if !containsArgSeq(args, "--die-with-parent") {
		t.Fatalf("missing --die-with-parent: %#v", args)
	}
	if !containsArgSeq(args, "--unshare-net") {
		t.Fatalf("missing --unshare-net: %#v", args)
	}
	if !containsArgSeq(args, "--bind", rwAbs, rwAbs) {
		t.Fatalf("missing rw bind for %s in %#v", rwAbs, args)
	}
	if !containsArgSeq(args, "--ro-bind", roAbs, roAbs) {
		t.Fatalf("missing ro bind for %s in %#v", roAbs, args)
	}
	if !containsArgSeq(args, "--chdir", rwAbs) {
		t.Fatalf("missing chdir for %s in %#v", rwAbs, args)
	}
	if !containsArgSeq(args, "--", "git", "status") {
		t.Fatalf("missing guest command in %#v", args)
	}
}

func TestPlanBubblewrapArgs_EmptyCommand(t *testing.T) {
	if _, _, err := planBubblewrapArgs(OSSandboxRequest{}); err == nil {
		t.Fatal("expected empty command error")
	}
}

func TestOverlaySandboxConfig_OSSandbox(t *testing.T) {
	base := SandboxConfig{OSSandbox: OSSandboxModeOff}
	OverlaySandboxConfig(&base, SandboxConfig{OSSandbox: "require"})
	if base.OSSandbox != OSSandboxModeRequire {
		t.Fatalf("expected require overlay, got %q", base.OSSandbox)
	}
	// Empty override must not clobber.
	OverlaySandboxConfig(&base, SandboxConfig{})
	if base.OSSandbox != OSSandboxModeRequire {
		t.Fatalf("empty overlay clobbered OSSandbox: %q", base.OSSandbox)
	}
}

func TestSandboxConfigActive_OSSandbox(t *testing.T) {
	if SandboxConfigActive(SandboxConfig{OSSandbox: OSSandboxModeOff}) {
		t.Fatal("off should not count as active by itself")
	}
	if !SandboxConfigActive(SandboxConfig{OSSandbox: OSSandboxModeAuto}) {
		t.Fatal("auto should count as active")
	}
}

func TestDecodeSandboxMapConfig_OSSandbox(t *testing.T) {
	cfg, err := decodeSandboxMapConfig(map[string]interface{}{
		"mode":      "workspace",
		"osSandbox": "auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OSSandbox != "auto" {
		t.Fatalf("osSandbox=%q want auto", cfg.OSSandbox)
	}
}

func containsArgSeq(args []string, seq ...string) bool {
	if len(seq) == 0 {
		return true
	}
	for i := 0; i+len(seq) <= len(args); i++ {
		match := true
		for j := range seq {
			if args[i+j] != seq[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
