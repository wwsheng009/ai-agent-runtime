package main

import (
	"os"
	"testing"
)

// parseArgsForTest drives the real flag parser the way main() does.
func parseArgsForTest(t *testing.T, argv []string) *cliFlags {
	t.Helper()
	previous := os.Args
	os.Args = append([]string{"ssh-client"}, argv...)
	t.Cleanup(func() { os.Args = previous })
	return parseFlags()
}

// TestDashDashSeparatorIsStripped locks the fix for the ssh-stdio transport.
//
// `SetInterspersed(false)` is required so a remote command's own flags are not
// parsed locally, but it also leaves the standard `--` marker in the operand
// list. remotetargets expands ssh-stdio targets as
//
//	ssh-client -T host -- <binary> serve --stdio --config-b64 <b64>
//
// so without the strip the remote side receives a command string starting with
// "--" and dies with `/bin/sh: 0: Illegal option --`. The previous in-process ssh
// test never involved ssh-client, which is why this survived to a live run.
func TestDashDashSeparatorIsStripped(t *testing.T) {
	flags := parseArgsForTest(t, []string{
		"-T",
		"-p", "2222",
		"--password", "secret",
		"--stdio",
		"tester@127.0.0.1",
		"--",
		"/usr/bin/aicli-remote-tools", "serve", "--stdio", "--config-b64", "eyJ4IjoxfQ==",
	})

	if flags.host != "tester@127.0.0.1" {
		t.Fatalf("host = %q, want tester@127.0.0.1", flags.host)
	}
	want := []string{"/usr/bin/aicli-remote-tools", "serve", "--stdio", "--config-b64", "eyJ4IjoxfQ=="}
	if len(flags.command) != len(want) {
		t.Fatalf("command = %q, want %q", flags.command, want)
	}
	for i := range want {
		if flags.command[i] != want[i] {
			t.Fatalf("command[%d] = %q, want %q (full: %q)", i, flags.command[i], want[i], flags.command)
		}
	}
	if !flags.stdioMode {
		t.Fatal("--stdio before the host must still be parsed as a local flag")
	}
	if flags.noTty != true {
		t.Fatal("-T must still be parsed as a local flag")
	}
}

// TestRemoteFlagsAfterHostAreNotLocal proves the reason SetInterspersed(false)
// exists: flags belonging to the remote program must not be interpreted here.
func TestRemoteFlagsAfterHostAreNotLocal(t *testing.T) {
	flags := parseArgsForTest(t, []string{
		"-T",
		"tester@host",
		"--stdio", // remote program's flag, not ours
		"--config-b64", "abc",
	})

	if flags.stdioMode {
		t.Fatal("--stdio after the host belongs to the remote command")
	}
	want := []string{"--stdio", "--config-b64", "abc"}
	if len(flags.command) != len(want) {
		t.Fatalf("command = %q, want %q", flags.command, want)
	}
	for i := range want {
		if flags.command[i] != want[i] {
			t.Fatalf("command[%d] = %q, want %q", i, flags.command[i], want[i])
		}
	}
}

// TestOrdinaryCommandStillWorks makes sure the strip is scoped: a normal
// invocation with no separator is unchanged.
func TestOrdinaryCommandStillWorks(t *testing.T) {
	flags := parseArgsForTest(t, []string{"tester@host", "ls", "-la"})
	if flags.host != "tester@host" {
		t.Fatalf("host = %q", flags.host)
	}
	if len(flags.command) != 2 || flags.command[0] != "ls" || flags.command[1] != "-la" {
		t.Fatalf("command = %q, want [ls -la]", flags.command)
	}
}

// TestOnlyLeadingSeparatorIsStripped keeps a literal `--` inside the remote
// command intact; only the marker position is special.
func TestOnlyLeadingSeparatorIsStripped(t *testing.T) {
	flags := parseArgsForTest(t, []string{"tester@host", "--", "echo", "--", "x"})
	want := []string{"echo", "--", "x"}
	if len(flags.command) != len(want) {
		t.Fatalf("command = %q, want %q", flags.command, want)
	}
	for i := range want {
		if flags.command[i] != want[i] {
			t.Fatalf("command[%d] = %q, want %q", i, flags.command[i], want[i])
		}
	}
}
