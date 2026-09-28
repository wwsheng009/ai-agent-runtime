package policy

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStripNullDeviceRedirectionsPOSIX(t *testing.T) {
	stripped := []struct {
		name    string
		command string
		want    string
	}{
		{
			"stderr redirect inside a pipeline",
			`grep -rn "race" backend/Makefile Makefile 2>/dev/null | head -20`,
			`grep -rn "race" backend/Makefile Makefile  | head -20`,
		},
		{"stdout redirect", "git status >/dev/null", "git status "},
		{"fd 1 redirect", "git status 1>/dev/null", "git status "},
		{"spaced target", "cat app.log 2> /dev/null", "cat app.log "},
		{"multiple redirects", "git status 2>/dev/null >/dev/null", "git status  "},
		{"after separator", "git status;git diff 2>/dev/null", "git status;git diff "},
	}
	for _, tc := range stripped {
		t.Run(tc.name, func(t *testing.T) {
			got := stripNullDeviceRedirections(tc.command, "linux")
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, ">")
		})
	}

	kept := []struct {
		name    string
		command string
	}{
		{"windows spelling is not a POSIX device", "git status 2>NUL"},
		{"append operator", "git status 2>>/dev/null"},
		{"fd duplication", "git status 2>&1"},
		{"other target", "git status >/dev/nullx"},
		{"nested path target", "git status >/dev/null/other"},
		{"target must start a token", "echo hi1>/dev/null"},
		{"quoted spelling is an argument", `grep "2>/dev/null" file`},
		{"escaped operator is an argument", `git status \>/dev/null`},
		{"expansion after target", "git status 2>/dev/null$x"},
	}
	for _, tc := range kept {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.command, stripNullDeviceRedirections(tc.command, "linux"))
		})
	}
}

func TestStripNullDeviceRedirectionsWindows(t *testing.T) {
	stripped := []struct {
		name    string
		command string
		want    string
	}{
		{"stderr redirect", "dir 2>NUL", "dir "},
		{"case insensitive device name", "dir 2>nul", "dir "},
		{"spaced target", "dir 2> NUL", "dir "},
		{"stdout redirect", "dir >NUL", "dir "},
	}
	for _, tc := range stripped {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, stripNullDeviceRedirections(tc.command, "windows"))
		})
	}

	for _, command := range []string{
		"dir 2>/dev/null",
		"dir 2>>NUL",
		"dir 2>&1",
		"dir 2>NULx",
	} {
		t.Run("kept "+command, func(t *testing.T) {
			assert.Equal(t, command, stripNullDeviceRedirections(command, "windows"))
		})
	}
}

// TestAssessShellReadOnlyCommandAllowsNullDeviceRedirection locks the reported
// false positive (2026-09-28): `grep ... 2>/dev/null | head -20` must stay on
// the read-only fast path, while real redirect targets and the remaining
// dynamic syntax stay denied.
func TestAssessShellReadOnlyCommandAllowsNullDeviceRedirection(t *testing.T) {
	native := `grep -rn "race" backend/Makefile Makefile 2>/dev/null | head -20`
	foreign := `grep -rn "race" backend/Makefile Makefile 2>NUL | head -20`
	nativeNull := " 2>/dev/null"
	if runtime.GOOS == "windows" {
		native, foreign = foreign, native
		nativeNull = " 2>NUL"
	}
	assert.True(t, AssessShellReadOnlyCommand(native).Allowed,
		"native null-device redirection must stay on the read-only fast path")

	foreignAssessment := AssessShellReadOnlyCommand(foreign)
	assert.False(t, foreignAssessment.Allowed,
		"the other platform's null-device spelling is an ordinary path here")
	assert.Equal(t, ShellReadOnlyReasonDynamicSyntax, foreignAssessment.Reason)

	for _, command := range []string{
		"echo x > f",
		"git status 2>&1",
		"git diff $env:GIT_ARG",
		"git status `cat x`",
	} {
		assessment := AssessShellReadOnlyCommand(command)
		assert.Falsef(t, assessment.Allowed, "expected %q denied", command)
		assert.Equalf(t, ShellReadOnlyReasonDynamicSyntax, assessment.Reason, "stable reason for %q", command)
	}

	// The exemption must not widen the command allowlist: stripping the null
	// redirect leaves the actual command to be judged on its own.
	nonAllowlisted := AssessShellReadOnlyCommand("rm -rf build" + nativeNull)
	assert.False(t, nonAllowlisted.Allowed)
	assert.Equal(t, ShellReadOnlyReasonNotAllowed, nonAllowlisted.Reason)

	// A compound command with one more real redirect stays out of the fast path.
	compoundWithRealRedirect := AssessShellReadOnlyCommand("git status" + nativeNull + " && echo x > f")
	assert.False(t, compoundWithRealRedirect.Allowed)
	assert.Equal(t, ShellReadOnlyReasonDynamicSyntax, compoundWithRealRedirect.Reason)
}
