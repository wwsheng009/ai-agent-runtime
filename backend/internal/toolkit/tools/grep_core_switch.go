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
}

// executeCoreSearch runs the shared core handler when a call stays inside the
// core-covered parameter set. handled=false means the legacy path must run.
func (g *GrepTool) executeCoreSearch(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, bool) {
	for key := range params {
		if !coreSearchParams[key] {
			return nil, false
		}
	}
	if boolParamValue(params, "only_matching") &&
		(intParamValue(params, "context") > 0 || intParamValue(params, "before_context") > 0 || intParamValue(params, "after_context") > 0) {
		// The core rejects this combination; the legacy path accepts it, so
		// route it there until the core implements the mix.
		return nil, false
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
