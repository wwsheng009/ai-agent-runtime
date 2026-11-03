package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

func TestStoragePruneRuntimeDeletesHistoricReceipts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "session_runtime.sqlite")
	store, err := runtimechat.NewSQLiteRuntimeStore(&runtimechat.RuntimeStoreConfig{
		Path:                 path,
		ToolReceiptRetention: 100,
		ToolReceiptMaxBytes:  1 << 20,
	})
	require.NoError(t, err)
	base := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for index := 1; index <= 4; index++ {
		require.NoError(t, store.SaveToolReceipt(ctx, runtimechat.ToolExecutionReceipt{
			SessionID:   "prune-command",
			ToolCallID:  "tool-" + string(rune('0'+index)),
			MessageJSON: []byte(`{"content":"ok"}`),
			CreatedAt:   base.Add(time.Duration(index) * time.Second),
		}))
	}
	require.NoError(t, store.Close())

	var out, errOut bytes.Buffer
	dryRunCode := runStoragePruneRuntime(storagePruneRuntimeOptions{
		out:              &out,
		errOut:           &errOut,
		json:             true,
		dbPath:           path,
		toolReceiptRows:  2,
		toolReceiptBytes: 1 << 20,
		cacheRequestRows: 2,
		eventRows:        2,
		dryRun:           true,
	})
	require.Equal(t, statsExitOK, dryRunCode, "stderr=%s", errOut.String())
	var dryRunReport storagePruneRuntimeReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &dryRunReport))
	require.Equal(t, "planned", dryRunReport.Status)
	require.Equal(t, int64(2), dryRunReport.Stats.ToolReceiptsDeleted)

	check, err := runtimechat.NewSQLiteRuntimeStore(&runtimechat.RuntimeStoreConfig{
		Path:                 path,
		ToolReceiptRetention: 100,
		ToolReceiptMaxBytes:  1 << 20,
	})
	require.NoError(t, err)
	receipts, err := check.ListToolReceipts(ctx, "prune-command", 0)
	require.NoError(t, err)
	require.Len(t, receipts, 4, "dry-run must not delete rows")
	require.NoError(t, check.Close())

	out.Reset()
	errOut.Reset()
	code := runStoragePruneRuntime(storagePruneRuntimeOptions{
		out:              &out,
		errOut:           &errOut,
		json:             true,
		dbPath:           path,
		toolReceiptRows:  2,
		toolReceiptBytes: 1 << 20,
		cacheRequestRows: 2,
		eventRows:        2,
	})
	require.Equal(t, statsExitOK, code, "stderr=%s", errOut.String())

	var report storagePruneRuntimeReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "pruned", report.Status)
	require.Equal(t, int64(2), report.Stats.ToolReceiptsDeleted)

	db, err := runtimechat.NewSQLiteRuntimeStore(&runtimechat.RuntimeStoreConfig{
		Path:                 path,
		ToolReceiptRetention: 100,
		ToolReceiptMaxBytes:  1 << 20,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	receipts, err = db.ListToolReceipts(ctx, "prune-command", 0)
	require.NoError(t, err)
	require.Len(t, receipts, 2)
	require.Equal(t, "tool-4", receipts[0].ToolCallID)
}
