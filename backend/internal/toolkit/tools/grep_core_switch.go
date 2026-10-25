package tools

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// coreSearchParams lists the parameters whose semantics already live in
// searchcore. A call that uses only these keys runs the shared handler, so the
// local tool executes literally the same code as the remote surface. Anything
// outside the set (rg_args, pattern_file, presentation-only flags, ...) stays
// on the legacy path until its semantics move into the core.
var coreSearchParams = map[string]bool{
	"pattern": true, "regexp": true, "patterns": true,
	"pattern_file": true, "pattern_files": true,
	"path": true, "paths": true,
	"glob": true, "include": true, "exclude": true,
	"literal": true, "fixed_strings": true,
	"ignore_case": true, "case_insensitive": true, "case_sensitive": true, "smart_case": true,
	"word": true, "word_regexp": true, "line_regexp": true, "invert_match": true,
	"limit": true, "offset": true,
	"type": true, "file_type": true, "type_not": true,
	// Type definitions are resolved by the shared core into the same glob
	// filters as type/type_not, on both engines, so they belong here rather
	// than on the ripgrep-owned legacy path.
	"type_add": true, "type_clear": true,
	// Ordering is resolved by the shared core (and implemented by both
	// engines), so the schema spellings no longer belong to the ripgrep-owned
	// legacy path.
	"sort": true, "sortr": true, "sort_reverse": true,
	"sort_files": true, "no_sort_files": true,
	"mode": true, "files_with_matches": true, "count": true,
	"files_without_match": true, "files_without_matches": true,
	"max_count": true, "max_depth": true, "max_filesize": true,
	"one_file_system": true,
	"stats":           true, "only_matching": true,
	"column": true, "trim": true,
	"max_columns": true, "max_columns_preview": true, "no_max_columns_preview": true,
	"context": true, "before_context": true, "after_context": true,
	"hidden": true, "no_hidden": true,
	"unrestricted_level": true,
	"no_ignore":          true, "no_ignore_parent": true, "no_ignore_vcs": true,
	"no_ignore_dot": true, "no_ignore_global": true, "no_ignore_files": true,
	"no_messages":                  true,
	"count_matches":                true,
	"glob_case_insensitive":        true,
	"ignore_file_case_insensitive": true,
	"ignore_file":                  true, "ignore_files": true,
	// Presentation-only rg spellings: the shared handler accepts them and reports
	// which ones were set in metadata.ignored_presentation. They change nothing,
	// which is exactly why they belong on the shared code path - a no-op has no
	// business being the one thing a second implementation owns.
	"line_number": true, "no_line_number": true,
	"with_filename": true, "no_filename": true,
	"heading": true, "no_heading": true,
	"pretty": true, "text": true, "binary": true,
	"line_buffered": true, "no_line_buffered": true,
	"block_buffered": true, "no_block_buffered": true,
	"null": true, "null_data": true,
	"field_context_separator": true, "path_separator": true, "color": true,
	// Engine selection: ripgrep-only capabilities. The shared handler reads them
	// and dispatches to the engine first, so a PCRE pattern is not handed to Go's
	// regexp before ripgrep gets its turn.
	"pcre2": true, "engine": true, "auto_hybrid_regex": true,
	"multiline": true, "multiline_dotall": true, "crlf": true,
	// `no_config` is a three-state request (unset / true / false), so the shared
	// handler reads presence as well as value; the default stays deterministic.
	"no_config": true,
	// `replace` is implemented by both engines - ripgrep substitutes with its
	// own -r, the walker expands the same template through Go's regexp - so it
	// is a shared capability rather than an rg-only flag.
	"replace": true,
	// json/json_output ask for ripgrep's own event stream. The shared handler
	// owns that format end to end (window, path translation, stats) and reports
	// the missing engine plainly instead of rendering something else.
	"json": true, "json_output": true,
	// Paging and context presentation: the page size has two spellings that mean
	// one thing, and the group separator is rendered by the shared renderer, so
	// both engines print the same boundaries.
	"head_limit":        true,
	"context_separator": true, "no_context_separator": true,
	// follow is implemented by both engines: ripgrep gets --follow, the walker
	// walks the links itself (cycle-protected), plus the Windows junction default.
	"follow": true,
}

// executeCoreSearch runs the shared core handler when a call stays inside the
// core-covered parameter set. handled=false means the legacy path must run.
func (g *GrepTool) executeCoreSearch(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, bool) {
	for key := range params {
		if !coreSearchParams[key] {
			return nil, false
		}
	}
	converted, err := executeCoreSearchTool(ctx, g, params)
	if err != nil || converted == nil {
		return nil, false
	}
	// The local tool has always published `ignore_files` as the explicit path
	// list, while the shared handler uses that key for the "ignore handling is
	// on" bit and keeps the list in ignore_files_list. Both shapes are relied
	// on, so adapt at the surface boundary instead of repurposing a key that
	// existing callers type-assert.
	if converted != nil && converted.Metadata != nil {
		if list, ok := converted.Metadata["ignore_files_list"].([]string); ok {
			converted.Metadata["ignore_files"] = list
		} else {
			converted.Metadata["ignore_files"] = []string{}
		}
	}
	return converted, true
}

func boolParamValue(params map[string]interface{}, key string) bool {
	value, _ := params[key].(bool)
	return value
}

func intParamValue(params map[string]interface{}, key string) int {
	switch value := params[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}
