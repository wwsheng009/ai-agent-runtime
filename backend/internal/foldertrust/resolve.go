package foldertrust

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Resolution is the concrete trust result for a workspace after decide + optional prompt.
type Resolution struct {
	// Outcome is the pure decide result before prompt resolution.
	// After Resolve, Outcome is always Trusted or Untrusted (Prompt is resolved).
	Outcome Outcome
	// Trusted is ProjectScopeAllowed(Outcome).
	Trusted bool
	// FeatureEnabled mirrors FeatureEnabled() at resolve time.
	FeatureEnabled bool
	// WorkspaceKey is the durable store key used for the decision.
	WorkspaceKey string
	// ProjectRoot is the absolute cwd / project root that was evaluated.
	ProjectRoot string
	// ConfigKinds lists detected trust-sensitive config kinds (debug/banner).
	ConfigKinds []ConfigKind
	// StorePath is the durable store file (may be empty).
	StorePath string
	// Source explains how the decision was reached (feature_off, store, unrecordable,
	// no_configs, grant, prompt_yes, prompt_no, headless_deny, explicit_untrust).
	Source string
	// Prompted is true when an interactive prompt was shown.
	Prompted bool
}

// ResolveOptions configures Resolve.
type ResolveOptions struct {
	// CWD is the project working directory (defaults to os.Getwd).
	CWD string
	// TrustGrant forces a durable trust grant before decide (CLI --trust).
	TrustGrant bool
	// Interactive overrides TTY detection when non-nil.
	Interactive *bool
	// Store overrides the durable store (tests).
	Store *Store
	// FeatureEnabled overrides FeatureEnabled() when non-nil.
	FeatureEnabled *bool
	// Stdin/Stderr for the interactive prompt (defaults to os.Stdin/Stderr).
	Stdin  io.Reader
	Stderr io.Writer
	// SkipPrompt forces Prompt → Untrusted without reading stdin (tests / headless force).
	SkipPrompt bool
}

// Resolve gathers inputs, optionally grants trust, decides, and resolves Prompt.
func Resolve(opts ResolveOptions) Resolution {
	cwd := strings.TrimSpace(opts.CWD)
	if cwd == "" {
		if abs, err := os.Getwd(); err == nil {
			cwd = abs
		}
	}
	cwd = Canonicalize(cwd)
	key := WorkspaceKey(cwd)
	if key == "" {
		key = cwd
	}
	key = Canonicalize(key)

	feature := FeatureEnabled()
	if opts.FeatureEnabled != nil {
		feature = *opts.FeatureEnabled
	}

	store := opts.Store
	if store == nil {
		store = Load()
	}

	res := Resolution{
		FeatureEnabled: feature,
		WorkspaceKey:   key,
		ProjectRoot:    cwd,
		StorePath:      store.Path(),
		ConfigKinds:    RepoConfigKinds(cwd),
	}

	if opts.TrustGrant && feature {
		_ = store.SetTrusted(key)
		res.Source = "grant"
	}

	interactive := isInteractiveTerminal()
	if opts.Interactive != nil {
		interactive = *opts.Interactive
	}

	inputs := DecideInputs{
		StoreTrusted:       store.IsTrusted(key),
		RepoConfigsPresent: len(res.ConfigKinds) > 0,
		Interactive:        interactive && !opts.SkipPrompt,
		KeyRecordable:      !IsUnsafeTrustRoot(key),
	}
	outcome := Decide(feature, inputs)
	res.Outcome = outcome

	switch outcome {
	case OutcomeTrusted:
		if res.Source == "" {
			switch {
			case !feature:
				res.Source = "feature_off"
			case inputs.StoreTrusted:
				res.Source = "store"
			case !inputs.KeyRecordable:
				res.Source = "unrecordable"
			case !inputs.RepoConfigsPresent:
				res.Source = "no_configs"
			default:
				res.Source = "trusted"
			}
		}
		res.Trusted = true
		return res
	case OutcomeUntrusted:
		res.Source = "headless_deny"
		res.Trusted = false
		return res
	case OutcomePrompt:
		res.Prompted = true
		stdin := opts.Stdin
		if stdin == nil {
			stdin = os.Stdin
		}
		stderr := opts.Stderr
		if stderr == nil {
			stderr = os.Stderr
		}
		if opts.SkipPrompt {
			res.Outcome = OutcomeUntrusted
			res.Source = "prompt_skipped"
			res.Trusted = false
			return res
		}
		if PromptForTrust(key, res.ConfigKinds, stdin, stderr) {
			_ = store.SetTrusted(key)
			res.Outcome = OutcomeTrusted
			res.Source = "prompt_yes"
			res.Trusted = true
			return res
		}
		// Explicit decline: record untrusted so we do not re-prompt every launch
		// while still failing closed for project scope.
		_ = store.SetUntrusted(key)
		res.Outcome = OutcomeUntrusted
		res.Source = "prompt_no"
		res.Trusted = false
		return res
	default:
		res.Outcome = OutcomeUntrusted
		res.Source = "unknown"
		res.Trusted = false
		return res
	}
}

