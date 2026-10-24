package searchcore

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// GrepPatternOptions carries the pattern-level switches shared by every grep
// engine. The pure-Go walker compiles them into one RE2 expression; the
// ripgrep runner translates them into flags. Both surfaces feed the same
// struct into the same resolution, so an argument cannot mean one thing on the
// local tool and another thing remotely.
type GrepPatternOptions struct {
	Literal       bool // treat every pattern as literal text
	IgnoreCase    bool // force case-insensitive matching
	CaseSensitive bool // force case-sensitive matching; wins over IgnoreCase/SmartCase
	SmartCase     bool // ignore case only when no pattern contains an uppercase rune
	Word          bool // match whole words only
	LineRegexp    bool // require the whole line to match
}

// EffectiveIgnoreCase resolves the three case switches to the single boolean
// each engine's native matcher receives. case_sensitive wins; smart_case only
// downgrades to case-insensitive when every pattern is free of uppercase
// runes. Resolving here (instead of delegating to rg --smart-case) is what
// keeps the two engines byte-identical on multi-pattern searches.
func EffectiveIgnoreCase(patterns []string, opts GrepPatternOptions) bool {
	if opts.CaseSensitive {
		return false
	}
	if opts.IgnoreCase {
		return true
	}
	if opts.SmartCase {
		return !hasUppercasePattern(patterns)
	}
	return false
}

func hasUppercasePattern(patterns []string) bool {
	for _, pattern := range patterns {
		for _, r := range pattern {
			if unicode.IsUpper(r) {
				return true
			}
		}
	}
	return false
}

// CompileGrepPattern builds the single RE2 expression shared by every engine
// that cannot delegate to ripgrep. It is the canonical definition of what
// literal, word, line_regexp and multiple patterns mean for grep: each switch
// is applied per pattern, patterns are OR-joined, and the case mode is applied
// once to the whole expression.
func CompileGrepPattern(patterns []string, opts GrepPatternOptions) (*regexp.Regexp, error) {
	if len(patterns) == 0 {
		return nil, fmt.Errorf("pattern 参数缺失或无效")
	}

	exprs := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		expr := pattern
		if opts.Literal {
			expr = regexp.QuoteMeta(pattern)
		}
		if opts.Word {
			expr = `\b` + expr + `\b`
		}
		if opts.LineRegexp {
			expr = `^(?:` + expr + `)$`
		}
		exprs = append(exprs, expr)
	}

	expr := exprs[0]
	if len(exprs) > 1 {
		expr = `(?:` + strings.Join(exprs, `)|(?:`) + `)`
	}
	if EffectiveIgnoreCase(patterns, opts) {
		expr = `(?i)` + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		msg := fmt.Sprintf("正则表达式无效: %v。若要搜索字面文本请设置 literal=true（或 fixed_strings=true）", err)
		if LooksLikePCREOnlyPatterns(patterns) {
			msg += "。检测到 lookaround/后行断言等 PCRE 语法（如 (?! / (?= / (?<= / (?<!），Go 内置 regexp 不支持；请设置 rg_args:[\"-P\"]（需本机 rg）"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return re, nil
}

// LooksLikePCREOnlyPatterns reports whether any pattern uses constructs Go's
// RE2 cannot compile (lookaround, backreferences); rg -P is the escape hatch.
func LooksLikePCREOnlyPatterns(patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(pattern, "(?!") ||
			strings.Contains(pattern, "(?=") ||
			strings.Contains(pattern, "(?<!") ||
			strings.Contains(pattern, "(?<=") {
			return true
		}
	}
	return false
}

// GrepPatternsFromArgs selects the search patterns from an argument map with
// the same precedence the local tool has always used: the single-value
// spellings (`pattern`, `regexp`) first, then the batch spelling (`patterns`).
// It returns the trimmed, non-empty list; callers report their own
// missing-pattern error when the result is empty.
func GrepPatternsFromArgs(args map[string]any) []string {
	patterns := make([]string, 0, 4)
	if len(args) == 0 {
		return patterns
	}
	for _, key := range []string{"pattern", "regexp"} {
		if value, ok := args[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				patterns = append(patterns, trimmed)
			}
		}
	}
	switch value := args["patterns"].(type) {
	case string:
		// The string spelling is one pattern, never a comma-separated list:
		// a comma is ordinary regex content ("a,b" means a-then-b). This is
		// the behaviour the local tool's resolvePatternListParam has always
		// had, and both surfaces must keep it.
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			patterns = append(patterns, trimmed)
		}
	case []string:
		for _, item := range value {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				patterns = append(patterns, trimmed)
			}
		}
	case []interface{}:
		for _, raw := range value {
			item, _ := raw.(string)
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				patterns = append(patterns, trimmed)
			}
		}
	}
	return dedupeGrepPatterns(patterns)
}

// dedupeGrepPatterns mirrors the local normalizePatternList: the first
// occurrence wins, so `-e foo -e foo` searches once.
func dedupeGrepPatterns(patterns []string) []string {
	if len(patterns) < 2 {
		return patterns
	}
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		seen := false
		for _, existing := range out {
			if existing == pattern {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, pattern)
		}
	}
	return out
}
