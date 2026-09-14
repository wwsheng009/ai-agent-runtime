package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsInsideGitWorkTree(t *testing.T) {
	t.Run("plain dir", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "plain", "nested")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir plain dir: %v", err)
		}
		if isInsideGitWorkTree(dir) {
			t.Fatalf("%q 不应被判定为位于 git 工作树内", dir)
		}
	})

	t.Run("git dir and nested", func(t *testing.T) {
		repo := filepath.Join(t.TempDir(), "repo")
		nested := filepath.Join(repo, "backend", "internal")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatalf("mkdir nested dir: %v", err)
		}
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatalf("mkdir .git: %v", err)
		}
		if !isInsideGitWorkTree(repo) {
			t.Fatalf("仓库根 %q 应被判定为 git 工作树", repo)
		}
		if !isInsideGitWorkTree(nested) {
			t.Fatalf("仓库子目录 %q 应被判定为 git 工作树", nested)
		}
	})

	t.Run("gitfile for worktree", func(t *testing.T) {
		worktree := filepath.Join(t.TempDir(), "worktree")
		nested := filepath.Join(worktree, "src")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatalf("mkdir worktree dir: %v", err)
		}
		gitfile := filepath.Join(worktree, ".git")
		if err := os.WriteFile(gitfile, []byte("gitdir: ../main/.git/worktrees/wt\n"), 0o644); err != nil {
			t.Fatalf("write gitfile: %v", err)
		}
		if !isInsideGitWorkTree(worktree) {
			t.Fatalf("gitfile 工作树 %q 应被判定为 git 工作树", worktree)
		}
		if !isInsideGitWorkTree(nested) {
			t.Fatalf("gitfile 工作树子目录 %q 应被判定为 git 工作树", nested)
		}
	})

	t.Run("empty dir", func(t *testing.T) {
		if isInsideGitWorkTree("") {
			t.Fatal("空目录不应被判定为 git 工作树")
		}
	})
}

// 缓存语义：负结果进短 TTL 缓存（TTL 内 `git init` 不生效，过期后重新探测生效）；
// 正结果在复核节流窗口内零开销复用，窗口过后才复核工作树根的 `.git` 是否仍在。
func TestIsInsideGitWorkTree_CacheFollowsRepoLifecycle(t *testing.T) {
	clearGitWorkTreeCache()
	t.Cleanup(clearGitWorkTreeCache)

	current := time.Now()
	restoreNow := gitWorkTreeNow
	gitWorkTreeNow = func() time.Time { return current }
	t.Cleanup(func() { gitWorkTreeNow = restoreNow })

	repo := filepath.Join(t.TempDir(), "workspace")
	nested := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	key := normalizeGitProbeDir(nested)

	// 1) 无 .git：负结果写入短 TTL 缓存。
	if isInsideGitWorkTree(nested) {
		t.Fatal("尚无 .git 时不应判定为 git 工作树")
	}
	entry, ok := loadGitWorkTreeEntry(key)
	if !ok || entry.root != "" {
		t.Fatalf("负结果应写入短 TTL 缓存，got %+v ok=%v", entry, ok)
	}
	if !entry.expiresAt.After(current) {
		t.Fatalf("负结果应带未来过期时刻，expiresAt=%v now=%v", entry.expiresAt, current)
	}

	// 2) TTL 内 git init：仍复用负结果，避免每个文件都重新向上探测。
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if isInsideGitWorkTree(nested) {
		t.Fatal("负结果 TTL 内应继续复用缓存")
	}

	// 3) TTL 过期后重新探测：git init 生效并转为正结果缓存。
	current = current.Add(gitWorkTreeNegativeTTL + time.Second)
	if !isInsideGitWorkTree(nested) {
		t.Fatal("负结果 TTL 过期后应重新探测到 git 工作树")
	}
	entry, ok = loadGitWorkTreeEntry(key)
	if !ok || entry.root != repo {
		t.Fatalf("正结果应缓存工作树根 %q，got %+v ok=%v", repo, entry, ok)
	}

	// 4) 复核窗口过后 .git 仍在：复核成功并刷新复核时刻。
	current = current.Add(gitWorkTreeReverifyInterval + time.Second)
	if !isInsideGitWorkTree(nested) {
		t.Fatal("复核成功应继续判定为 git 工作树")
	}
	entry, ok = loadGitWorkTreeEntry(key)
	if !ok || entry.verifiedAt.Before(current) {
		t.Fatalf("复核成功应刷新 verifiedAt，got %+v ok=%v", entry, ok)
	}

	// 5) 复核窗口内仓库消失：沿用旧判定（这是节流的固有代价）。
	if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
		t.Fatalf("remove .git: %v", err)
	}
	if !isInsideGitWorkTree(nested) {
		t.Fatal("复核窗口内应继续复用正结果")
	}

	// 6) 复核窗口过后复核失败：缓存失效并回退为完整探测，得到负结果。
	current = current.Add(gitWorkTreeReverifyInterval + time.Second)
	if isInsideGitWorkTree(nested) {
		t.Fatal(".git 删除后不应再判定为 git 工作树")
	}
	entry, ok = loadGitWorkTreeEntry(key)
	if !ok || entry.root != "" {
		t.Fatalf("复核失败后应回退为负结果缓存，got %+v ok=%v", entry, ok)
	}
}

