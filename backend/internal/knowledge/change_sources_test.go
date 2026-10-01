package knowledge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Phase 5 变更源 2 测试：git 校正源（HEAD 移动 + 工作树状态）、stat 校正源
// （外部写盘 / `git checkout --` 还原）、Layer 级校正（队列 + 版本判定点）。

// gitRun 在 dir 里执行 git；环境隔离（不读用户/系统配置，避免钩子与模板干扰）。
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=", "GIT_CONFIG_SYSTEM=", "GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// gitRepoWithDemo 初始化仓库并提交 demo/a.go + demo/b.go 的初始版本。
func gitRepoWithDemo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	writeTree(t, root, "demo/a.go", "package demo\n\nfunc Alpha() int { return 1 }\n")
	writeTree(t, root, "demo/b.go", "package demo\n\nfunc Beta() int { return 1 }\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-qm", "init")
	return root
}

// waitForExternalSync 轮询直到条件成立（校正 + 索引是异步的，测试必须等）。
// probe 返回 (是否成立, 供超时诊断的最后状态)。
func waitForExternalSync(t *testing.T, what string, probe func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	last := ""
	for {
		ok, state := probe()
		if state != "" {
			last = state
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("超时等待: %s（最后观测: %s）", what, last)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestGitChangeSourceDetectsStatusAndHeadMove(t *testing.T) {
	ctx := context.Background()
	root := gitRepoWithDemo(t)

	source := NewGitChangeSource(root, nil)
	first, err := source.Detect(ctx)
	require.NoError(t, err)
	require.False(t, first.Skipped, "git 仓库不得跳过: %s", first.SkipReason)
	require.NotEmpty(t, first.Head)
	require.False(t, first.HeadMoved, "首次检测没有上一次 HEAD，不算移动")
	require.Empty(t, first.Paths, "干净工作树不得报变化")

	// 修改 + 未跟踪新文件 + 删除：三个维度都要被发现。
	writeTree(t, root, "demo/a.go", "package demo\n\nfunc Alpha() int { return 2 }\n")
	writeTree(t, root, "demo/c.go", "package demo\n\nfunc Gamma() int { return 1 }\n")
	require.NoError(t, os.Remove(filepath.Join(root, "demo", "b.go")))

	second, err := source.Detect(ctx)
	require.NoError(t, err)
	require.False(t, second.HeadMoved)
	require.Equal(t, []string{"demo/a.go", "demo/b.go", "demo/c.go"}, second.Paths,
		"修改/删除/未跟踪新文件都必须被发现")

	// HEAD 移动：新分支改内容再切回，工作树匹配新 HEAD、status 干净，
	// 只有 HEAD 差异能发现。
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-qm", "work")
	gitRun(t, root, "checkout", "-q", "-b", "feature")
	writeTree(t, root, "demo/a.go", "package demo\n\nfunc Alpha() int { return 3 }\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-qm", "feature")
	gitRun(t, root, "checkout", "-q", "-")

	third, err := source.Detect(ctx)
	require.NoError(t, err)
	require.True(t, third.HeadMoved, "分支切换必须被识别为 HEAD 移动")
	require.Contains(t, third.Paths, "demo/a.go", "HEAD 差异文件必须被发现")
}

func TestGitChangeSourceSkipsNonRepository(t *testing.T) {
	ctx := context.Background()
	source := NewGitChangeSource(t.TempDir(), nil)
	report, err := source.Detect(ctx)
	require.NoError(t, err, "非仓库不是错误")
	require.True(t, report.Skipped)
	require.Empty(t, report.Paths)
}

func TestGitChangeSourceFiltersOutsideWorkspace(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	writeTree(t, root, "other/x.go", "package other\n\nvar X = 1\n")
	writeTree(t, root, "sub/a.go", "package sub\n\nvar A = 1\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-qm", "init")

	// 工作区是仓库子目录：仓库根下的变化只有 sub/ 内的算数。
	workspace := filepath.Join(root, "sub")
	writeTree(t, root, "other/x.go", "package other\n\nvar X = 2\n")
	writeTree(t, root, "sub/a.go", "package sub\n\nvar A = 2\n")

	source := NewGitChangeSource(workspace, nil)
	report, err := source.Detect(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"a.go"}, report.Paths, "工作区外路径必须被丢弃")
}

func TestScanIndexedFileStatsDetectsExternalWrites(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/a.go", "package demo\n\nfunc Alpha() int { return 1 }\n")
	writeTree(t, root, "demo/b.go", "package demo\n\nfunc Beta() int { return 1 }\n")
	writeTree(t, root, "demo/c.go", "package demo\n\nfunc Gamma() int { return 1 }\n")

	store := newTestStore(t)
	cfg := DefaultConfig().WithWorkspace(root)
	if _, err := RunIndex(ctx, store, cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	require.NoError(t, err)

	// 外部改写 a.go（size 与 mtime 都变）、删除 b.go；c.go 不动。
	writeTree(t, root, "demo/a.go", "package demo\n\nfunc Alpha() int { return 1 }\n\nfunc Extra() int { return 2 }\n")
	require.NoError(t, os.Remove(filepath.Join(root, "demo", "b.go")))

	report, err := ScanIndexedFileStats(ctx, store, root, wsID)
	require.NoError(t, err)
	require.Equal(t, 3, report.Scanned)
	require.Equal(t, []string{"demo/a.go"}, report.Changed)
	require.Equal(t, []string{"demo/b.go"}, report.Missing)
	require.Zero(t, report.Errors)
	require.Equal(t, []string{"demo/a.go", "demo/b.go"}, report.ChangedPaths())
}

// Layer 级：外部改写（绕过工具）必须被校正发现、进队列、并让版本判定立即变保守。
func TestLayerSyncExternalChangesFeedsQueueAndVersion(t *testing.T) {
	ctx := context.Background()
	root := gitRepoWithDemo(t)
	layer := activateChangeLayerForTest(t, root)
	if _, err := RunIndex(ctx, layer.Store(), layerConfigForTest(root)); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}

	stable, err := layer.ObserveVersion(ctx, 0, time.Time{})
	require.NoError(t, err)
	require.NotContains(t, stable.Version, externalVersionPendingMarker, "初始版本必须稳定")

	// 外部进程改写（不经过任何工具）：stat 校正负责发现。
	writeTree(t, root, "demo/a.go", "package demo\n\nfunc Renamed() int { return 9 }\n")

	pending, err := layer.ObserveVersion(ctx, 0, time.Time{})
	require.NoError(t, err)
	require.NotEqual(t, stable.Version, pending.Version,
		"外部变更后版本判定必须立即变保守（不等索引追上）")

	waitForExternalSync(t, "索引追上外部变更", func() (bool, string) {
		obs, err := layer.ObserveVersion(ctx, 0, time.Time{})
		if err != nil {
			return false, "err=" + err.Error()
		}
		report, _ := layer.SyncExternalChanges(ctx)
		state := obs.Version + " | git=" + strings.Join(report.GitPaths, ",") +
			" stat=" + strings.Join(report.StatPaths, ",") +
			" missing=" + strings.Join(report.Missing, ",") +
			" marked=" + strconv.Itoa(report.Marked)
		queue := layer.ChangeQueue()
		lastErr := queue.LastError()
		state += " | qgen=" + strconv.FormatUint(queue.Generation(), 10)
		if lastErr != nil {
			state += " qerr=" + lastErr.Error()
		}
		return !strings.Contains(obs.Version, externalVersionPendingMarker) && obs.Version != stable.Version, state
	})
}

// `git checkout -- <file>` 场景：HEAD 没动、status 干净，只有 stat 校正能发现
// （这正是 04 §5 Phase 5 验收门槛"git checkout 后 stale 判定"的核心用例）。
func TestLayerSyncExternalChangesDetectsCheckoutRevert(t *testing.T) {
	ctx := context.Background()
	root := gitRepoWithDemo(t)
	layer := activateChangeLayerForTest(t, root)
	cfg := layerConfigForTest(root)
	if _, err := RunIndex(ctx, layer.Store(), cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}
	original, err := layer.ObserveVersion(ctx, 0, time.Time{})
	require.NoError(t, err)

	// agent 改过文件并已进索引（edit hook 路径）。
	writeTree(t, root, "demo/a.go", "package demo\n\nfunc Modified() int { return 7 }\n")
	layer.MarkChanged(filepath.Join(root, "demo", "a.go"))
	waitForExternalSync(t, "修改后的内容进索引", func() (bool, string) {
		obs, err := layer.ObserveVersion(ctx, 0, time.Time{})
		if err != nil {
			return false, "err=" + err.Error()
		}
		return !strings.Contains(obs.Version, externalVersionPendingMarker) && obs.Version != original.Version, obs.Version
	})
	modified, err := layer.ObserveVersion(ctx, 0, time.Time{})
	require.NoError(t, err)

	// 外部把文件还原回 HEAD：git 侧完全干净。
	gitRun(t, root, "checkout", "--", "demo/a.go")
	status := strings.TrimSpace(gitRun(t, root, "status", "--porcelain", "--", "demo/a.go"))
	require.Empty(t, status, "还原后 demo/a.go 在 git 侧必须干净（这次变化只有 stat 校正能发现）")

	reverted, err := layer.ObserveVersion(ctx, 0, time.Time{})
	require.NoError(t, err)
	require.NotEqual(t, modified.Version, reverted.Version,
		"还原后版本判定必须立即变保守")

	// 索引追上后回到初始内容的稳定版本（内容与 original 相同 → 版本回到 original）。
	waitForExternalSync(t, "索引追上还原", func() (bool, string) {
		obs, err := layer.ObserveVersion(ctx, 0, time.Time{})
		if err != nil {
			return false, "err=" + err.Error()
		}
		return !strings.Contains(obs.Version, externalVersionPendingMarker) && obs.Version == original.Version,
			obs.Version + " (want " + original.Version + ")"
	})
}

// git 侧"持续 modified"（改动未提交）但索引已跟上时，不得产生 #pending：
// 否则版本判定会一直不稳定，旧知识被无谓地判死。
func TestSyncExternalChangesIgnoresGitDirtyButFreshFiles(t *testing.T) {
	ctx := context.Background()
	root := gitRepoWithDemo(t)
	layer := activateChangeLayerForTest(t, root)
	if _, err := RunIndex(ctx, layer.Store(), layerConfigForTest(root)); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}

	// 改文件但不提交：相对 HEAD 永远是 modified（git 侧持续脏）。
	writeTree(t, root, "demo/a.go", "package demo\n\nfunc Alpha() int { return 5 }\n")
	layer.MarkChanged(filepath.Join(root, "demo", "a.go"))

	waitForExternalSync(t, "改动进索引", func() (bool, string) {
		obs, err := layer.ObserveVersion(ctx, 0, time.Time{})
		if err != nil {
			return false, "err=" + err.Error()
		}
		return !strings.Contains(obs.Version, externalVersionPendingMarker), obs.Version
	})

	// 跨过 git 节流窗口（2s）再采样：git 侧会再次看到 modified，但索引已跟上，
	// 不得再产生 pending 标记。
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		obs, err := layer.ObserveVersion(ctx, 0, time.Time{})
		require.NoError(t, err)
		require.NotContains(t, obs.Version, externalVersionPendingMarker,
			"索引已跟上的持续脏文件不得产生 pending 标记")
		time.Sleep(150 * time.Millisecond)
	}
}

