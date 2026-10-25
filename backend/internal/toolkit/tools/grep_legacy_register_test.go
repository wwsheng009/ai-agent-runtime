package tools

import (
	"sort"
	"testing"
)

// grepLegacyOnlyParams is the frozen list of parameters the grep schema
// declares but only the local legacy path serves today.
//
// It is a debt register, not a design: every entry below is a parameter the
// shared handler (searchcore) does not honor yet, so a call that uses it falls
// back to the local implementation and the "one tool, one implementation" rule
// stops holding for that call. The three groups have very different costs, which
// is why they are listed together but ported in order:
//
//   - presentation/no-op flags: the tool's own output shape already satisfies
//     them, so the shared handler only has to accept and report them as ignored;
//   - rg-engine flags (pcre2, engine, auto_hybrid_regex, multiline,
//     multiline_dotall, replace, json, json_output, follow, crlf): they need
//     the ripgrep seam the runtime already exposes, plus their own output
//     semantics and tests;
//   - escape hatches (rg_args, passthru): the raw-argument path deliberately
//     mirrors rg's own parser.
//
// The test below fails when a schema parameter appears without core support, so
// the list cannot grow unnoticed; it also fails when the shared handler starts
// honoring one of these, so the register has to be kept honest in both
// directions.
//
// The presentation group, the type set edits (type_add/type_clear) and the
// ordering group (sort/sortr/sort_reverse/sort_files/no_sort_files) have
// already moved: they are core-covered now.
var grepLegacyOnlyParams = []string{
	"passthru", "rg_args",
}

func TestGrepLegacyOnlyParamsAreRegistered(t *testing.T) {
	schema := NewGrepTool().Parameters()
	props, ok := schema["properties"].(map[string]interface{})
	if !ok || len(props) == 0 {
		t.Fatalf("grep schema has no properties: %#v", schema)
	}

	legacy := make([]string, 0, len(props))
	for key := range props {
		if !coreSearchParams[key] {
			legacy = append(legacy, key)
		}
	}
	sort.Strings(legacy)

	want := append([]string(nil), grepLegacyOnlyParams...)
	sort.Strings(want)

	if len(legacy) != len(want) {
		t.Fatalf("legacy-only parameter set changed:\n got %d: %v\nwant %d: %v\n"+
			"Port the parameter into searchcore (or drop it from the schema) and "+
			"update the register in the same change - a schema that offers what only "+
			"one surface honors is the drift this list exists to catch.",
			len(legacy), legacy, len(want), want)
	}
	for i := range legacy {
		if legacy[i] != want[i] {
			t.Fatalf("legacy-only parameter %q is not in the register (or the register is stale):\n got %v\nwant %v",
				legacy[i], legacy, want)
		}
	}
}

// TestGrepCoreSwitchCoversEveryRegisteredParam is the other direction: a
// parameter the shared handler does honor must not be left behind on the legacy
// path, or the same call would be answered by two implementations again.
func TestGrepCoreSwitchCoversEveryRegisteredParam(t *testing.T) {
	registered := make(map[string]bool, len(grepLegacyOnlyParams))
	for _, key := range grepLegacyOnlyParams {
		registered[key] = true
	}
	for key := range coreSearchParams {
		if registered[key] {
			t.Fatalf("parameter %q is covered by the core switch and still listed as legacy-only: "+
				"remove it from grepLegacyOnlyParams so the two lists describe one reality", key)
		}
	}
}

// TestGrepOnlyMatchingWithContextRunsOnTheSharedHandler records that the last
// behavioural hole is closed: the combination used to route to the legacy path
// because the shared handler rejected it, and it now renders `rg -o -C` there.
func TestGrepOnlyMatchingWithContextRunsOnTheSharedHandler(t *testing.T) {
	tool := NewGrepTool()
	for _, args := range []map[string]interface{}{
		{"pattern": "needle", "only_matching": true, "context": 2},
		{"pattern": "needle", "only_matching": true},
	} {
		if _, handled := tool.executeCoreSearch(t.Context(), args); !handled {
			t.Fatalf("the shared handler must answer %v; the legacy path is retired for it", args)
		}
	}
}

// TestGrepTypeSetEditsRunOnTheSharedHandler pins the batch that moved the type
// table edits: a call that only changes type definitions must be answered by
// searchcore on every host, not by the ripgrep-owning legacy path.
func TestGrepTypeSetEditsRunOnTheSharedHandler(t *testing.T) {
	tool := NewGrepTool()
	tool.SetBasePath(t.TempDir())
	for _, args := range []map[string]interface{}{
		{"pattern": "needle", "type_add": "doc:*.md"},
		{"pattern": "needle", "type_add": []interface{}{"go:*.gold"}},
		{"pattern": "needle", "type_clear": "cpp"},
		{"pattern": "needle", "type": "doc", "type_add": "doc:*.md", "type_clear": "doc"},
	} {
		if _, handled := tool.executeCoreSearch(t.Context(), args); !handled {
			t.Fatalf("the shared handler must answer %v; the legacy path no longer owns type definitions", args)
		}
	}
}

// TestGrepSortSpellingsRunOnTheSharedHandler pins the ordering group the same
// way: every spelling resolves in searchcore, so a host with ripgrep and one
// without answer the call with the same order.
func TestGrepSortSpellingsRunOnTheSharedHandler(t *testing.T) {
	tool := NewGrepTool()
	tool.SetBasePath(t.TempDir())
	for _, args := range []map[string]interface{}{
		{"pattern": "needle", "sort": "path"},
		{"pattern": "needle", "sortr": "modified"},
		{"pattern": "needle", "sort_reverse": "path"},
		{"pattern": "needle", "sort_files": true},
		{"pattern": "needle", "sort_files": false},
		{"pattern": "needle", "no_sort_files": true},
	} {
		if _, handled := tool.executeCoreSearch(t.Context(), args); !handled {
			t.Fatalf("the shared handler must answer %v; the legacy path no longer owns ordering", args)
		}
	}
}
