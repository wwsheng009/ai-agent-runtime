package remotetools

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

// TestRemoteGlobUsesSharedMatcher pins that the remote search path and the
// local glob surface resolve patterns through the same matcher. The table is
// intentionally the same semantics exercised by
// searchcore/glob_conformance_test.go.
func TestRemoteGlobUsesSharedMatcher(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"*.go", "a.go", true},
		{"*.go", "sub/a.go", true},
		{"**/*.go", "sub/a.go", true},
		{"src/**/test/*.go", "src/a/b/test/x.go", true},
		{"src/**/test/*.go", "src/test/x.go", true},
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},
		{"[ab].go", "a.go", true},
		{"[ab].go", "c.go", false},
		{"src/{a,b}/*.{go,ts}", "src/b/x.ts", true},
	}
	for _, tc := range cases {
		matcher, err := searchcore.NewGlobMatcher(tc.pattern)
		if err != nil {
			t.Fatalf("NewGlobMatcher(%q): %v", tc.pattern, err)
		}
		if got := matcher.Match(tc.path); got != tc.want {
			t.Errorf("remote matcher %q/%q = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}
