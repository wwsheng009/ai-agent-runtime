package main

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

func TestAllowAllFlagAddsExplicitStarRule(t *testing.T) {
	flags := &serveFlags{}
	fs := newFlagSet("serve", flags)
	if err := fs.Parse([]string{"--allow-all"}); err != nil {
		t.Fatalf("parse --allow-all: %v", err)
	}
	cfg, err := loadConfig(flags)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	found := false
	for _, entry := range cfg.Shell.AllowedCommands {
		if strings.TrimSpace(entry) == "*" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("--allow-all did not add \"*\": %v", cfg.Shell.AllowedCommands)
	}
}

func TestPolicyLintsDetectCommonAllowlistMistakes(t *testing.T) {
	cfg := remotetools.DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Profile = remotetools.ProfileAll
	cfg.Shell.AllowedCommands = []string{"deploy*.sh"}
	cfg.Shell.AllowedCommandPatterns = []string{"deploy*.sh *"}
	lints := strings.Join(policyLints(cfg), "\n")
	for _, want := range []string{
		"allowedCommands entry \"deploy*.sh\" contains wildcard characters",
		"wildcard in program position",
	} {
		if !strings.Contains(lints, want) {
			t.Fatalf("policy lint lacks %q:\n%s", want, lints)
		}
	}

	cfg.Shell.AllowedCommands = []string{"*"}
	cfg.Shell.AllowedCommandPatterns = nil
	cfg.Shell.DenyShellMetachars = false
	lints = strings.Join(policyLints(cfg), "\n")
	if !strings.Contains(lints, "allow-all and denyShellMetachars=false") {
		t.Fatalf("policy lint lacks the all-allow risk warning:\n%s", lints)
	}
}

func TestEffectiveRulesUseExplicitKinds(t *testing.T) {
	rules := effectiveAllowRules(remotetools.ShellConfig{
		AllowedCommands:        []string{"git", "*"},
		AllowedCommandPatterns: []string{"deploy*.sh *"},
		DeniedCommands:         []string{"rm"},
		DeniedCommandPatterns:  []string{"shutdown *"},
	})
	want := []string{"prefix:git", "all", "glob:deploy*.sh *"}
	if strings.Join(rules, "|") != strings.Join(want, "|") {
		t.Fatalf("effective allow rules = %v, want %v", rules, want)
	}
	deny := effectiveDenyRules(remotetools.ShellConfig{
		DeniedCommands:        []string{"rm"},
		DeniedCommandPatterns: []string{"shutdown *"},
	})
	wantDeny := []string{"prefix:rm", "glob:shutdown *"}
	if strings.Join(deny, "|") != strings.Join(wantDeny, "|") {
		t.Fatalf("effective deny rules = %v, want %v", deny, wantDeny)
	}
}

func TestRootAndServeHelpDistinguishCommandPolicy(t *testing.T) {
	var root strings.Builder
	printUsage(&root)
	for _, want := range []string{
		"serve -h",
		"init -h",
		"policy",
		"--allow-all",
		"--deny-command",
		"--denied-command-patterns",
		"--deny-shell-metachars",
		"shell.deniedCommands",
	} {
		if !strings.Contains(root.String(), want) {
			t.Fatalf("root help lacks %q:\n%s", want, root.String())
		}
	}

	var serve strings.Builder
	printServeUsage(&serve)
	for _, want := range []string{
		"--allow-command",
		"--deny-command",
		"--allow-command-pattern",
		"--deny-command-pattern",
		"--denied-command-patterns",
		"--deny-shell-metachars",
		"shell.allowedCommands",
		"shell.deniedCommands",
	} {
		if !strings.Contains(serve.String(), want) {
			t.Fatalf("serve help lacks %q:\n%s", want, serve.String())
		}
	}
}

func TestInitHelpExitsZeroAndNamesInit(t *testing.T) {
	if code := runInit([]string{"-h"}); code != 0 {
		t.Fatalf("init -h exit code = %d, want 0", code)
	}
}
