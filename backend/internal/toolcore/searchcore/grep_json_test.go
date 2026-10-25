package searchcore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jsonEventSearcher answers a json call with a canned event stream, which is
// exactly what the engine hands the handler on that path.
type jsonEventSearcher struct {
	events    []string
	paths     []string
	stats     GrepJSONStats
	available bool
}

func (s *jsonEventSearcher) Grep(context.Context, GrepOptions) (GrepResult, bool, error) {
	if !s.available {
		return GrepResult{}, false, nil
	}
	return GrepResult{
		Mode:           GrepModeContent,
		JSONEvents:     s.events,
		JSONEventPaths: s.paths,
		JSONStats:      s.stats,
		FilesScanned:   s.stats.FilesSearched,
	}, true, nil
}

// Glob completes the Searcher contract; these tests never ask for it.
func (s *jsonEventSearcher) Glob(context.Context, GlobOptions) (GlobResult, bool, error) {
	return GlobResult{}, false, nil
}

func jsonFixture(t *testing.T, root string) ([]string, []string) {
	t.Helper()
	a := filepath.Join(root, "a.txt")
	if err := os.WriteFile(a, []byte("foo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	events := []string{
		`{"type":"begin","data":{"path":{"text":` + quoteJSON(a) + `}}}`,
		`{"type":"match","data":{"path":{"text":` + quoteJSON(a) + `},"lines":{"text":"foo\n"},"line_number":1,"absolute_offset":0,"submatches":[{"match":{"text":"foo"},"start":0,"end":3}]}}`,
		`{"type":"end","data":{"path":{"text":` + quoteJSON(a) + `}}}`,
		`{"type":"summary","data":{"elapsed_total":{"human":"0.000100s","nanos":100000,"secs":0},"stats":{"bytes_printed":0,"bytes_searched":4,"matched_lines":1,"matches":1,"searches":1,"searches_with_match":1,"files_searched":1}}}`,
	}
	paths := []string{a, a, a, ""}
	return events, paths
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// TestGrepJSONPageKeepsTheStreamParseable pins the json contract: the page is a
// prefix of ripgrep's own stream, every line stays valid JSON, and - unlike the
// rendered page - no paging notice is ever appended, because that would corrupt
// the payload the caller parses.
func TestGrepJSONPageKeepsTheStreamParseable(t *testing.T) {
	root := t.TempDir()
	events, paths := jsonFixture(t, root)
	searcher := &jsonEventSearcher{
		events:    events,
		paths:     paths,
		stats:     GrepJSONStats{Matches: 1, MatchedLines: 1, FilesSearched: 1, BytesSearched: 4},
		available: true,
	}

	result := runGrepWithSearcher(t, root, searcher, map[string]any{
		"pattern": "foo", "json": true, "limit": 2,
	})
	lines := strings.Split(result.Content, "\n")
	if len(lines) != 2 {
		t.Fatalf("limit 2 must yield two stream lines, got %d:\n%s", len(lines), result.Content)
	}
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("every line must stay valid JSON, got %q: %v", line, err)
		}
	}
	if strings.Contains(result.Content, "next_offset") {
		t.Fatalf("a paging notice would corrupt the JSON payload:\n%s", result.Content)
	}
	if result.Metadata["json"] != true {
		t.Fatalf("metadata.json = %#v, want true", result.Metadata["json"])
	}
	if result.Metadata["normalized_output"] != false {
		t.Fatalf("metadata.normalized_output = %#v, want false", result.Metadata["normalized_output"])
	}
	stats, ok := result.Metadata["stats"].(map[string]any)
	if !ok {
		t.Fatalf("json mode must publish ripgrep's own counters, got %#v", result.Metadata["stats"])
	}
	if stats["matches"] != 1 || stats["matched_lines"] != 1 || stats["files_searched"] != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
	if result.Metadata["has_more"] != true {
		t.Fatalf("two more lines existed, so has_more must be true: %#v", result.Metadata["has_more"])
	}
	if result.Metadata["next_offset"] != 2 {
		t.Fatalf("next_offset = %#v, want 2 (the line this page stopped at)", result.Metadata["next_offset"])
	}
}

// TestGrepJSONRewritesEventPathsToDisplayPaths: the stream is passed through
// byte-for-byte except for the path, which follows the same convention every
// other output shape of this tool uses - a client reads one path language.
func TestGrepJSONRewritesEventPathsToDisplayPaths(t *testing.T) {
	root := t.TempDir()
	events, paths := jsonFixture(t, root)
	searcher := &jsonEventSearcher{events: events, paths: paths, available: true}

	result := runGrepWithSearcher(t, root, searcher, map[string]any{"pattern": "foo", "json": true})
	var first map[string]any
	if err := json.Unmarshal([]byte(strings.Split(result.Content, "\n")[0]), &first); err != nil {
		t.Fatal(err)
	}
	data, _ := first["data"].(map[string]any)
	pathMap, _ := data["path"].(map[string]any)
	if got := pathMap["text"]; got != "a.txt" {
		t.Fatalf("the event path must be the display path, got %#v", got)
	}
}

