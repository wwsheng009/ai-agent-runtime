package ui

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2/styles"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

var (
	syntaxThemeMu      sync.RWMutex
	currentSyntaxTheme = "auto"
)

// CuratedSyntaxThemeNames is the searchable short list shown in /theme.
// Full Chroma catalog remains available via SetSyntaxTheme for power users.
func CuratedSyntaxThemeNames() []string {
	names := []string{
		"auto",
		"monokai",
		"dracula",
		"github",
		"github-dark",
		"vim",
		"friendly",
		"solarized-dark",
		"solarized-light",
		"nord",
		"native",
		"bw",
	}
	out := make([]string, 0, len(names))
	seen := map[string]struct{}{}
	for _, n := range names {
		if n != "auto" && !syntaxThemeExists(n) {
			continue
		}
		key := strings.ToLower(n)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// CurrentSyntaxThemeName returns the active syntax theme for code/diff.
func CurrentSyntaxThemeName() string {
	syntaxThemeMu.RLock()
	defer syntaxThemeMu.RUnlock()
	if currentSyntaxTheme == "" {
		return "auto"
	}
	return currentSyntaxTheme
}

// CurrentResolvedSyntaxThemeName returns the concrete Chroma style selected
// by the syntax preference and the effective light/dark terminal mode.
func CurrentResolvedSyntaxThemeName() string {
	name := CurrentSyntaxThemeName()
	if strings.EqualFold(name, "auto") {
		return resolveAutoSyntaxTheme()
	}
	return name
}

// SetSyntaxTheme sets the global syntax highlighter theme.
// Unknown names return an error without mutating state.
func SetSyntaxTheme(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "auto") {
		syntaxThemeMu.Lock()
		currentSyntaxTheme = "auto"
		syntaxThemeMu.Unlock()
		syntax.SetGlobalDefaultTheme(resolveAutoSyntaxTheme())
		return nil
	}
	if !syntaxThemeExists(name) {
		for _, n := range styles.Names() {
			if strings.EqualFold(n, name) {
				name = n
				goto ok
			}
		}
		return fmt.Errorf("未知语法主题: %s", name)
	}
ok:
	syntaxThemeMu.Lock()
	currentSyntaxTheme = name
	syntaxThemeMu.Unlock()
	syntax.SetGlobalDefaultTheme(name)
	return nil
}

// NormalizeSyntaxThemeName returns canonical name or "" if unknown.
func NormalizeSyntaxThemeName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "auto") {
		return "auto"
	}
	if syntaxThemeExists(raw) {
		return raw
	}
	for _, n := range styles.Names() {
		if strings.EqualFold(n, raw) {
			return n
		}
	}
	return ""
}

func resolveAutoSyntaxTheme() string {
	if CurrentThemeResolvedModeName() == ThemeModeLight {
		return "github"
	}
	return "monokai"
}

// refreshAutoSyntaxTheme updates formatter paths that use the process-wide
// syntax default after the terminal mode changes.
func refreshAutoSyntaxTheme() {
	if strings.EqualFold(CurrentSyntaxThemeName(), "auto") {
		syntax.SetGlobalDefaultTheme(resolveAutoSyntaxTheme())
	}
}

func syntaxThemeExists(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	// Prefer registry names: styles.Get may return Fallback for unknowns.
	for _, n := range styles.Names() {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}
