package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/viewcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// viewBatchConformanceKeys are the batch fields both surfaces must agree on.
// Surface stamps (skip_render_truncation, the declared budget) are excluded:
// those describe one surface's render-layer contract, not the tool.
var viewBatchConformanceKeys = []string{
	"batch", "request_count", "succeeded_count", "failed_count", "partial_failure",
	"compact", "batch_default_limit_applied", "batch_tail_reserve_bytes",
	"batch_aggregate_budget_bytes", "batch_bytes_emitted", "batch_skipped_count",
	"batch_skipped_files", "failures_omitted", "failed_items_omitted", "items",
	"failed_items",
}

// TestViewLocalAndCoreRenderTheSameBatch runs whole batches through both
// execution paths. The aggregate window, the skip accounting, the trailing
// blocks, the compact digest and the batch metadata are one implementation now,
// so the delivered body must be identical - including the failure text and the
// byte accounting that decides which sections fit.
func TestViewLocalAndCoreRenderTheSameBatch(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")

	cases := []struct {
		name  string
		args  map[string]interface{}
		needs func(root string)
		// wantSkipped / wantFailed pin the outcome of the case itself, so a case
		// that stops exercising the cap or the failure path fails instead of
		// silently comparing two empty batches.
		wantSkipped int
		wantFailed  int
	}{
		{
			name:       "mixed with a failure",
			wantFailed: 1,
			args: map[string]interface{}{"files": []interface{}{
				map[string]interface{}{"file_path": "small.txt"},
				map[string]interface{}{"file_path": "missing.txt"},
				map[string]interface{}{"file_path": "long.txt"},
			}},
		},
		{
			name: "compact scan",
			args: map[string]interface{}{
				"compact": true,
				"files": []interface{}{
					map[string]interface{}{"file_path": "small.txt"},
					map[string]interface{}{"file_path": "long.txt"},
				},
			},
		},
		{
			// Three full windows of one 2000-line file. One window is ~28 KiB
			// (the per-item byte budget stops it), so the first two fit the
			// ~61 KiB section budget and the third must be reported as skipped
			// with the accounting that says so. Getting this identical means the
			// ported budget is the same budget.
			name:        "aggregate cap",
			wantSkipped: 1,
			needs: func(root string) {
				var builder strings.Builder
				for i := 0; i < 2000; i++ {
					fmt.Fprintf(&builder, "line %04d: %s\n", i, strings.Repeat("y", 36))
				}
				if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(builder.String()), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			args: map[string]interface{}{"files": []interface{}{
				map[string]interface{}{"file_path": "big.txt", "limit": 2000},
				map[string]interface{}{"file_path": "big.txt", "limit": 2000},
				map[string]interface{}{"file_path": "big.txt", "limit": 2000},
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			viewConformanceFixtures(t, root)
			if tc.needs != nil {
				tc.needs(root)
			}
			ctx := context.Background()

			tool := newViewToolAt(t, root)
			local, err := tool.Execute(ctx, tc.args)
			if err != nil || local == nil {
				t.Fatalf("local batch failed: result=%#v err=%v", local, err)
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
				t.Fatalf("core batch failed: result=%#v err=%v", core, err)
			}
			shared := toolkit.ResultToToolkit(core)

			if got, _ := local.Metadata["batch_skipped_count"].(int); got != tc.wantSkipped {
				t.Fatalf("batch_skipped_count = %d, want %d", got, tc.wantSkipped)
			}
			if got, _ := local.Metadata["failed_count"].(int); got != tc.wantFailed {
				t.Fatalf("failed_count = %d, want %d", got, tc.wantFailed)
			}

			if shared.Success != local.Success {
				t.Fatalf("success diverged: local=%v core=%v", local.Success, shared.Success)
			}
			if shared.Content != local.Content {
				t.Fatalf("batch content diverged:\n--- local (%d bytes) ---\n%s\n--- core (%d bytes) ---\n%s",
					len(local.Content), local.Content, len(shared.Content), shared.Content)
			}
			for _, key := range viewBatchConformanceKeys {
				if !reflect.DeepEqual(shared.Metadata[key], local.Metadata[key]) {
					t.Fatalf("metadata[%q] diverged:\nlocal=%#v\ncore =%#v",
						key, local.Metadata[key], shared.Metadata[key])
				}
			}
		})
	}
}
