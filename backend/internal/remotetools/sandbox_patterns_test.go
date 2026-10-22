package remotetools

import (
	"strings"
	"testing"
)

func newPatternSandbox(t *testing.T, allowed, denied []string) *Sandbox {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Profile = ProfileOps
	cfg.Shell.AllowedCommandPatterns = allowed
	cfg.Shell.DeniedCommandPatterns = denied
	cfg.Shell.DenyShellMetachars = true
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	ws, err := NewWorkspace(cfg.Workspace, nil)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	sandbox, err := NewSandbox(cfg, ws)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	return sandbox
}

func TestCheckCommandPatterns(t *testing.T) {
	cases := []struct {
		name    string
		allowed []string
		denied  []string
		command string
		wantErr bool
		rule    string
	}{
		{
			name:    "script family",
			allowed: []string{"deploy238_*.sh"},
			command: "deploy238_a.sh",
			rule:    "allowedCommandPatterns:deploy238_*.sh",
		},
		{
			name:    "script path is reduced to basename",
			allowed: []string{"deploy238_*.sh"},
			command: "./deploy238_a.sh",
			rule:    "allowedCommandPatterns:deploy238_*.sh",
		},
		{
			name:    "full match rejects extra args",
			allowed: []string{"deploy238_*.sh"},
			command: "deploy238_a.sh --force",
			wantErr: true,
			rule:    "policy:not-allowed",
		},
		{
			name:    "trailing wildcard covers args",
			allowed: []string{"deploy238_*.sh *"},
			command: "deploy238_a.sh --force",
			rule:    "allowedCommandPatterns:deploy238_*.sh *",
		},
		{
			name:    "subcommand pattern match",
			allowed: []string{"systemctl restart itsm-*"},
			command: "systemctl restart itsm-backend",
			rule:    "allowedCommandPatterns:systemctl restart itsm-*",
		},
		{
			name:    "subcommand pattern mismatch",
			allowed: []string{"systemctl restart itsm-*"},
			command: "systemctl stop itsm-backend",
			wantErr: true,
			rule:    "policy:not-allowed",
		},
		{
			name:    "deny pattern wins",
			allowed: []string{"*"},
			denied:  []string{"sh -c *"},
			command: `sh -c "echo hi"`,
			wantErr: true,
			rule:    "deniedCommandPatterns:sh -c *",
		},
		{
			name:    "deny pattern wins over narrower allow",
			allowed: []string{"echo *"},
			denied:  []string{"echo secret*"},
			command: "echo secret-value",
			wantErr: true,
			rule:    "deniedCommandPatterns:echo secret*",
		},
		{
			name:    "metachars still blocked after pattern allow",
			allowed: []string{"*"},
			command: "echo hi; whoami",
			wantErr: true,
			rule:    "policy:metachars",
		},
		{
			name:    "destructive classifier still blocked after pattern allow",
			allowed: []string{"*"},
			command: "rm -rf /",
			wantErr: true,
			rule:    "policy:destructive",
		},
		{
			name:    "fail closed without any allow rule",
			command: "echo hi",
			wantErr: true,
			rule:    "policy:fail-closed",
		},
		{
			name:    "case insensitive",
			allowed: []string{"ECHO *"},
			command: "echo HI",
			rule:    "allowedCommandPatterns:echo *",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox := newPatternSandbox(t, tc.allowed, tc.denied)
			rule, err := sandbox.CheckCommand(tc.command)
			if tc.wantErr && err == nil {
				t.Fatalf("expected denial, got rule %q", rule)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected success, got %v (rule %q)", err, rule)
			}
			if rule != tc.rule {
				t.Fatalf("rule = %q, want %q", rule, tc.rule)
			}
		})
	}
}

func TestNormalizeValidatesNewPolicyFields(t *testing.T) {
	base := func() *Config {
		cfg := DefaultConfig()
		cfg.Workspace = t.TempDir()
		return cfg
	}

	cfg := base()
	cfg.Shell.AllowedCommandPatterns = []string{"  Deploy238_*.SH  ", "deploy238_*.sh"}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(cfg.Shell.AllowedCommandPatterns) != 1 || cfg.Shell.AllowedCommandPatterns[0] != "deploy238_*.sh" {
		t.Fatalf("patterns not normalized: %v", cfg.Shell.AllowedCommandPatterns)
	}

	cfg = base()
	cfg.Shell.DeniedCommandPatterns = []string{""}
	if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), "empty pattern") {
		t.Fatalf("expected empty-pattern error, got %v", err)
	}

	cfg = base()
	cfg.Audit.CommandPreviewBytes = -1
	if err := cfg.Normalize(); err == nil {
		t.Fatal("expected negative commandPreviewBytes to be rejected")
	}

	cfg = base()
	cfg.Audit.CommandPreviewBytes = 10000
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if cfg.Audit.CommandPreviewBytes != 4096 {
		t.Fatalf("preview cap = %d, want 4096", cfg.Audit.CommandPreviewBytes)
	}
}
