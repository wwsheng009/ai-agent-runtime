package searchcore

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// grepIgnoreRule is one parsed ignore-file line, scoped to the directory its
// file lived in. The rule set mirrors the local tool's loader line for line:
// comments, \# and \! escapes, `!` negation, and the pattern itself kept verbatim
// (including a trailing `/`, which then matches through the path branch).
type grepIgnoreRule struct {
	pattern string
	negate  bool
	scope   string // absolute directory the rule file lived in
	// caseInsensitive applies only to rules read from an explicit ignore_file
	// when the caller asked for it (rg --ignore-file-case-insensitive).
	// Conventionally discovered files keep case-sensitive semantics.
	caseInsensitive bool
}

// grepIgnorePolicy carries the resolved ignore switches. A nil policy means
// "no ignore handling at all" (no_ignore), which is also what the walker sees
// when the caller opts out.
type grepIgnorePolicy struct {
	skipParent        bool
	skipVCS           bool
	skipDot           bool
	skipGlobal        bool
	skipExplicitFiles bool
	explicitFiles     []string
	// discoveredCaseInsensitive makes the conventionally discovered ignore
	// files (.gitignore, .ignore, .git/info/exclude, ...) compare
	// case-insensitively.
	//
	// It deliberately does NOT cover explicit ignore_file entries: rg's own
	// --help says "Process ignore files (.gitignore, .ignore, etc.) case
	// insensitively", and probing rg 14 confirms an explicit --ignore-file keeps
	// case-sensitive matching even when the flag is set.
	discoveredCaseInsensitive bool
}

func newGrepIgnorePolicy(opts GrepOptions) *grepIgnorePolicy {
	if opts.NoIgnore {
		return nil
	}
	return &grepIgnorePolicy{
		skipParent:                opts.NoIgnoreParent,
		skipVCS:                   opts.NoIgnoreVCS,
		skipDot:                   opts.NoIgnoreDot,
		skipGlobal:                opts.NoIgnoreGlobal,
		skipExplicitFiles:         opts.NoIgnoreFiles,
		explicitFiles:             opts.IgnoreFiles,
		discoveredCaseInsensitive: opts.IgnoreFileCaseInsensitive,
	}
}

// ValidateGrepIgnoreFiles checks the explicit ignore_file(s) before a search
// starts. A caller that names a file expects its rules to apply; silently
// dropping an unreadable one would change the result set without saying so.
// Missing ignore files discovered by convention (.gitignore, .ignore, globals)
// are not checked here - they are optional by nature, which is also why rg
// stays quiet about them.
//
// NoMessages mirrors rg --no-messages: skip an unreadable explicit file instead
// of failing the whole call.
func ValidateGrepIgnoreFiles(opts GrepOptions) error {
	if opts.NoIgnore || opts.NoIgnoreFiles {
		return nil
	}
	for _, file := range opts.IgnoreFiles {
		info, err := os.Stat(file)
		if err == nil && info.IsDir() {
			err = fmt.Errorf("is a directory")
		}
		if err != nil {
			if opts.NoMessages {
				continue
			}
			return fmt.Errorf("读取 ignore_file 失败 %s: %w", file, err)
		}
		if _, readErr := os.ReadFile(file); readErr != nil {
			if opts.NoMessages {
				continue
			}
			return fmt.Errorf("读取 ignore_file 失败 %s: %w", file, readErr)
		}
	}
	return nil
}

// grepIgnoreRules is the cache + assembler the walk keeps for one search root.
// Directory rule files are read at most once per directory.
type grepIgnoreRules struct {
	policy *grepIgnorePolicy
	root   string
	base   []grepIgnoreRule
	cache  map[string][]grepIgnoreRule
}

