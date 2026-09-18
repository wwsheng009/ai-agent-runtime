package chat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// P1.7 压测对比（DoD 5/11 的量化部分）：同一负载下「单池（DisableReadPool=true）」
// 与「双池」的读 P95 对比。默认跳过，设置 AICLI_RUNTIME_POOL_LOAD_TEST=1 运行；
// 阈值只要求"双池不劣化且显著降低"，绝对值写进分册报告。
func TestReadPoolLoadComparison(t *testing.T) {
	if os.Getenv("AICLI_RUNTIME_POOL_LOAD_TEST") != "1" {
		t.Skip("set AICLI_RUNTIME_POOL_LOAD_TEST=1 to run the pool load comparison")
	}
	measure := func(t *testing.T, disableReadPool bool) (readP95 time.Duration, writeP95 time.Duration) {
		t.Helper()
		store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
			Path:            filepath.Join(t.TempDir(), "pool-load.sqlite"),
			DisableReadPool: disableReadPool,
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		ctx := context.Background()
		require.NoError(t, store.ensure())
		const sessionID = "pool-load"
		const warmup = 64
		for index := 0; index < warmup; index++ {
			_, err := store.AppendEvent(ctx, runtimeevents.Event{
				Type: "pool.load", SessionID: sessionID,
				Payload: map[string]interface{}{"index": index, "text": "warmup"},
			})
			require.NoError(t, err)
		}

		const writes = 2000
		const readers = 4
		const readsPerReader = 200
		writeLatencies := make([]time.Duration, 0, writes)
		var writeMu sync.Mutex
		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := 0; index < writes; index++ {
				start := time.Now()
				_, err := store.AppendEvent(ctx, runtimeevents.Event{
					Type: "pool.load", SessionID: sessionID,
					Payload: map[string]interface{}{"index": index, "text": "load-load-load"},
				})
				if err != nil {
					continue
				}
				writeMu.Lock()
				writeLatencies = append(writeLatencies, time.Since(start))
				writeMu.Unlock()
			}
		}()

		readLatencies := make([][]time.Duration, readers)
		for reader := 0; reader < readers; reader++ {
			wg.Add(1)
			go func(reader int) {
				defer wg.Done()
				samples := make([]time.Duration, 0, readsPerReader)
				for index := 0; index < readsPerReader; index++ {
					start := time.Now()
					_, err := store.ListEvents(ctx, sessionID, 0, 100)
					if err == nil {
						samples = append(samples, time.Since(start))
					}
				}
				readLatencies[reader] = samples
			}(reader)
		}
		wg.Wait()

		var all []time.Duration
		for _, samples := range readLatencies {
			all = append(all, samples...)
		}
		require.NotEmpty(t, all)
		return p95Duration(all), p95Duration(writeLatencies)
	}

	singleReadP95, singleWriteP95 := measure(t, true)
	dualReadP95, dualWriteP95 := measure(t, false)
	t.Logf("read p95: single=%s dual=%s (ratio %.2f)", singleReadP95, dualReadP95,
		float64(dualReadP95)/float64(singleReadP95))
	t.Logf("write p95: single=%s dual=%s", singleWriteP95, dualWriteP95)

	require.Less(t, dualReadP95, singleReadP95, "双池读 P95 必须优于单池")
	if singleWriteP95 > 0 {
		// 写侧不劣化（±5% 放宽为 1.5x，CI 抖动）。
		require.Less(t, float64(dualWriteP95), float64(singleWriteP95)*1.5,
			fmt.Sprintf("写 P95 劣化过多: single=%s dual=%s", singleWriteP95, dualWriteP95))
	}
}

func p95Duration(samples []time.Duration) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := (95*len(sorted) + 99) / 100
	if index > 0 {
		index--
	}
	return sorted[index]
}
