package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

type policyTestReport struct {
	Command            string   `json:"command"`
	Decision           string   `json:"decision"`
	Rule               string   `json:"rule,omitempty"`
	Code               string   `json:"code,omitempty"`
	Message            string   `json:"message,omitempty"`
	NextAction         string   `json:"next_action,omitempty"`
	Retriable          bool     `json:"retriable,omitempty"`
	ShellExposed       bool     `json:"shell_exposed"`
	AllowAll           bool     `json:"allow_all"`
	DenyShellMetachars bool     `json:"deny_shell_metachars"`
	AllowRules         []string `json:"allow_rules,omitempty"`
	DenyRules          []string `json:"deny_rules,omitempty"`
}

type policyLintReport struct {
	OK    bool     `json:"ok"`
	Lints []string `json:"lints"`
}

func runPolicy(args []string) int {
	if len(args) == 0 {
		printPolicyUsage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "test":
		return runPolicyTest(args[1:])
	case "lint":
		return runPolicyLint(args[1:])
	case "help", "-h", "--help":
		printPolicyUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "aicli-remote-tools policy: unknown subcommand %q\n\n", args[0])
		printPolicyUsage(os.Stderr)
		return 2
	}
}

func runPolicyTest(args []string) int {
	flags := &serveFlags{}
	fs := newFlagSet("policy test", flags)
	fs.Usage = func() { printPolicyTestUsage(os.Stdout) }
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "aicli-remote-tools policy test: missing command")
		printPolicyTestUsage(os.Stderr)
		return 2
	}
	command := strings.Join(rest, " ")
	cfg, err := loadConfig(flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools policy test: %v\n", err)
		return 1
	}
	if err := cfg.Normalize(); err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools policy test: %v\n", err)
		return 1
	}
	policy, err := remotetools.NewCommandPolicy(cfg.Shell, cfg.Limits.CommandTimeout.Duration)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools policy test: %v\n", err)
		return 1
	}
	rule, checkErr := policy.CheckCommand(command)
	report := policyTestReport{
		Command:            command,
		Decision:           "allow",
		Rule:               rule,
		ShellExposed:       cfg.ShellExposed(),
		AllowAll:           shellAllowsAll(cfg.Shell),
		DenyShellMetachars: cfg.Shell.DenyShellMetachars,
		AllowRules:         effectiveAllowRules(cfg.Shell),
		DenyRules:          effectiveDenyRules(cfg.Shell),
	}
	if checkErr != nil {
		report.Decision = "deny"
		var toolErr *remotetools.ToolError
		if errors.As(checkErr, &toolErr) {
			report.Code = toolErr.Code
			report.Message = toolErr.Message
			report.NextAction = toolErr.NextAction
			report.Retriable = toolErr.Retriable
		} else {
			report.Message = checkErr.Error()
		}
	}
	if flags.jsonOut {
		encoded, encodeErr := json.MarshalIndent(report, "", "  ")
		if encodeErr != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools policy test: %v\n", encodeErr)
			return 1
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return 0
	}
	printPolicyTestReport(report)
	return 0
}

func printPolicyTestReport(report policyTestReport) {
	fmt.Fprintln(os.Stdout, "aicli-remote-tools policy test")
	fmt.Fprintf(os.Stdout, "  command          : %s\n", report.Command)
	fmt.Fprintf(os.Stdout, "  decision         : %s\n", report.Decision)
	if report.Rule != "" {
		fmt.Fprintf(os.Stdout, "  matched rule     : %s\n", report.Rule)
	}
	if report.Code != "" {
		fmt.Fprintf(os.Stdout, "  code             : %s\n", report.Code)
	}
	if report.Message != "" {
		fmt.Fprintf(os.Stdout, "  reason           : %s\n", report.Message)
	}
	if report.NextAction != "" {
		fmt.Fprintf(os.Stdout, "  next             : %s\n", report.NextAction)
	}
	fmt.Fprintf(os.Stdout, "  shell exposed    : %v\n", report.ShellExposed)
	fmt.Fprintf(os.Stdout, "  allow all        : %v\n", report.AllowAll)
	metachars := "allow"
	if report.DenyShellMetachars {
		metachars = "deny"
	}
	fmt.Fprintf(os.Stdout, "  metachar gate    : %s\n", metachars)
	fmt.Fprintf(os.Stdout, "  allow rules      : %s\n", orDash(strings.Join(report.AllowRules, ", ")))
	fmt.Fprintf(os.Stdout, "  deny rules       : %s\n", orDash(strings.Join(report.DenyRules, ", ")))
}

