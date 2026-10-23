package searchcore

import "testing"

// TestGlobMatcherMatchesSegmentMatcher pins the single-semantics invariant:
// the wrapper used by remote search and the segment matcher used by local glob
// must agree on the same pattern/path pairs.
func TestGlobMatcherMatchesSegmentMatcher(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"*.go", "a.go", true},
		{"*.go", "sub/a.go", true},
		{"**/*.go", "sub/a.go", true},
		{"sub/*.go", "sub/a.go", true},
		{"sub/*.go", "other/a.go", false},
		{"src/**/test/*.go", "src/a/b/test/x.go", true},
		{"src/**/test/*.go", "src/test/x.go", true},
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},
		{"[ab].go", "a.go", true},
		{"[ab].go", "c.go", false},
		{"*.{go,ts}", "a.ts", true},
		{"src/{a,b}/*.{go,ts}", "src/b/x.ts", true},
		{"src/{a,b}/*.{go,ts}", "src/c/x.ts", false},
	}
	for _, tc := range cases {
		matcher, err := NewGlobMatcher(tc.pattern)
		if err != nil {
			t.Fatalf("NewGlobMatcher(%q): %v", tc.pattern, err)
		}
		if got := matcher.Match(tc.path); got != tc.want {
			t.Errorf("GlobMatcher(%q).Match(%q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}

		// The segment matcher is the same implementation the local glob surface
		// uses. For patterns without brace alternation it must agree exactly.
		if tc.pattern == "*.{go,ts}" || tc.pattern == "src/{a,b}/*.{go,ts}" {
			continue
		}
		matched, err := MatchGlobPath(tc.pattern, tc.path, false)
		if err != nil {
			t.Fatalf("MatchGlobPath(%q): %v", tc.pattern, err)
		}
		if matched != tc.want {
			t.Errorf("MatchGlobSegments(%q, %q) = %v, want %v", tc.pattern, tc.path, matched, tc.want)
		}
	}
}

func TestGlobMatcherBraceExpansionIsRecursive(t *testing.T) {
	matcher, err := NewGlobMatcher("src/{a,b}/*.{go,ts}")
	if err != nil {
		t.Fatalf("NewGlobMatcher: %v", err)
	}
	for _, path := range []string{"src/a/x.go", "src/b/y.ts"} {
		if !matcher.Match(path) {
			t.Errorf("expected %q to match", path)
		}
	}
	if matcher.Match("src/c/x.go") {
		t.Error("unexpected match for src/c/x.go")
	}
}
