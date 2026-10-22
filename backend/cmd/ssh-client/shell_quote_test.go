package main

import (
	"strings"
	"testing"
)

// TestShellQuoteArg locks the POSIX single-quote escaping used by --stdio mode.
//
// ssh.Session.Run hands the whole command string to the remote shell, so a path
// containing a space, a quote or a `$` must survive verbatim (review B4).
func TestShellQuoteArg(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "view", "view"},
		{"path", "/usr/local/bin/aicli-remote-tools", "/usr/local/bin/aicli-remote-tools"},
		{"flag", "--workspace", "--workspace"},
		{"empty", "", "''"},
		{"space", "/srv/my app", "'/srv/my app'"},
		{"dollar", "$HOME/x", "'$HOME/x'"},
		{"backtick", "`id`", "'`id`'"},
		{"semicolon", "a;rm -rf /", "'a;rm -rf /'"},
		{"single quote", "it's", `'it'\''s'`},
		{"double quote", `say "hi"`, `'say "hi"'`},
		{"newline", "a\nb", "'a\nb'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shellQuoteArg(tc.input); got != tc.want {
				t.Fatalf("shellQuoteArg(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestShellQuoteJoin locks the joined form used for the remote exec string.
func TestShellQuoteJoin(t *testing.T) {
	argv := []string{
		"/usr/local/bin/aicli-remote-tools",
		"serve",
		"--stdio",
		"--workspace",
		"/srv/my app",
		"--tool-prefix",
		"remote_dev",
	}
	got := shellQuoteJoin(argv)
	want := "/usr/local/bin/aicli-remote-tools serve --stdio --workspace '/srv/my app' --tool-prefix remote_dev"
	if got != want {
		t.Fatalf("shellQuoteJoin = %q, want %q", got, want)
	}
	if strings.Count(got, "'") != 2 {
		t.Fatalf("expected exactly one quoted argument, got %q", got)
	}
}

// TestShellQuoteJoinEmpty covers the degenerate case without panicking.
func TestShellQuoteJoinEmpty(t *testing.T) {
	if got := shellQuoteJoin(nil); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}