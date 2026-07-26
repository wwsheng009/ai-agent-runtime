// Package foldertrust implements the VS-Code-style folder trust gate for
// repo-local code-exec configs (project plugins / hooks / MCP).
//
// Pure decide precedence (no I/O):
//  1. Feature flag OFF  → trusted (no gating; preserves prior behavior)
//  2. Store trusted (self/ancestor) → trusted
//  3. Key unrecordable (HOME / FS root / non-absolute) → trusted
//  4. No repo-local code-exec configs → trusted
//  5. Interactive → prompt
//  6. Headless → untrusted
package foldertrust

// Outcome is the pure trust decision for a set of inputs.
type Outcome string

const (
	// OutcomeTrusted allows project-scope repo-local code-exec configs.
	OutcomeTrusted Outcome = "trusted"
	// OutcomeUntrusted blocks project-scope repo-local code-exec configs.
	OutcomeUntrusted Outcome = "untrusted"
	// OutcomePrompt asks the user interactively (caller must prompt + grant/deny).
	OutcomePrompt Outcome = "prompt"
)

// DecideInputs feeds the pure Decide precedence function.
type DecideInputs struct {
	// StoreTrusted is true when the durable store trusts the workspace key
	// (self or ancestor cascade).
	StoreTrusted bool
	// RepoConfigsPresent is true when any trust-sensitive project config exists.
	RepoConfigsPresent bool
	// Interactive is true when a TTY prompt is available.
	Interactive bool
	// KeyRecordable is false for over-broad roots the store refuses to persist
	// (home / filesystem root / non-absolute).
	KeyRecordable bool
}

// Decide returns the pure trust outcome. No I/O.
func Decide(featureEnabled bool, in DecideInputs) Outcome {
	if !featureEnabled {
		return OutcomeTrusted
	}
	if in.StoreTrusted {
		return OutcomeTrusted
	}
	// Over-broad keys can't be durably gated — trust rather than re-prompt forever.
	if !in.KeyRecordable {
		return OutcomeTrusted
	}
	if !in.RepoConfigsPresent {
		return OutcomeTrusted
	}
	if in.Interactive {
		return OutcomePrompt
	}
	return OutcomeUntrusted
}

// ProjectScopeAllowed reports whether project-scope loaders may apply
// repo-local plugins/hooks/MCP contributions.
func ProjectScopeAllowed(outcome Outcome) bool {
	return outcome == OutcomeTrusted
}
