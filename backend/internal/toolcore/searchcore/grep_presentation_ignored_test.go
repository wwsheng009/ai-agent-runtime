package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

type presentationPaths struct{ root string }

func (p presentationPaths) Root() string { return p.root }

func (p presentationPaths) Resolve(raw string) (string, error) {
	if raw == "" {
		return p.root, nil
	}
	if filepath.IsAbs(raw) {
		return raw, nil
	}
	return filepath.Join(p.root, raw), nil
}

func (p presentationPaths) Relative(abs string) string {
	rel, err := filepath.Rel(p.root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

type presentationRuntime struct{ paths toolcore.PathResolver }

func (r presentationRuntime) Paths() toolcore.PathResolver            { return r.paths }
func (r presentationRuntime) Permissions() toolcore.PermissionChecker { return nil }
func (r presentationRuntime) Commands() toolcore.CommandPolicy        { return nil }
func (r presentationRuntime) Env() toolcore.EnvProvider               { return nil }
func (r presentationRuntime) Output() toolcore.OutputShaper           { return nil }
func (r presentationRuntime) Artifacts() toolcore.ArtifactSink        { return nil }
func (r presentationRuntime) Sessions() toolcore.SessionState         { return nil }
func (r presentationRuntime) Clock() toolcore.Clock                   { return nil }
func (r presentationRuntime) IDs() toolcore.IDGenerator               { return nil }

func runGrepHandler(t *testing.T, root string, args map[string]any) *toolcore.Result {
	t.Helper()
	handler := NewGrepHandler(toolcore.Definition{Name: "grep", Version: "1.0.0"})
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    args,
		Runtime: presentationRuntime{paths: presentationPaths{root: root}},
	})
	if err != nil || result == nil {
		t.Fatalf("grep failed: err=%v result=%+v", err, result)
	}
	return result
}

// TestGrepReportsIgnoredPresentationFlags pins the presentation-only rg
// spellings: they are accepted, they change nothing about the page, and which
// ones were set is reported back - so "understood and deliberately not applied"
// can never look like "silently dropped".
func TestGrepReportsIgnoredPresentationFlags(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	baseline := runGrepHandler(t, root, map[string]any{"pattern": "needle"})
	empty, ok := baseline.Metadata["ignored_presentation"].([]string)
	if !ok {
		t.Fatalf("ignored_presentation must always be published, got %#v", baseline.Metadata["ignored_presentation"])
	}
	if len(empty) != 0 {
		t.Fatalf("a call with no presentation flags must report an empty list, got %#v", empty)
	}

	flagged := runGrepHandler(t, root, map[string]any{
		"pattern":                 "needle",
		"line_number":             true,
		"pretty":                  true,
		"color":                   "never",
		"heading":                 false, // false is not a request for anything
		"path_separator":          "  ",  // whitespace only: not a value
		"field_context_separator": `""`,  // quoted empty placeholder: not passed
	})
	want := []string{"line_number", "pretty", "color=never"}
	got, _ := flagged.Metadata["ignored_presentation"].([]string)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ignored_presentation = %#v, want %#v", got, want)
	}
	if flagged.Content != baseline.Content {
		t.Fatalf("presentation flags must not change the page:\nwith=%q\nwithout=%q",
			flagged.Content, baseline.Content)
	}
}

// TestGrepPresentationReportKeepsEverySpellingAndOrder walks the whole supported
// family at once: every spelling the schema declares is reported when set, once
// each, in the order the builtin tool has always used.
func TestGrepPresentationReportKeepsEverySpellingAndOrder(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := runGrepHandler(t, root, map[string]any{
		"pattern":           "needle",
		"text":              true,
		"binary":            true,
		"null":              true,
		"null_data":         true,
		"with_filename":     true,
		"no_filename":       true,
		"line_buffered":     true,
		"no_line_buffered":  true,
		"block_buffered":    true,
		"no_block_buffered": true,
		"no_line_number":    true,
		"no_heading":        true,
	})
	got, _ := result.Metadata["ignored_presentation"].([]string)
	expect := []string{
		"no_heading", "with_filename", "no_line_number", "no_filename",
		"line_buffered", "no_line_buffered", "block_buffered", "no_block_buffered",
		"null", "null_data", "text", "binary",
	}
	if !reflect.DeepEqual(got, expect) {
		t.Fatalf("ignored_presentation = %#v, want %#v", got, expect)
	}
}