func runPolicyLint(args []string) int {
	flags := &serveFlags{}
	fs := newFlagSet("policy lint", flags)
	fs.Usage = func() { printPolicyLintUsage(os.Stdout) }
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	cfg, err := loadConfig(flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools policy lint: %v\n", err)
		return 1
	}
	if err := cfg.Normalize(); err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools policy lint: %v\n", err)
		return 1
	}
	lints := policyLints(cfg)
	report := policyLintReport{OK: len(lints) == 0, Lints: lints}
	if flags.jsonOut {
		encoded, encodeErr := json.MarshalIndent(report, "", "  ")
		if encodeErr != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools policy lint: %v\n", encodeErr)
			return 1
		}
		fmt.Fprintln(os.Stdout, string(encoded))
	} else if len(lints) == 0 {
		fmt.Fprintln(os.Stdout, "aicli-remote-tools policy lint: ok")
	} else {
		fmt.Fprintln(os.Stdout, "aicli-remote-tools policy lint:")
		for _, lint := range lints {
			fmt.Fprintf(os.Stdout, "  lint: %s\n", lint)
		}
	}
	if len(lints) > 0 {
		return 1
	}
	return 0
}

func shellAllowsAll(shell remotetools.ShellConfig) bool {
	for _, entry := range shell.AllowedCommands {
		if strings.TrimSpace(entry) == "*" {
			return true
		}
	}
	return false
}

func effectiveAllowRules(shell remotetools.ShellConfig) []string {
	rules := make([]string, 0, len(shell.AllowedCommands)+len(shell.AllowedCommandPatterns))
	for _, entry := range shell.AllowedCommands {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if entry == "*" {
			rules = append(rules, "all")
		} else {
			rules = append(rules, "prefix:"+entry)
		}
	}
	for _, pattern := range shell.AllowedCommandPatterns {
		pattern = strings.TrimSpace(pattern)
		if pattern != "" {
			rules = append(rules, "glob:"+pattern)
		}
	}
	return rules
}

func effectiveDenyRules(shell remotetools.ShellConfig) []string {
	rules := make([]string, 0, len(shell.DeniedCommands)+len(shell.DeniedCommandPatterns))
	for _, entry := range shell.DeniedCommands {
		entry = strings.TrimSpace(entry)
		if entry != "" {
			rules = append(rules, "prefix:"+entry)
		}
	}
	for _, pattern := range shell.DeniedCommandPatterns {
		pattern = strings.TrimSpace(pattern)
		if pattern != "" {
			rules = append(rules, "glob:"+pattern)
		}
	}
	return rules
}

// policyLints keeps the low-cost migration path honest: it reports only
// config shapes that are easy to misread or clearly high-risk, and never
// changes the policy itself.
func policyLints(cfg *remotetools.Config) []string {
	if cfg == nil {
		return nil
	}
	shell := cfg.Shell
	allowAll := shellAllowsAll(shell)
	allowRuleCount := len(shell.AllowedCommands) + len(shell.AllowedCommandPatterns)
	lints := make([]string, 0, 6)
	seen := map[string]bool{}
	add := func(message string) {
		if message == "" || seen[message] {
			return
		}
		seen[message] = true
		lints = append(lints, message)
	}

	if cfg.ShellExposed() && allowRuleCount == 0 {
		add("shell is visible but no allow rule is configured; every shell call will be denied (fail-closed)")
	}
	if !cfg.ShellExposed() && allowRuleCount > 0 {
		add("allow rules are configured but shell is not exposed by profile/readOnly/tools")
	}
	if allowAll && allowRuleCount > 1 {
		add("allowedCommands contains \"*\" together with other allow rules; the other rules are redundant")
	}
	for _, entry := range shell.AllowedCommands {
		entry = strings.TrimSpace(entry)
		if entry == "" || entry == "*" {
			continue
		}
		if strings.ContainsAny(entry, "*?") {
			add(fmt.Sprintf("allowedCommands entry %q contains wildcard characters; allowedCommands matches literal argv prefixes; use allowedCommandPatterns for globs", entry))
		}
	}
	for _, pattern := range shell.AllowedCommandPatterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if pattern == "*" {
			add("allowedCommandPatterns contains \"*\"; prefer allowedCommands: [\"*\"] or --allow-all for an explicit all-allow rule")
		} else if fields := strings.Fields(pattern); len(fields) > 0 && strings.ContainsAny(fields[0], "*?") {
			add(fmt.Sprintf("allowedCommandPatterns entry %q has a wildcard in program position; it can match a writable script; prefer a fixed wrapper command", pattern))
		}
		if !strings.ContainsAny(pattern, "*?") {
			add(fmt.Sprintf("allowedCommandPatterns entry %q has no wildcard; allowedCommands is simpler for an exact argv prefix", pattern))
		}
	}
	if allowAll && !shell.DenyShellMetachars {
		add("allow-all and denyShellMetachars=false together expose the full target shell; use only on a disposable or trusted target")
	}
	return lints
}