func newGrepIgnoreRules(policy *grepIgnorePolicy, root string) *grepIgnoreRules {
	if policy == nil {
		return nil
	}
	// A single-file root still consults its directory's ignore files, exactly
	// like a walk that started in that directory would.
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		root = filepath.Dir(filepath.Clean(root))
	}
	return &grepIgnoreRules{
		policy: policy,
		root:   root,
		base:   policy.baseRules(root),
		cache:  map[string][]grepIgnoreRule{},
	}
}

// rulesFor assembles the effective rules for one absolute path: the base rules
// (global + ancestors + explicit files) followed by each directory from the
// search root down to the path's parent, in walk order, so later rules win.
func (r *grepIgnoreRules) rulesFor(absPath string) []grepIgnoreRule {
	if r == nil {
		return nil
	}
	dir := absPath
	if info, err := os.Stat(absPath); err != nil || !info.IsDir() {
		dir = filepath.Dir(absPath)
	}
	chain := []string{}
	for current := filepath.Clean(dir); ; current = filepath.Dir(current) {
		if current == r.root {
			chain = append(chain, current)
			break
		}
		if !strings.HasPrefix(current, r.root+string(filepath.Separator)) {
			// The path sits outside the search root: only base rules apply.
			chain = nil
			break
		}
		chain = append(chain, current)
		if parent := filepath.Dir(current); parent == current {
			break
		}
	}
	// Reverse into root-first order.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	rules := make([]grepIgnoreRule, 0, len(r.base))
	rules = append(rules, r.base...)
	for _, dir := range chain {
		rules = append(rules, r.dirRules(dir)...)
	}
	return rules
}

func (r *grepIgnoreRules) dirRules(dir string) []grepIgnoreRule {
	if cached, ok := r.cache[dir]; ok {
		return cached
	}
	rules := r.policy.dirRules(dir)
	r.cache[dir] = rules
	return rules
}

// baseRules loads the sources that apply to the whole search: the user's global
// ignore files and every ancestor directory's ignore files, outermost first, so
// deeper rules can negate earlier ones. Explicit ignore_file entries join the
// same list, scoped to their own directory.
func (p *grepIgnorePolicy) baseRules(root string) []grepIgnoreRule {
	if p == nil {
		return nil
	}
	rules := make([]grepIgnoreRule, 0, 16)
	if !p.skipGlobal {
		rules = append(rules, globalIgnoreRulesWithCase(p.discoveredCaseInsensitive)...)
	}
	if !p.skipParent {
		ancestors := []string{}
		for current := filepath.Dir(filepath.Clean(root)); ; current = filepath.Dir(current) {
			ancestors = append(ancestors, current)
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
		}
		// Root-first so the closest ancestor has the last word.
		for i, j := 0, len(ancestors)-1; i < j; i, j = i+1, j-1 {
			ancestors[i], ancestors[j] = ancestors[j], ancestors[i]
		}
		for _, ancestor := range ancestors {
			rules = append(rules, p.dirRules(ancestor)...)
		}
	}
	if !p.skipExplicitFiles {
		for _, file := range p.explicitFiles {
			// Explicit files stay case-sensitive even under the flag: rg does
			// the same (verified), and the flag's own help text scopes it to
			// the discovered file family.
			rules = append(rules, loadGrepIgnoreFile(file, filepath.Dir(file))...)
		}
	}
	return rules
}

func (p *grepIgnorePolicy) dirRules(dir string) []grepIgnoreRule {
	if p == nil {
		return nil
	}
	rules := make([]grepIgnoreRule, 0, 8)
	if !p.skipDot {
		for _, name := range []string{".ignore", ".rgignore"} {
			rules = append(rules, loadGrepIgnoreFileWithCase(
				filepath.Join(dir, name), dir, p.discoveredCaseInsensitive)...)
		}
	}
	if !p.skipVCS {
		for _, rel := range []string{".gitignore", filepath.Join(".git", "info", "exclude")} {
			rules = append(rules, loadGrepIgnoreFileWithCase(
				filepath.Join(dir, rel), dir, p.discoveredCaseInsensitive)...)
		}
	}
	return rules
}

