package executor

import (
	"reflect"
	"testing"
	"time"
)

func TestOverlaySandboxConfig_OverridesScalarsAndLists(t *testing.T) {
	base := SandboxConfig{
		Enabled:          false,
		MaxExecutionTime: 5 * time.Second,
		AllowedPaths:     []string{"/a"},
		DeniedCommands:   []string{"sh"},
	}
	override := SandboxConfig{
		MaxExecutionTime: 10 * time.Second,
		AllowedPaths:     []string{"/b"},
		DeniedCommands:   []string{"bash"},
	}

	OverlaySandboxConfig(&base, override)

	if !base.Enabled {
		t.Fatal("expected overlay to activate sandbox")
	}
	if base.MaxExecutionTime != 10*time.Second {
		t.Fatalf("expected MaxExecutionTime=10s, got %v", base.MaxExecutionTime)
	}
	if !reflect.DeepEqual(base.AllowedPaths, []string{"/b"}) {
		t.Fatalf("unexpected AllowedPaths: %#v", base.AllowedPaths)
	}
	if !reflect.DeepEqual(base.DeniedCommands, []string{"bash"}) {
		t.Fatalf("unexpected DeniedCommands: %#v", base.DeniedCommands)
	}
}

func TestCloneSandboxConfig_ReturnsDefensiveCopy(t *testing.T) {
	original := SandboxConfig{
		AllowedPaths:   []string{"/a"},
		AllowedHosts:   []string{"example.com"},
		DeniedCommands: []string{"powershell"},
	}

	cloned := CloneSandboxConfig(original)
	cloned.AllowedPaths[0] = "/changed"

	if original.AllowedPaths[0] != "/a" {
		t.Fatalf("expected original AllowedPaths to remain unchanged, got %#v", original.AllowedPaths)
	}
}