// TestGrepJSONWithoutRipgrepFailsClosed: the event stream is ripgrep's own
// format, so there is nothing to render when the engine cannot run. The call
// must say that plainly instead of returning some other text under the json key.
func TestGrepJSONWithoutRipgrepFailsClosed(t *testing.T) {
	root := t.TempDir()
	searcher := &jsonEventSearcher{available: false}

	result := runGrepWithSearcher(t, root, searcher, map[string]any{"pattern": "foo", "json": true})
	if result.Success {
		t.Fatalf("a json call without ripgrep must fail, got %q", result.Content)
	}
	message := result.Content
	if result.Err != nil {
		message = result.Err.Error()
	}
	if !strings.Contains(message, "ripgrep") {
		t.Fatalf("the failure must name the missing engine, got %q", message)
	}
	if strings.Contains(result.Content, `"type":"match"`) {
		t.Fatalf("nothing may be rendered as if it were a JSON stream:\n%s", result.Content)
	}
}

// TestRewriteGrepJSONPath covers the replacement itself: escaping, a line whose
// path already is the display path, and the events that carry no path at all.
func TestRewriteGrepJSONPath(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		display string
		want    string
	}{
		{
			name:    "relative path",
			line:    `{"type":"match","data":{"path":{"text":"a.txt"},"line_number":3}}`,
			display: "sub/a.txt",
			want:    `{"type":"match","data":{"path":{"text":"sub/a.txt"},"line_number":3}}`,
		},
		{
			name:    "windows separator survives json escaping",
			line:    `{"type":"match","data":{"path":{"text":"C:\\work\\a.txt"},"line_number":1}}`,
			display: "a.txt",
			want:    `{"type":"match","data":{"path":{"text":"a.txt"},"line_number":1}}`,
		},
		{
			name:    "already the display path stays byte-identical",
			line:    `{"type":"match","data":{"path":{"text":"a.txt"},"line_number":1}}`,
			display: "a.txt",
			want:    `{"type":"match","data":{"path":{"text":"a.txt"},"line_number":1}}`,
		},
		{
			name:    "summary carries no path",
			line:    `{"type":"summary","data":{"stats":{"matches":1}}}`,
			display: "a.txt",
			want:    `{"type":"summary","data":{"stats":{"matches":1}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rewriteGrepJSONPath(tc.line, tc.display); got != tc.want {
				t.Fatalf("rewriteGrepJSONPath() = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestRipgrepJSONModeCollectsRawEvents is the engine half: the argv still asks
// for the event stream, the raw lines arrive untouched, and the summary counters
// are summed the way the handler publishes them.
func TestRipgrepJSONModeCollectsRawEvents(t *testing.T) {
	root := t.TempDir()
	events, _ := jsonFixture(t, root)
	var gotArgs []string
	engine := Ripgrep{
		Path: "rg",
		Stream: func(_ context.Context, args []string, _ string, consume func(line []byte) bool) error {
			gotArgs = append([]string(nil), args...)
			for _, line := range events {
				if !consume([]byte(line)) {
					break
				}
			}
			return nil
		},
	}

	result, used, err := engine.Grep(context.Background(), GrepOptions{
		Pattern: "foo", Path: root, Limit: 10, JSONOutput: true,
	})
	if err != nil || !used {
		t.Fatalf("engine must serve json output: err=%v used=%v", err, used)
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "--json") {
		t.Fatalf("the event stream still comes from --json:\n%v", gotArgs)
	}
	if len(result.JSONEvents) != len(events) {
		t.Fatalf("collected %d events, want %d", len(result.JSONEvents), len(events))
	}
	for i := range events {
		if result.JSONEvents[i] != events[i] {
			t.Fatalf("event %d was modified:\n got %s\nwant %s", i, result.JSONEvents[i], events[i])
		}
	}
	wantPath := filepath.Join(root, "a.txt")
	if result.JSONEventPaths[1] != wantPath {
		t.Fatalf("event path = %q, want %q", result.JSONEventPaths[1], wantPath)
	}
	if result.JSONEventPaths[3] != "" {
		t.Fatalf("the summary carries no path, got %q", result.JSONEventPaths[3])
	}
	if result.JSONStats.Matches != 1 || result.JSONStats.FilesSearched != 1 {
		t.Fatalf("summary counters were not summed: %+v", result.JSONStats)
	}
}
