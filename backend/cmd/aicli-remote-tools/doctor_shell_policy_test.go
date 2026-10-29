package main

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

func TestDoctorShellPolicyReportsEffectiveGatesAndGuidance(t *testing.T) {
	cfg := remotetools.DefaultConfig()
	cfg.Workspace = t.TempDir()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	report := buildDoctorReport(cfg)
	if !report.ShellVisible {
		t.Fatalf("default profile should expose shell: %+v", report)
	}
	if !report.DenyShellMetachars {
		t.Fatal("default shell policy must deny metacharacters")
	}
	if report.CommandTimeout != "2m0s" {
		t.Fatalf("command timeout = %q, want 2m0s", report.CommandTimeout)
	}
	if report.ShellResolved == "" || report.ShellType == "" {
		t.Fatalf("doctor did not report the resolved shell: %+v", report)
	}
	if !containsString(report.ShellEnvAllowlist, "PATH") {
		t.Fatalf("doctor shell env = %v, want PATH", report.ShellEnvAllowlist)
	}
	warnings := strings.Join(report.Warnings, "\n")
	for _, want := range []string{
		"shell is visible but no allowlist is configured",
		"shell.allowedCommands",
		"shell.allowedCommandPatterns",
		"denyShellMetachars",
	} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("doctor warning lacks %q:\n%s", want, warnings)
		}
	}
	// The default local config now defaults artifacts.root to a workspace .aicli
	// directory, so the "truncated with no recovery" warning must not appear and
	// the store root must be reported as configured.
	if strings.Contains(warnings, "truncated with no recovery") {
		t.Fatalf("default config must not warn about truncation now that artifacts.root defaults:\n%s", warnings)
	}
	if report.ArtifactsRoot == "" {
		t.Fatalf("doctor must report the defaulted artifacts.root, got empty")
	}
	if !strings.Contains(report.ArtifactsRoot, "artifacts") {
		t.Fatalf("defaulted artifacts.root = %q, want a path ending in artifacts", report.ArtifactsRoot)
	}

	cfg.Shell.AllowedCommands = []string{"git"}
	report = buildDoctorReport(cfg)
	if strings.Contains(strings.Join(report.Warnings, "\n"), "shell is visible but no allowlist") {
		t.Fatalf("configured allowlist still produced the fail-closed warning: %+v", report.Warnings)
	}
	if len(report.AllowedCommands) != 1 || report.AllowedCommands[0] != "git" {
		t.Fatalf("allowed commands = %v, want [git]", report.AllowedCommands)
	}

	cfg.ReadOnly = true
	report = buildDoctorReport(cfg)
	if report.ShellVisible {
		t.Fatal("readOnly must hide shell from the effective surface")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