// 降频策略的核心保证：正结果在复核窗口内命中缓存时不产生任何文件系统调用，
// 超窗后恰好一次 os.Stat 复核（统计 gitWorkTreeStat 调用次数）。
func TestIsInsideGitWorkTree_HotPathSkipsStatWithinReverifyWindow(t *testing.T) {
	clearGitWorkTreeCache()
	t.Cleanup(clearGitWorkTreeCache)

	current := time.Now()
	restoreNow := gitWorkTreeNow
	gitWorkTreeNow = func() time.Time { return current }
	t.Cleanup(func() { gitWorkTreeNow = restoreNow })

	statCalls := 0
	restoreStat := gitWorkTreeStat
	gitWorkTreeStat = func(name string) (os.FileInfo, error) {
		statCalls++
		return restoreStat(name)
	}
	t.Cleanup(func() { gitWorkTreeStat = restoreStat })

	repo := filepath.Join(t.TempDir(), "repo")
	nested := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	if !isInsideGitWorkTree(nested) {
		t.Fatal("首次探测应判定为 git 工作树")
	}
	if statCalls == 0 {
		t.Fatal("首次探测应产生文件系统调用")
	}

	statCalls = 0
	for i := 0; i < 3; i++ {
		if !isInsideGitWorkTree(nested) {
			t.Fatal("复核窗口内应复用正结果")
		}
	}
	if statCalls != 0 {
		t.Fatalf("复核窗口内不应产生文件系统调用，got %d", statCalls)
	}

	current = current.Add(gitWorkTreeReverifyInterval + time.Second)
	statCalls = 0
	if !isInsideGitWorkTree(nested) {
		t.Fatal("复核窗口过后应复核成功")
	}
	if statCalls != 1 {
		t.Fatalf("复核应恰好产生 1 次文件系统调用，got %d", statCalls)
	}
}

func benchmarkGitWorkTreeFixture(b *testing.B, depth int) string {
	b.Helper()
	root := b.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		b.Fatalf("mkdir .git: %v", err)
	}
	deep := root
	for i := 0; i < depth; i++ {
		deep = filepath.Join(deep, fmt.Sprintf("level%d", i))
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		b.Fatalf("mkdir deep dir: %v", err)
	}
	return deep
}

// 无缓存基线：每次调用都向上逐级探测 12 层目录。
func BenchmarkFindGitWorkTreeRootDeep(b *testing.B) {
	deep := benchmarkGitWorkTreeFixture(b, 12)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if findGitWorkTreeRoot(deep) == "" {
			b.Fatal("fixture must be inside a git work tree")
		}
	}
}

// freezeGitWorkTreeClock 固定缓存时钟，使基准不受 TTL/复核窗口到期影响。
func freezeGitWorkTreeClock(b *testing.B) time.Time {
	b.Helper()
	now := time.Now()
	restore := gitWorkTreeNow
	gitWorkTreeNow = func() time.Time { return now }
	b.Cleanup(func() { gitWorkTreeNow = restore })
	return now
}

// 缓存热路径：正结果在复核窗口内命中，零系统调用。
func BenchmarkIsInsideGitWorkTreeCached(b *testing.B) {
	deep := benchmarkGitWorkTreeFixture(b, 12)
	freezeGitWorkTreeClock(b)
	if !isInsideGitWorkTree(deep) {
		b.Fatal("fixture must be inside a git work tree")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !isInsideGitWorkTree(deep) {
			b.Fatal("cached probe must stay true")
		}
	}
}

// 复核路径：条目超出复核窗口后的一次 os.Stat 复核 + 缓存续期。
func BenchmarkIsInsideGitWorkTreeReverify(b *testing.B) {
	deep := benchmarkGitWorkTreeFixture(b, 12)
	now := freezeGitWorkTreeClock(b)
	if !isInsideGitWorkTree(deep) {
		b.Fatal("fixture must be inside a git work tree")
	}
	key := normalizeGitProbeDir(deep)
	root := findGitWorkTreeRoot(deep)
	stale := gitWorkTreeEntry{root: root, verifiedAt: now.Add(-gitWorkTreeReverifyInterval - time.Second)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 每次把条目拨回“早已超窗”，强制走一次 os.Stat 复核。
		gitWorkTreeCache.Store(key, stale)
		if !isInsideGitWorkTree(deep) {
			b.Fatal("reverify must stay true")
		}
	}
}

// 负结果热路径：非 git 工程同一目录在短 TTL 内重复探测，零系统调用。
func BenchmarkIsInsideGitWorkTreeNegativeCached(b *testing.B) {
	deep := filepath.Join(b.TempDir(), "not", "a", "git", "repo")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		b.Fatalf("mkdir: %v", err)
	}
	clearGitWorkTreeCache()
	freezeGitWorkTreeClock(b)
	if isInsideGitWorkTree(deep) {
		b.Skip("临时目录位于 git 工作树内，无法构造负结果场景")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if isInsideGitWorkTree(deep) {
			b.Fatal("non-git dir must stay negative")
		}
	}
}
