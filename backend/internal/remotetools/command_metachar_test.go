package remotetools

import (
	"strings"
	"testing"
)

func TestDetectShellMetacharsReportsEveryFindingInOnePass(t *testing.T) {
	reasons := detectShellMetachars("a; b | c > d", false)
	if len(reasons) != 3 {
		t.Fatalf("reasons = %v, want 3 findings (; | >) so one repair round is enough", reasons)
	}
}

func TestDetectShellMetacharsMasksConstructsItAlreadyReported(t *testing.T) {
	// "||" must not also be reported as a bare pipe.
	reasons := detectShellMetachars("a || b", false)
	if len(reasons) != 1 {
		t.Fatalf("reasons = %v, want only the || separator", reasons)
	}
	if !strings.Contains(reasons[0], "||") {
		t.Fatalf("reason = %q, want the || separator", reasons[0])
	}
}

func TestDetectShellMetacharsIgnoresQuotedSpans(t *testing.T) {
	for _, command := range []string{`echo "a;b"`, `echo 'a|b'`, `git log --grep="x;y"`} {
		if reasons := detectShellMetachars(command, true); len(reasons) != 0 {
			t.Fatalf("detectShellMetachars(%q) = %v, want no findings inside quotes", command, reasons)
		}
	}
}

func TestStripQuotedSpansOnlyHonorsEscapesForPosixShells(t *testing.T) {
	// bash/zsh/sh: "a\;b" is a literal semicolon, so it is not a separator.
	if reasons := detectShellMetachars(`echo a\;b`, true); len(reasons) != 0 {
		t.Fatalf("posix escape reported as a separator: %v", reasons)
	}
	// PowerShell/cmd: the backslash is an ordinary character, so ";" still
	// separates commands and must keep being denied.
	if reasons := detectShellMetachars(`echo a\;b`, false); len(reasons) == 0 {
		t.Fatal("escaped-looking separator must still be denied on shells without backslash escapes")
	}
}

func TestShellHonorsBackslashEscapes(t *testing.T) {
	cases := map[string]bool{
		"bash":          true,
		"/bin/zsh":      true,
		"sh":            true,
		"pwsh":          false,
		"powershell":    false,
		"pwsh.exe":      false,
		`C:\Program Files\PowerShell\7\pwsh.exe`: false,
		"cmd": false,
	}
	for program, want := range cases {
		if got := shellHonorsBackslashEscapes(program); got != want {
			t.Fatalf("shellHonorsBackslashEscapes(%q) = %v, want %v", program, got, want)
		}
	}
}

func TestCommandPolicyKeepsEscapedSeparatorDeniedOnPowerShell(t *testing.T) {
	policy, err := NewCommandPolicy(ShellConfig{
		Program:            "pwsh",
		AllowedCommands:    []string{"*"},
		DenyShellMetachars: true,
	}, 0)
	if err != nil {
		t.Fatalf("NewCommandPolicy: %v", err)
	}
	rule, checkErr := policy.CheckCommand(`echo a\;b`)
	if checkErr == nil {
		t.Fatal(`"echo a\;b" must stay denied on pwsh: the backslash does not escape the separator`)
	}
	if rule != "policy:metachars" {
		t.Fatalf("rule = %q, want policy:metachars", rule)
	}
}

func TestCommandPolicyAllowsEscapedSeparatorOnBash(t *testing.T) {
	policy, err := NewCommandPolicy(ShellConfig{
		Program:            "bash",
		AllowedCommands:    []string{"*"},
		DenyShellMetachars: true,
	}, 0)
	if err != nil {
		t.Fatalf("NewCommandPolicy: %v", err)
	}
	if rule, checkErr := policy.CheckCommand(`echo a\;b`); checkErr != nil {
		t.Fatalf(`"echo a\;b" is a single command in bash, got %v (rule %q)`, checkErr, rule)
	}
}

func TestCommandPolicyMetacharHintIsActionable(t *testing.T) {
	policy, err := NewCommandPolicy(ShellConfig{
		AllowedCommands:    []string{"*"},
		DenyShellMetachars: true,
	}, 0)
	if err != nil {
		t.Fatalf("NewCommandPolicy: %v", err)
	}
	_, checkErr := policy.CheckCommand("a; b")
	if checkErr == nil {
		t.Fatal("expected the metacharacter gate to deny this command")
	}
	message := checkErr.Error()
	// A remote caller cannot edit the target configuration, so the hint must
	// not send it to a setting it cannot change.
	if strings.Contains(message, "denyShellMetachars") {
		t.Fatalf("hint tells the caller to change target config it cannot edit: %s", message)
	}
	if !strings.Contains(message, "separate shell calls") {
		t.Fatalf("hint does not offer an executable alternative: %s", message)
	}
	// Both offending constructs should be surfaced together.
	if !strings.Contains(message, "a command separator (;)") {
		t.Fatalf("hint dropped the separator finding: %s", message)
	}
}

func TestDefaultPolicyAllowsPipelines(t *testing.T) {
	// The restriction is opt-in: a target with no explicit setting must be able
	// to compose commands, otherwise "find . -size +100M | head" needs several
	// round trips to list, page and trim.
	if DefaultConfig().Shell.DenyShellMetachars {
		t.Fatal("DefaultConfig().Shell.DenyShellMetachars = true, want the restriction off by default")
	}
	policy, err := NewCommandPolicy(ShellConfig{
		AllowedCommands: []string{"*"},
		DenyShellMetachars: false,
	}, 0)
	if err != nil {
		t.Fatalf("NewCommandPolicy: %v", err)
	}
	for _, command := range []string{
		`find . -size +100M | head`,
		`grep -c TODO *.go | sort -k2`,
		`git status; git diff`,
	} {
		if rule, checkErr := policy.CheckCommand(command); checkErr != nil {
			t.Fatalf("CheckCommand(%q) = %v (rule %q), want the default policy to allow composition", command, checkErr, rule)
		}
	}
}

func TestDenyShellMetacharsStillBlocksWhenExplicitlyEnabled(t *testing.T) {
	policy, err := NewCommandPolicy(ShellConfig{
		AllowedCommands:    []string{"*"},
		DenyShellMetachars: true,
	}, 0)
	if err != nil {
		t.Fatalf("NewCommandPolicy: %v", err)
	}
	if _, checkErr := policy.CheckCommand(`find . -size +100M | head`); checkErr == nil {
		t.Fatal("opt-in restriction must still refuse pipes when denyShellMetachars is true")
	}
}

func TestIsUnknownOutcomeStatus(t *testing.T) {
	for _, status := range []string{"orphaned", "ORPHANED", " unknown "} {
		if !isUnknownOutcomeStatus(status) {
			t.Fatalf("isUnknownOutcomeStatus(%q) = false, want true", status)
		}
	}
	for _, status := range []string{"completed", "failed", "cancelled", ""} {
		if isUnknownOutcomeStatus(status) {
			t.Fatalf("isUnknownOutcomeStatus(%q) = true, want false", status)
		}
	}
}
