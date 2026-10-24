package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/viewcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// viewSymbolConformanceKeys are the fields a symbol request must publish the
// same way on both paths.
var viewSymbolConformanceKeys = []string{
	"view_symbol_note", "file_path", "offset", "limit", "offset_resolved",
	"lines_read", "total_lines", "eof", "is_truncated", "suggested_next_offset",
}

// viewFailureMessage reads the model-visible failure text of a local result.
//
// The local envelope carries it in Error and leaves Content empty; the shared
// handler publishes the same sentence in both fields (that is what a remote
// consumer reads). Comparing the sentence rather than one field keeps this test
// about the tool's answer; the envelopes themselves converge when the local
// Execute flips onto the shared handler.
func viewFailureMessage(result *toolkit.ToolResult) string {
	if result == nil {
		return ""
	}
	if result.Error != nil {
		return result.Error.Error()
	}
	return result.Content
}

// TestViewLocalAndCoreResolveTheSameSymbol runs the `symbol` argument through
// both execution paths against the same code index: a resolved symbol must
// produce the same window, a fallback must produce the same note, and a symbol
// with nothing to fall back to must fail with the same sentence.
func TestViewLocalAndCoreResolveTheSameSymbol(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	fixture := newCodeToolsFixture(t)
	ctx := context.Background()

	tool := newViewToolAt(t, fixture.root)
	tool.SetCodeIndexResolver(fixture.resolver)

	cases := []struct {
		name string
		args map[string]interface{}
	}{
		{"resolved symbol", map[string]interface{}{"symbol": "Beta"}},
		{"resolved symbol honours limit", map[string]interface{}{"symbol": "Beta", "limit": 1}},
		{"resolved symbol wins over file_path", map[string]interface{}{"symbol": "Beta", "file_path": fixture.fileRel}},
		{"unresolved symbol falls back", map[string]interface{}{"symbol": "Missing", "file_path": fixture.fileRel}},
		{"unresolved symbol without a window", map[string]interface{}{"symbol": "Missing"}},
		{"unresolved symbol with files only", map[string]interface{}{
			"symbol": "Missing",
			"files":  []interface{}{map[string]interface{}{"file_path": fixture.fileRel}},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			local, err := tool.Execute(ctx, tc.args)
			if err != nil || local == nil {
				t.Fatalf("local view failed: result=%#v err=%v", local, err)
			}

			handler := viewcore.NewHandler(toolcore.Definition{
				Name:                "view",
				Version:             "1.1.0",
				DefaultOutputBudget: viewOutputBudgetBytes,
			})
			core, err := handler.Execute(ctx, &toolcore.Call{
				Args:    tc.args,
				Runtime: tool.CoreRuntime(ctx),
			})
			if err != nil || core == nil {
				t.Fatalf("core view failed: result=%#v err=%v", core, err)
			}
			shared := toolkit.ResultToToolkit(core)

			if shared.Success != local.Success {
				t.Fatalf("success diverged: local=%v (%q) core=%v (%q)",
					local.Success, viewFailureMessage(local), shared.Success, viewFailureMessage(shared))
			}
			if !local.Success {
				if got, want := viewFailureMessage(shared), viewFailureMessage(local); got != want {
					t.Fatalf("failure text diverged:\nlocal=%q\ncore =%q", want, got)
				}
				return
			}
			if shared.Content != local.Content {
				t.Fatalf("content diverged:\n--- local ---\n%s\n--- core ---\n%s", local.Content, shared.Content)
			}
			for _, key := range viewSymbolConformanceKeys {
				if !reflect.DeepEqual(shared.Metadata[key], local.Metadata[key]) {
					t.Fatalf("metadata[%q] diverged:\nlocal=%#v\ncore =%#v",
						key, local.Metadata[key], shared.Metadata[key])
				}
			}
			// A resolved symbol must actually read the symbol's range, not the
			// file's first line: the fixture's Beta spans lines 5-7.
			if strings.Contains(tc.name, "resolved") && strings.Contains(tc.name, "symbol") {
				if !strings.Contains(shared.Content, "5: func Beta()") {
					t.Fatalf("expected the symbol range, got %q", shared.Content)
				}
			}
		})
	}
}
