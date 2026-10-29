package main

import "testing"

func TestServeFlagsAcceptShellOverride(t *testing.T) {
	flags := &serveFlags{}
	fs := newFlagSet("serve", flags)
	if err := fs.Parse([]string{"--shell", "pwsh"}); err != nil {
		t.Fatalf("parse --shell: %v", err)
	}
	if flags.shell != "pwsh" {
		t.Fatalf("shell flag = %q, want pwsh", flags.shell)
	}
}
