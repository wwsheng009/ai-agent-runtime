//go:build !win7compat

package aiclipaths

import "testing"

func TestStandardBuildProfileDefaults(t *testing.T) {
	if BuildProfile != "main" {
		t.Fatalf("BuildProfile = %q, want main", BuildProfile)
	}
	assertBuildProfileDefaults(t, "config.yaml", "aicli.yaml", "runtime.yaml", "session_history.sqlite")
}
