package chat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// P2.13 / M5：快照与并发写的延迟数值压测（分册 §1.3 首行、§4 DoD 1 的数值部分）。
//
// CI 默认跳过（绝对阈值在 CI 上易抖动，DoD 1 已由确定性用例
// `TestSnapshotSessionDoesNotBlockSessionWrites` 承担）；发版或改动快照/存储写路径时手动跑：
//
//	$env:AICLI_RUNTIME_SNAPSHOT_LOAD_TEST=1; go test ./internal/chat -run TestSnapshotSessionConcurrentWriteLatency -v -timeout 900s
//
// 目标：快照循环进行时，会话存储写延迟 P99 增量 ≤ 10ms。
func TestSnapshotSessionConcurrentWriteLatency(t *testing.T) {
	if os.Getenv("AICLI_RUNTIME_SNAPSHOT_LOAD_TEST") != "1" {
		t.Skip("set AICLI_RUNTIME_SNAPSHOT_LOAD_TEST=1 to run the snapshot/write latency load test")
	}

	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("snapshot-load-user")
	require.NoError(t, store.Save(ctx, session))

	const (
		messageBytes = 512
		seedMessages = 4000
		writeSamples = 400
	)
	payload := strings.Repeat("x", messageBytes)
	seedStart := time.Now()
	for i := 0; i < seedMessages; i++ {
		require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage(fmt.Sprintf("%d:%s", i, payload))))
	}
	t.Logf("seed: messages=%d bytes~%d take=%s", seedMessages, seedMessages*(messageBytes+16), time.Since(seedStart).Round(time.Millisecond))

	// 预热：先完成一次快照（打开专用池/建表/首轮 VACUUM INTO 的一次性成本不进基线）。
	warmDestination := filepath.Join(t.TempDir(), "warmup.sqlite")
	require.NoError(t, store.SnapshotSession(ctx, session.ID, warmDestination))

	measureWrites := func() []time.Duration {
		latencies := make([]time.Duration, 0, writeSamples)
		for i := 0; i < writeSamples; i++ {
			begin := time.Now()
			require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage("live:" + payload)))
			latencies = append(latencies, time.Since(begin))
		}
		return latencies
	}

	baseline := measureWrites()

	// 并发段：快照循环 + 同样的写测量。
	snapshotDir := t.TempDir()
	var snapshots, snapshotErrs atomic.Int64
	snapshotDurationTotal := atomic.Int64{}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			destination := filepath.Join(snapshotDir, "loop.sqlite")
			begin := time.Now()
			err := store.SnapshotSession(ctx, session.ID, destination)
			snapshotDurationTotal.Add(int64(time.Since(begin)))
			if err != nil {
				snapshotErrs.Add(1)
			}
			snapshots.Add(1)
			_ = os.Remove(destination)
		}
	}()
	loaded := measureWrites()
	close(stop)
	<-done

	p := func(latencies []time.Duration, quantile float64) time.Duration {
		sorted := append([]time.Duration(nil), latencies...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		index := int(float64(len(sorted)-1) * quantile)
		return sorted[index]
	}
	maxOf := func(latencies []time.Duration) time.Duration {
		return p(latencies, 1.0)
	}

	baselineP99 := p(baseline, 0.99)
	loadedP99 := p(loaded, 0.99)
	delta := loadedP99 - baselineP99
	runs := snapshots.Load()
	avgSnapshot := time.Duration(0)
	if runs > 0 {
		avgSnapshot = time.Duration(snapshotDurationTotal.Load() / runs)
	}
	stats := store.SnapshotStats()

	t.Logf("write latency: baseline p50=%s p99=%s max=%s | loaded p50=%s p99=%s max=%s | delta_p99=%s",
		p(baseline, 0.50).Round(time.Microsecond), baselineP99.Round(time.Microsecond), maxOf(baseline).Round(time.Microsecond),
		p(loaded, 0.50).Round(time.Microsecond), loadedP99.Round(time.Microsecond), maxOf(loaded).Round(time.Microsecond),
		delta.Round(time.Microsecond))
	t.Logf("snapshots: runs=%d errors=%d avg_duration=%s", runs, snapshotErrs.Load(), avgSnapshot.Round(time.Millisecond))
	t.Logf("snapshot stats: total=%d succeeded=%d failed=%d timeouts=%d schema_drift=%d degraded_to_main_pool=%d",
		stats.Total, stats.Succeeded, stats.Failed, stats.Timeouts, stats.SchemaDrift, stats.DegradedToMainPool)

	require.Zero(t, snapshotErrs.Load(), "并发快照不得失败（池/归属权/一致性门禁必须全绿）")
	require.Greater(t, runs, int64(0), "并发段必须至少完成一次快照")
	require.LessOrEqual(t, delta, 10*time.Millisecond,
		"会话存储写 P99 增量必须 ≤10ms（分册 §1.3）：baseline=%s loaded=%s", baselineP99, loadedP99)
}
