package knowledge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 本文件把 04 §5 Phase 5 的**验收门槛**变成可复现的自动化用例：
//
//  1. agent 连续编辑 100 次后，增量索引与全量重建结果 diff = 0
//     → TestAcceptanceIncrementalMatchesFullAfter100Edits
//  2. 外部 git checkout 后 stale 判定正确率 100%（样本 ≥ 50 次）
//     → TestAcceptanceCheckoutStaleDetection50Samples
//  3. 索引写不阻塞会话读：读延迟/锁等待 p95 < 50ms、锁重试失败率 < 0.1%
//     → TestAcceptanceReadLatencyAndLockWaitUnderWriteLoad
//  4. 双实例同开同一 workspace → 后到者只读降级、无锁死：由既有
//     `TestActivateOwnerThenReader`（activation_test.go）与 owner 仲裁用例覆盖，
//     本文件不重复。
//
// 口径说明：1 与 2 是确定性判定（不依赖机器性能），3 是**本机进程内**近似
// （单 workspace、写连接 + 只读连接并发），阈值取文档门槛原值。

// acceptanceEditSource 是每轮编辑都改名/改体的入口文件：符号替换、跨文件引用
// 重绑定、FTS 行随删随增都被覆盖。
const acceptanceEditSource = `package demo

// Target 是本轮版本的入口。
func Target%03d() int {
	return helper%03d()
}

func helper%03d() int { return %d }
`

// acceptanceCallerSource 是对入口的跨文件调用方。
const acceptanceCallerSource = `package demo

func Caller%03d() int {
	return Target%03d()
}
`

// TestAcceptanceIncrementalMatchesFullAfter100Edits 对应门槛 1：100 次连续编辑
// （改内容 / 跨文件引用 / 加文件 / 删文件 / 重命名）全部经变更队列 → 定向增量
// 落地后，与"同一终态磁盘状态的一次全量重建"做 **ID 级**逐行对照。
func TestAcceptanceIncrementalMatchesFullAfter100Edits(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/a.go", fmt.Sprintf(acceptanceEditSource, 0, 0, 0, 0))
	writeTree(t, root, "demo/b.go", fmt.Sprintf(acceptanceCallerSource, 0, 0))
	writeTree(t, root, "demo/keep.go", "package demo\n\nfunc Keep() int { return 1 }\n")

	cfg := indexPathsConfig(root)
	incStore := newTestStore(t)
	if _, err := RunIndex(ctx, incStore, cfg); err != nil {
		t.Fatalf("RunIndex(initial): %v", err)
	}
	queue := NewChangeQueue(cfg, incStore, ChangeQueueOptions{Debounce: 5 * time.Millisecond})
	t.Cleanup(queue.Close)

	var generated []string
	const edits = 100
	for i := 1; i <= edits; i++ {
		var rel string
		switch i % 5 {
		case 1: // 改入口：符号改名 + 函数体变化
			rel = "demo/a.go"
			writeTree(t, root, rel, fmt.Sprintf(acceptanceEditSource, i, i, i, i))
		case 2: // 改调用方：跨文件引用指向新入口
			rel = "demo/b.go"
			writeTree(t, root, rel, fmt.Sprintf(acceptanceCallerSource, i, i))
		case 3: // 加文件
			rel = fmt.Sprintf("demo/gen_%03d.go", i)
			writeTree(t, root, rel, fmt.Sprintf("package demo\n\nfunc Gen%03d() int { return %d }\n", i, i))
			generated = append(generated, rel)
		case 4: // 删先前生成的文件
			if len(generated) == 0 {
				rel = "demo/keep.go"
				writeTree(t, root, rel, fmt.Sprintf("package demo\n\nfunc Keep() int { return %d }\n", i))
				break
			}
			rel = generated[0]
			generated = generated[1:]
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				t.Fatalf("remove %s: %v", rel, err)
			}
		case 0: // 重命名先前生成的文件（旧路径软删除 + 新路径入库）
			if len(generated) == 0 {
				rel = "demo/keep.go"
				writeTree(t, root, rel, fmt.Sprintf("package demo\n\nfunc Keep() int { return %d }\n", i))
				break
			}
			old := generated[0]
			generated = generated[1:]
			newRel := fmt.Sprintf("demo/moved_%03d.go", i)
			if err := os.Rename(filepath.Join(root, filepath.FromSlash(old)), filepath.Join(root, filepath.FromSlash(newRel))); err != nil {
				t.Fatalf("rename %s -> %s: %v", old, newRel, err)
			}
			queue.Mark(old)
			generated = append(generated, newRel)
			rel = newRel
		}
		queue.Mark(rel)
		if _, err := queue.Flush(ctx); err != nil {
			t.Fatalf("Flush(#%d, %s): %v", i, rel, err)
		}
	}
	require.Zero(t, queue.Pending(), "100 次编辑后队列必须已排空")

	// 终态全量重建（同一磁盘状态、独立 store；符号 ID 不含 workspace id，可比）。
	fullStore := newTestStore(t)
	if _, err := RunIndex(ctx, fullStore, cfg); err != nil {
		t.Fatalf("RunIndex(final full): %v", err)
	}
	incProj, err := projectWorkspaceIndex(ctx, incStore, root)
	if err != nil {
		t.Fatalf("project(incremental): %v", err)
	}
	fullProj, err := projectWorkspaceIndex(ctx, fullStore, root)
	if err != nil {
		t.Fatalf("project(full): %v", err)
	}
	if diff := diffProjections(incProj, fullProj); diff != "" {
		t.Fatalf("%d 次编辑后增量与全量不等价:\n%s", edits, diff)
	}
}