// stat 校正每次都跑；git 校正按最小间隔节流（它只补"未跟踪新文件"这一维度）。
func TestSyncExternalChangesThrottlesGitOnly(t *testing.T) {
	ctx := context.Background()
	root := gitRepoWithDemo(t)
	layer := activateChangeLayerForTest(t, root)
	if _, err := RunIndex(ctx, layer.Store(), layerConfigForTest(root)); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}

	first, err := layer.SyncExternalChanges(ctx)
	require.NoError(t, err)
	require.True(t, first.Ran)
	require.False(t, first.GitSkipped, "首次调用 git 校正必须执行")

	second, err := layer.SyncExternalChanges(ctx)
	require.NoError(t, err)
	require.True(t, second.Ran, "stat 校正不受节流影响")
	require.True(t, second.GitSkipped)
	require.Equal(t, "throttled", second.GitSkipReason)

	syncs, _, lastGit := layer.ExternalSyncStats()
	require.GreaterOrEqual(t, syncs, 2)
	require.False(t, lastGit.IsZero())
}

// nil / 未挂载队列时是 no-op（off / reader / 非会话路径的零副作用契约）。
func TestSyncExternalChangesNoopWithoutQueue(t *testing.T) {
	ctx := context.Background()
	var nilLayer *Layer
	report, err := nilLayer.SyncExternalChanges(ctx)
	require.NoError(t, err)
	require.True(t, report.Skipped)
	require.Zero(t, report.Marked)

	root := t.TempDir()
	cfg := DefaultConfig().WithWorkspace(root)
	cfg.Mode = ModeShadow
	owner, err := Activate(ctx, cfg, root, ActivationOptions{SkipInitialIndex: true})
	require.NoError(t, err)
	defer owner.Close()
	reader, err := Activate(ctx, cfg, root, ActivationOptions{SkipInitialIndex: true})
	require.NoError(t, err)
	defer reader.Close()
	require.Equal(t, RoleReader, reader.Role())

	readerReport, err := reader.Layer().SyncExternalChanges(ctx)
	require.NoError(t, err)
	require.True(t, readerReport.Skipped, "reader 没有写权限，校正必须是 no-op")
}

// activateChangeLayerForTest 打开 shadow + owner 的 Layer（跳过初始索引，由用例自己跑）。
func activateChangeLayerForTest(t *testing.T, root string) *Layer {
	t.Helper()
	ctx := context.Background()
	cfg := layerConfigForTest(root)
	act, err := Activate(ctx, cfg, root, ActivationOptions{SkipInitialIndex: true})
	require.NoError(t, err)
	require.NotNil(t, act)
	t.Cleanup(func() { _ = act.Close() })
	require.Equal(t, RoleOwner, act.Role())
	require.NotNil(t, act.Layer())
	return act.Layer()
}

func layerConfigForTest(root string) Config {
	cfg := DefaultConfig().WithWorkspace(root)
	cfg.Mode = ModeShadow
	return cfg
}
