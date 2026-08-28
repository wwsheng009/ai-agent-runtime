//go:build win7compat

package aiclipaths

import "testing"

func TestWin7BuildProfileDefaults(t *testing.T) {
	if BuildProfile != "win7" {
		t.Fatalf("BuildProfile = %q, want win7", BuildProfile)
	}
	assertBuildProfileDefaults(t, "config.win7.yaml", "aicli.win7.yaml", "runtime.win7.yaml", "session_history_win7.sqlite")
}
