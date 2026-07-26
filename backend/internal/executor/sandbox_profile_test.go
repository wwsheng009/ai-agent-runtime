package executor

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeSandboxProfile(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"off":      SandboxProfileOff,
		"disabled": SandboxProfileOff,
		"workspace": SandboxProfileWorkspace,
		"read-only": SandboxProfileReadOnly,
		"readonly":  SandboxProfileReadOnly,
		"strict":    SandboxProfileStrict,
	}
	for raw, want := range cases {
		got, err := NormalizeSandboxProfile(raw)
		if err != nil {
			t.Fatalf("NormalizeSandboxProfile(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("NormalizeSandboxProfile(%q)=%q want %q", raw, got, want)
		}
	}
	if _, err := NormalizeSandboxProfile("nope"); err == nil {
		t.Fatal("expected invalid profile error")
	}
}

func TestResolveSandboxProfile_WorkspaceAndReadOnly(t *testing.T) {
	root := t.TempDir()
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	ws, err := ResolveSandboxProfile(SandboxProfileWorkspace, SandboxProfileOptions{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if ws.Effective != SandboxProfileWorkspace || !ws.Config.Enabled {
		t.Fatalf("unexpected workspace result: %#v", ws)
	}
	if len(ws.Config.AllowedPaths) != 1 || filepath.Clean(ws.Config.AllowedPaths[0]) != filepath.Clean(abs) {
		t.Fatalf("unexpected allowed paths: %#v", ws.Config.AllowedPaths)
	}
	if ws.ReadOnly {
		t.Fatal("workspace profile must not force read-only")
	}

	ro, err := ResolveSandboxProfile(SandboxProfileReadOnly, SandboxProfileOptions{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("read-only: %v", err)
	}
	if !ro.ReadOnly || ro.Effective != SandboxProfileReadOnly {
		t.Fatalf("unexpected read-only result: %#v", ro)
	}
	if len(ro.Config.ReadOnlyPaths) != 1 {
		t.Fatalf("expected read-only path, got %#v", ro.Config.ReadOnlyPaths)
	}
	if len(ro.Config.DeniedCommands) == 0 {
		t.Fatal("expected denied commands for read-only")
	}
}

func TestResolveSandboxProfile_StrictBlocksNetwork(t *testing.T) {
	root := t.TempDir()
	strict, err := ResolveSandboxProfile(SandboxProfileStrict, SandboxProfileOptions{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("strict: %v", err)
	}
	if strict.Effective != SandboxProfileStrict || !strict.Config.BlockNetwork {
		t.Fatalf("unexpected strict result: %#v", strict)
	}
	if !strict.ReadOnly {
		t.Fatal("strict implies read-only")
	}
	if len(strict.Config.AllowedCommands) == 0 {
		t.Fatal("expected strict allowed commands")
	}

	sandbox := NewSandbox(&strict.Config)
	if err := sandbox.CheckURL("https://example.com"); err == nil {
		t.Fatal("expected network block under strict")
	}
	if err := sandbox.CheckPermission(OpWrite, filepath.Join(root, "file.txt")); err == nil {
		t.Fatal("expected write blocked under strict read-only paths")
	}
}

func TestResolveSandboxProfile_DowngradesWithoutWorkspace(t *testing.T) {
	ws, err := ResolveSandboxProfile(SandboxProfileWorkspace, SandboxProfileOptions{})
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if ws.Effective != SandboxProfileOff || len(ws.Warnings) == 0 {
		t.Fatalf("expected workspace downgrade to off with warning, got %#v", ws)
	}
	if strings.Index(ws.Warnings[0], "downgraded") < 0 {
		t.Fatalf("expected downgrade wording, got %q", ws.Warnings[0])
	}

	strict, err := ResolveSandboxProfile(SandboxProfileStrict, SandboxProfileOptions{})
	if err != nil {
		t.Fatalf("strict: %v", err)
	}
	if strict.Effective != SandboxProfileReadOnly {
		t.Fatalf("expected strict->read-only downgrade, got %#v", strict)
	}
	if !strict.Config.BlockNetwork {
		t.Fatal("downgraded strict should still block network")
	}
	if len(strict.Warnings) == 0 {
		t.Fatal("expected explicit downgrade warning")
	}
}

func TestResolveSandboxMap_ModeAndOverrides(t *testing.T) {
	root := t.TempDir()
	result, err := ResolveSandboxMap(map[string]interface{}{
		"mode":           "workspace",
		"deniedCommands": []string{"rm"},
		"allowedHosts":   []interface{}{"example.com"},
	}, SandboxProfileOptions{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("ResolveSandboxMap: %v", err)
	}
	if result.Effective != SandboxProfileWorkspace {
		t.Fatalf("effective=%q", result.Effective)
	}
	found := false
	for _, cmd := range result.Config.DeniedCommands {
		if cmd == "rm" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected override deniedCommands, got %#v", result.Config.DeniedCommands)
	}
	if len(result.Config.AllowedHosts) != 1 || result.Config.AllowedHosts[0] != "example.com" {
		t.Fatalf("unexpected allowed hosts: %#v", result.Config.AllowedHosts)
	}
}
