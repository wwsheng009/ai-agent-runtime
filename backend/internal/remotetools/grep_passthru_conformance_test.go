package remotetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

func grepPassthruTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"a.txt": "one\nneedle here\nthree\n",
		"b.txt": "alpha\nbeta\n",
		"c.txt": "needle one\nmid\nneedle two\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func passthruLabels(t *testing.T, root string, result searchcore.GrepResult) []string {
	t.Helper()
	labels := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		rel, err := filepath.Rel(root, match.File)
		if err != nil {
			t.Fatalf("rel: %v", err)
		}
		separator := ":"
		if match.IsContext {
			separator = "-"
		}
		labels = append(labels, fmt.Sprintf("%s%s%d:%s", filepath.ToSlash(rel), separator, match.Line, match.Text))
	}
	return labels
}

// TestRemoteGrepPassthruMatchesWalker pins the whole-file output contract
// across engines: the same lines, in the same order, with the same match /
// context marks - including replacement, invert, max_count and only_matching.
func TestRemoteGrepPassthruMatchesWalker(t *testing.T) {
	root := grepPassthruTree(t)
	runner := resolveRipgrepRunner()

	cases := []struct {
		name string
		opts searchcore.GrepOptions
		// expectWalker marks the combinations ripgrep cannot answer under the
		// walker's contract: it stops reading a file once its match cap is
		// reached, so the trailing context lines never arrive.
		expectWalker bool
	}{
		{name: "plain", opts: searchcore.GrepOptions{Pattern: "needle", Passthru: true}},
		{name: "replace", opts: searchcore.GrepOptions{Pattern: "needle", Passthru: true, Replace: "X"}},
		{name: "invert", opts: searchcore.GrepOptions{Pattern: "needle", Passthru: true, InvertMatch: true}},
		{name: "max count", opts: searchcore.GrepOptions{Pattern: "needle", Passthru: true, MaxCount: 1, MaxCountSet: true}, expectWalker: true},
		{name: "only matching", opts: searchcore.GrepOptions{Pattern: "needle", Passthru: true, OnlyMatching: true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Path = root
			opts.Limit = 50
			walker, err := searchcore.Grep(context.Background(), opts)
			if err != nil {
				t.Fatalf("walker: %v", err)
			}
			walkerLabels := passthruLabels(t, root, walker)
			if len(walkerLabels) == 0 {
				t.Fatal("walker produced no passthru lines")
			}

			if !runner.available() {
				return
			}
			rgResult, used, rgErr := runner.grep(context.Background(), opts)
			if rgErr != nil {
				t.Fatalf("ripgrep: %v", rgErr)
			}
			if tc.expectWalker {
				if used {
					t.Fatal("ripgrep accepted a combination its output contract cannot serve; the results would diverge")
				}
				return
			}
			if !used {
				t.Fatal("ripgrep declined a passthru request it supports")
			}
			rgLabels := passthruLabels(t, root, rgResult)
			if len(rgLabels) != len(walkerLabels) {
				t.Fatalf("engine line count differs:\nrg:     %v\nwalker: %v", rgLabels, walkerLabels)
			}
			for i := range walkerLabels {
				if rgLabels[i] != walkerLabels[i] {
					t.Fatalf("line %d differs:\nrg:     %v\nwalker: %v", i, rgLabels, walkerLabels)
				}
			}
		})
	}
}
