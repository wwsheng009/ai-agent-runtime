// Package pathrepair provides portable, I/O-bounded repairs for file names
// whose visible ASCII spelling differs from the on-disk bytes only in
// invisible Unicode code points (narrow no-break space, curly quotes, ...).
//
// The toolkit file tools call FindSpellingMatches after a path-not-found
// failure; the tool preflight layer uses the same folding table to rank the
// candidate so a unique match can be auto-healed. Sharing the table keeps the
// two layers from disagreeing about what "the same name" means
// (docs/analysis/commandcode-read-tool-design-borrowing-20260926.md §3.7).
package pathrepair

import (
	"os"
	"sort"
	"strings"
)

// FindSpellingMatchesLimit bounds the candidate list returned to the model.
// More than a handful of equally-plausible names is noise, not help.
const FindSpellingMatchesLimit = 8

// NormalizeNameForSpelling folds invisible Unicode variants into a comparable
// ASCII form. Only character folding happens here: no trimming and no
// whitespace collapsing, because name matching must stay faithful to the
// original spelling.
//
// Folding table:
//   - U+202F, U+00A0, U+2000..U+200A, U+205F, U+3000 -> ASCII space
//   - U+2018, U+2019, U+201A, U+201B -> ASCII apostrophe
//   - U+201C, U+201D, U+201E, U+201F -> ASCII double quote
func NormalizeNameForSpelling(s string) string {
	if s == "" {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(s))
	for _, char := range s {
		switch char {
		case '\u202f', '\u00a0', '\u2000', '\u2001', '\u2002', '\u2003',
			'\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009',
			'\u200a', '\u205f', '\u3000':
			builder.WriteRune(' ')
		case '\u2018', '\u2019', '\u201a', '\u201b':
			builder.WriteRune('\'')
		case '\u201c', '\u201d', '\u201e', '\u201f':
			builder.WriteRune('"')
		default:
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

// FindSpellingMatches returns the names in dir whose folded form equals the
// folded form of base, excluding the exact name itself (it needs no repair).
// Results are sorted lexicographically and capped at FindSpellingMatchesLimit.
// An empty dir, a missing directory, or any read failure returns nil.
func FindSpellingMatches(dir string, base string) []string {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	want := strings.ToLower(NormalizeNameForSpelling(base))
	if want == "" {
		return nil
	}

	var matches []string
	for _, entry := range entries {
		name := entry.Name()
		if name == base {
			continue
		}
		if strings.ToLower(NormalizeNameForSpelling(name)) == want {
			matches = append(matches, name)
		}
	}
	if len(matches) == 0 {
		return nil
	}

	sort.Strings(matches)
	if len(matches) > FindSpellingMatchesLimit {
		matches = matches[:FindSpellingMatchesLimit]
	}
	return matches
}