// globalIgnoreRules mirrors the local tool's two global candidates.
func globalIgnoreRules() []grepIgnoreRule {
	return globalIgnoreRulesWithCase(false)
}

func globalIgnoreRulesWithCase(caseInsensitive bool) []grepIgnoreRule {
	// HOME/USERPROFILE win over os.UserHomeDir: tests (and hosts) pin the
	// global ignore location through the environment, exactly like the local
	// tool's os.UserHomeDir respects them.
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		home = strings.TrimSpace(os.Getenv("USERPROFILE"))
	}
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return nil
		}
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return nil
	}
	rules := make([]grepIgnoreRule, 0, 8)
	for _, candidate := range []string{
		filepath.Join(home, ".config", "git", "ignore"),
		filepath.Join(home, ".gitignore_global"),
	} {
		rules = append(rules, loadGrepIgnoreFileWithCase(candidate, filepath.Dir(candidate), caseInsensitive)...)
	}
	return rules
}

// loadGrepIgnoreFile parses one ignore file the way the local loader does.
// Missing files are silent: ignore files are optional by nature.
func loadGrepIgnoreFile(filePath, scope string) []grepIgnoreRule {
	return loadGrepIgnoreFileWithCase(filePath, scope, false)
}

// loadGrepIgnoreFileWithCase is the same loader with the per-file case mode
// recorded on each rule, so the comparison rule travels with the rule instead of
// being a global switch that would leak into discovered files.
func loadGrepIgnoreFileWithCase(filePath, scope string, caseInsensitive bool) []grepIgnoreRule {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	rules := make([]grepIgnoreRule, 0, len(lines))
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
			line = strings.TrimPrefix(line, `\`)
		} else if strings.HasPrefix(line, "#") {
			continue
		}
		negate := false
		if strings.HasPrefix(line, "!") {
			negate = true
			line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
		}
		if line == "" {
			continue
		}
		rules = append(rules, grepIgnoreRule{
			pattern:         line,
			negate:          negate,
			scope:           scope,
			caseInsensitive: caseInsensitive,
		})
	}
	return rules
}

// ignoredByGrepRules applies the rules in order: the last matching rule wins,
// and a matching negated rule un-ignores the path. Matching follows the local
// tool: a pattern without `/` matches the candidate's base name, anything else
// matches the whole scope-relative path.
func ignoredByGrepRules(rules []grepIgnoreRule, absPath string) bool {
	ignored := false
	for _, rule := range rules {
		pattern := strings.TrimSpace(rule.pattern)
		if pattern == "" {
			continue
		}
		candidate := ""
		if rule.scope == "" {
			candidate = filepath.ToSlash(absPath)
		} else {
			rel, err := filepath.Rel(rule.scope, absPath)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				// The rule file lives outside the search tree (the global
				// candidates do): gitignore semantics still apply, matched
				// against the base name - the local tool's behaviour.
				candidate = filepath.Base(absPath)
			} else {
				candidate = filepath.ToSlash(rel)
			}
		}
		if candidate == "" || candidate == "." {
			continue
		}
		matched := false
		if strings.Contains(pattern, "/") || strings.Contains(pattern, "**") {
			ok, err := MatchGlobPath(pattern, candidate, rule.caseInsensitive)
			matched = err == nil && ok
		} else {
			base := path.Base(candidate)
			if rule.caseInsensitive {
				// path.Match has no case folding, so compare lowered copies.
				// The pattern itself is left untouched for the slash branch,
				// which goes through MatchGlobPath and folds internally.
				ok, err := path.Match(strings.ToLower(pattern), strings.ToLower(base))
				matched = err == nil && ok
			} else {
				ok, err := path.Match(pattern, base)
				matched = err == nil && ok
			}
		}
		if matched {
			ignored = !rule.negate
		}
	}
	return ignored
}
