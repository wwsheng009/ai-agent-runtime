package tools

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPathSpellingNormalizeNameForSpelling(t *testing.T) {
	spaceVariants := []rune{
		'\u202f', '\u00a0', '\u2000', '\u2001', '\u2002', '\u2003',
		'\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009',
		'\u200a', '\u205f', '\u3000',
	}
	for _, variant := range spaceVariants {
		require.Equal(t, "a b", normalizeNameForSpelling("a"+string(variant)+"b"),
			"rune %U must fold to an ASCII space", variant)
	}

	for _, variant := range []rune{'\u2018', '\u2019', '\u201a', '\u201b'} {
		require.Equal(t, "don't", normalizeNameForSpelling("don"+string(variant)+"t"),
			"rune %U must fold to an ASCII apostrophe", variant)
	}

	for _, variant := range []rune{'\u201c', '\u201d', '\u201e', '\u201f'} {
		require.Equal(t, `say "hi"`, normalizeNameForSpelling("say "+string(variant)+"hi"+string(variant)),
			"rune %U must fold to an ASCII double quote", variant)
	}

	// Folding only: no trimming and no whitespace collapsing.
	require.Equal(t, " two  spaces ", normalizeNameForSpelling(" two\u00a0 spaces "))
	require.Equal(t, "plain.txt", normalizeNameForSpelling("plain.txt"))
	require.Equal(t, "", normalizeNameForSpelling(""))
}

func TestPathSpellingFindSpellingMatches(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "report\u202Ffinal.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "don\u2019t.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("x"), 0o644))

	// An ASCII space finds the narrow-no-break-space file.
	require.Equal(t, []string{"report\u202Ffinal.txt"}, findSpellingMatches(dir, "report final.txt"))
	// A straight quote finds the curly-quote file.
	require.Equal(t, []string{"don\u2019t.txt"}, findSpellingMatches(dir, "don't.txt"))

	// Exact names need no repair and are never reported as their own match.
	require.Nil(t, findSpellingMatches(dir, "plain.txt"))
	require.Nil(t, findSpellingMatches(dir, "report\u202Ffinal.txt"))
	require.Nil(t, findSpellingMatches(dir, "don\u2019t.txt"))

	// Missing names, missing directories and empty dirs yield nil.
	require.Nil(t, findSpellingMatches(dir, "missing report.txt"))
	require.Nil(t, findSpellingMatches(filepath.Join(dir, "nope"), "report final.txt"))
	require.Nil(t, findSpellingMatches("", "report final.txt"))
}

func TestPathSpellingFindSpellingMatchesLimit(t *testing.T) {
	dir := t.TempDir()
	for _, variant := range []rune{
		'\u2000', '\u2001', '\u2002', '\u2003', '\u2004',
		'\u2005', '\u2006', '\u2007', '\u2008', '\u2009',
	} {
		name := "cap" + string(variant) + "file.txt"
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644))
	}
	// A decoy sharing the prefix but folding differently must stay out.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cap-file.txt"), []byte("x"), 0o644))

	matches := findSpellingMatches(dir, "cap file.txt")
	require.Len(t, matches, 8)
	require.True(t, sort.StringsAreSorted(matches))
	require.NotContains(t, matches, "cap-file.txt")
	for _, match := range matches {
		require.Equal(t, "cap file.txt", normalizeNameForSpelling(match))
	}
}