// GrantTrust persists trust for cwd's workspace key (CLI --trust / /trust).
// No-op when the feature is off (store left untouched, matching Grok inert grant).
func GrantTrust(cwd string) (string, error) {
	if !FeatureEnabled() {
		return WorkspaceKey(cwd), nil
	}
	key := Canonicalize(WorkspaceKey(cwd))
	store := Load()
	if err := store.SetTrusted(key); err != nil {
		return key, err
	}
	return key, nil
}

// PromptForTrust shows the MVP stderr y/N prompt. Defaults to NO on empty/EOF.
func PromptForTrust(key string, kinds []ConfigKind, stdin io.Reader, stderr io.Writer) bool {
	if stderr == nil {
		stderr = os.Stderr
	}
	if stdin == nil {
		stdin = os.Stdin
	}
	kindList := "plugins/hooks/MCP"
	if len(kinds) > 0 {
		parts := make([]string, 0, len(kinds))
		for _, k := range kinds {
			parts = append(parts, string(k))
		}
		kindList = strings.Join(parts, "/")
	}
	_, _ = fmt.Fprintln(stderr)
	_, _ = fmt.Fprintf(stderr, "This folder contains repo-local config (%s) that can run commands on your machine.\n", kindList)
	_, _ = fmt.Fprintf(stderr, "  Folder: %s\n", key)
	_, _ = fmt.Fprint(stderr, "Trust the authors of this folder and allow project plugins/hooks/MCP? [y/N] ")
	reader := bufio.NewReader(stdin)
	line, err := reader.ReadString('\n')
	if err != nil && len(strings.TrimSpace(line)) == 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// FormatSummary returns a short /debug line.
func FormatSummary(res Resolution) string {
	if !res.FeatureEnabled {
		return "feature_off (project scope allowed)"
	}
	status := "untrusted"
	if res.Trusted {
		status = "trusted"
	}
	kinds := "none"
	if len(res.ConfigKinds) > 0 {
		parts := make([]string, 0, len(res.ConfigKinds))
		for _, k := range res.ConfigKinds {
			parts = append(parts, string(k))
		}
		kinds = strings.Join(parts, ",")
	}
	return fmt.Sprintf("%s source=%s key=%s configs=%s", status, res.Source, res.WorkspaceKey, kinds)
}

func isInteractiveTerminal() bool {
	stdinStat, errIn := os.Stdin.Stat()
	stderrStat, errErr := os.Stderr.Stat()
	if errIn != nil || errErr != nil {
		return false
	}
	stdinTTY := (stdinStat.Mode() & os.ModeCharDevice) != 0
	stderrTTY := (stderrStat.Mode() & os.ModeCharDevice) != 0
	return stdinTTY && stderrTTY
}