// TestAcceptanceCheckoutStaleDetection50Samples 对应门槛 2：外部 `git checkout --`
// 绕过 edit hook 写盘后，"这条记忆还能不能复用"的判定必须**立即**变保守
// （`#pending` 未稳定标记），且在索引追上后回到稳定（无误报）。50 次采样要求
// 检出率 100%、误报 0。
func TestAcceptanceCheckoutStaleDetection50Samples(t *testing.T) {
	ctx := context.Background()
	root := gitRepoWithDemo(t)
	layer := activateChangeLayerForTest(t, root)
	cfg := layerConfigForTest(root)
	if _, err := RunIndex(ctx, layer.Store(), cfg); err != nil {
		t.Fatalf("RunIndex(initial): %v", err)
	}

	const samples = 50
	detected := 0
	for i := 1; i <= samples; i++ {
		// 1) 未提交的工作区改写 → 索引追上 → 判定必须稳定（git 侧的持续
		//    modified 不得造成误报，Fresh 过滤负责这一条）。
		writeTree(t, root, "demo/a.go", fmt.Sprintf("package demo\n\nfunc Alpha() int { return %d }\n", i+1))
		if _, err := RunIndex(ctx, layer.Store(), cfg); err != nil {
			t.Fatalf("RunIndex(#%d): %v", i, err)
		}
		stable, err := layer.ObserveVersion(ctx, 0, time.Now())
		require.NoError(t, err)
		require.NotContains(t, stable.Version, "#pending",
			"索引已追上时不得报未稳定（第 %d 次采样）", i)

		// 2) git checkout -- 还原到 HEAD（外部写盘）→ 判定必须立即保守。
		gitRun(t, root, "checkout", "--", "demo/a.go")
		after, err := layer.ObserveVersion(ctx, 0, time.Now())
		require.NoError(t, err)
		if strings.Contains(after.Version, "#pending") {
			detected++
		}
	}
	require.Equal(t, samples, detected,
		"checkout 后 stale 判定检出率必须 100%%（检出 %d/%d）", detected, samples)
}

// TestAcceptanceReadLatencyAndLockWaitUnderWriteLoad 对应门槛 3：写侧持续跑
// 定向增量（每条都经过 execWrite → 锁等待采样）时，会话读（FindSymbols/Stats）
// 的 p95 延迟与写路径锁等待 p95 都必须 < 50ms，且锁重试失败 0 次。
func TestAcceptanceReadLatencyAndLockWaitUnderWriteLoad(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	const files = 12
	for i := 0; i < files; i++ {
		writeTree(t, root, fmt.Sprintf("demo/f%02d.go", i),
			fmt.Sprintf("package demo\n\nfunc F%02d() int { return %d }\n", i, i))
	}
	cfg := indexPathsConfig(root)
	writeStore := newTestStore(t)
	if _, err := RunIndex(ctx, writeStore, cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}
	wsID, err := writeStore.EnsureWorkspace(ctx, Workspace{RootPath: root})
	require.NoError(t, err)
	readStore, err := OpenStore(ctx, writeStore.(*sqliteStore).path, true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = readStore.Close() })

	stop := make(chan struct{})
	writeErr := make(chan error, 1)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				writeErr <- nil
				return
			default:
			}
			rel := fmt.Sprintf("demo/f%02d.go", i%files)
			content := fmt.Sprintf("package demo\n\nfunc F%02d() int { return %d }\n", i%files, i+100)
			if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
				writeErr <- err
				return
			}
			if _, err := IndexPaths(ctx, writeStore, cfg, []string{rel}); err != nil {
				writeErr <- err
				return
			}
		}
	}()

	var latencies []time.Duration
	deadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(deadline) {
		start := time.Now()
		if _, err := readStore.FindSymbols(ctx, SymbolQuery{Name: "F00", Limit: 20}); err != nil {
			t.Fatalf("会话读失败: %v", err)
		}
		if _, err := readStore.Stats(ctx, wsID); err != nil {
			t.Fatalf("Stats: %v", err)
		}
		latencies = append(latencies, time.Since(start))
	}
	close(stop)
	require.NoError(t, <-writeErr)

	readP50 := durationPercentile(latencies, 0.50)
	readP95 := durationPercentile(latencies, 0.95)
	lock := writeStore.(*sqliteStore).lockWait.snapshot()
	t.Logf("读延迟 p50=%v p95=%v（%d 次采样）；写侧锁等待 p95=%.3fms max=%.3fms（%d 样本，重试失败 %d）",
		readP50, readP95, len(latencies), lock.P95MS, lock.MaxMS, lock.Samples, lock.RetryFailures)

	require.Greater(t, len(latencies), 50, "读侧采样过少，测量不成立")
	require.Less(t, readP95, 50*time.Millisecond, "索引写不得阻塞会话读（读延迟 p95 门槛 50ms）")
	require.Less(t, lock.P95MS, 50.0, "锁等待 p95 门槛 50ms")
	require.Zero(t, lock.RetryFailures, "锁重试失败率必须 < 0.1%（本用例要求 0 次）")
}

// durationPercentile 返回升序排列后的近似分位（最近样本窗口语义与 LockWaitStats 一致）。
func durationPercentile(samples []time.Duration, q float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	idx := int(q * float64(len(ordered)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(ordered) {
		idx = len(ordered) - 1
	}
	return ordered[idx]
}
