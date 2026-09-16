package gitbrowse

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatusGroupsAndCounts(t *testing.T) {
	root := newTestRepo(t)
	// 已暂存：a.txt 追加一行；staged.txt 新增。
	writeFile(t, filepath.Join(root, "a.txt"), "line1\nline2\nline3\nline4\n")
	writeFile(t, filepath.Join(root, "staged.txt"), "new file\n")
	runGit(t, root, "add", "a.txt", "staged.txt")
	// 未暂存：a.txt 再改一行；untracked.txt 未跟踪。
	writeFile(t, filepath.Join(root, "a.txt"), "line1\nCHANGED\nline3\nline4\n")
	writeFile(t, filepath.Join(root, "untracked.txt"), "u\n")

	service := newTestService(t, root, Limits{})
	result, err := service.Status(context.Background(), RepoRequest{Scope: "workspace:wd"})
	require.NoError(t, err)

	require.False(t, result.Clean)
	require.Equal(t, root, result.Repo.Root)
	require.Equal(t, "main", result.Repo.Branch)
	require.False(t, result.Repo.Detached)
	require.Len(t, result.Repo.Head, 7)
	require.Len(t, result.Repo.HeadFull, 40)
	require.Empty(t, result.Repo.Upstream)
	require.False(t, result.Repo.IsBare)
	require.Empty(t, result.Warnings)
	require.Positive(t, result.GeneratedAt)

	require.Len(t, result.Staged, 2)
	require.Equal(t, "a.txt", result.Staged[0].Path)
	require.Equal(t, "M", result.Staged[0].Status)
	// XY 保留 porcelain 原始两位：工作区仍有改动，因此是 MM。
	require.Equal(t, "MM", result.Staged[0].XY)
	require.Equal(t, 1, result.Staged[0].Insertions)
	require.Equal(t, 0, result.Staged[0].Deletions)
	require.False(t, result.Staged[0].Binary)
	require.Equal(t, "staged.txt", result.Staged[1].Path)
	require.Equal(t, "A", result.Staged[1].Status)
	require.Equal(t, "A.", result.Staged[1].XY)
	require.Equal(t, 1, result.Staged[1].Insertions)

	require.Len(t, result.Unstaged, 1)
	require.Equal(t, "a.txt", result.Unstaged[0].Path)
	require.Equal(t, "M", result.Unstaged[0].Status)
	require.Equal(t, "MM", result.Unstaged[0].XY)
	require.Equal(t, 1, result.Unstaged[0].Insertions)
	require.Equal(t, 1, result.Unstaged[0].Deletions)

	require.Len(t, result.Untracked, 1)
	require.Equal(t, "untracked.txt", result.Untracked[0].Path)
	require.Equal(t, "U", result.Untracked[0].Status)
	require.Equal(t, "?", result.Untracked[0].XY)
	// 未跟踪文件不在 numstat 里：必须读文件统计，不能退化成 +0 −0。
	require.Equal(t, 1, result.Untracked[0].Insertions)
	require.Equal(t, 0, result.Untracked[0].Deletions)
	require.False(t, result.Untracked[0].Binary)

	require.Empty(t, result.Conflicts)
	require.Empty(t, result.Renames)
}

// 未跟踪文件的行数口径与 diff 一致：末尾无换行不产生空行，二进制按标记而非 0/0 伪装。
func TestStatusUntrackedCounts(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "multi.txt"), "one\ntwo\nthree\n")
	writeFile(t, filepath.Join(root, "no-newline.txt"), "one\ntwo")
	writeFile(t, filepath.Join(root, "untracked-bin.dat"), "a\x00b\x00c")

	service := newTestService(t, root, Limits{})
	result, err := service.Status(context.Background(), RepoRequest{Scope: "cwd"})
	require.NoError(t, err)
	require.Len(t, result.Untracked, 3)

	byPath := make(map[string]ChangeEntry, len(result.Untracked))
	for _, entry := range result.Untracked {
		byPath[entry.Path] = entry
	}
	require.Equal(t, 3, byPath["multi.txt"].Insertions)
	require.Equal(t, 0, byPath["multi.txt"].Deletions)
	require.False(t, byPath["multi.txt"].Binary)
	// 无尾换行：2 行（不是 1 行，也不是 3 行）。
	require.Equal(t, 2, byPath["no-newline.txt"].Insertions)
	require.Equal(t, 0, byPath["no-newline.txt"].Deletions)
	require.True(t, byPath["untracked-bin.dat"].Binary)
	require.Equal(t, 0, byPath["untracked-bin.dat"].Insertions)
	require.Empty(t, result.Warnings)
}

// 读不到结论时用 -1 表示未知（不用 0 伪装），并给出可解释 warning。
func TestStatusUntrackedCountsUnknownWhenOversized(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "huge.txt"), strings.Repeat("a", 8192))

	service := newTestService(t, root, Limits{MaxOutputBytes: 4096})
	result, err := service.Status(context.Background(), RepoRequest{Scope: "cwd"})
	require.NoError(t, err)

	require.Len(t, result.Untracked, 1)
	require.Equal(t, "huge.txt", result.Untracked[0].Path)
	require.Equal(t, -1, result.Untracked[0].Insertions)
	require.Equal(t, -1, result.Untracked[0].Deletions)
	require.Contains(t, result.Warnings,
		"1 untracked file(s) could not be counted (unreadable, non-regular, oversized or beyond the scan budget)")
}

// 总扫描预算耗尽后，剩余未跟踪文件按未知（-1）处理并给出 warning；预算内的文件照常统计。
// 目的是让「一次列表请求」不会退化成无上限的磁盘读取。
func TestStatusUntrackedCountsUnknownWhenScanBudgetExhausted(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "a-big.txt"), "one\ntwo\nthree\n") // 14 字节
	writeFile(t, filepath.Join(root, "b-big.txt"), "one\ntwo\nthree\n") // 14 字节

	service := newTestService(t, root, Limits{MaxUntrackedScanBytes: 20})
	result, err := service.Status(context.Background(), RepoRequest{Scope: "cwd"})
	require.NoError(t, err)
	require.Len(t, result.Untracked, 2)

	byPath := make(map[string]ChangeEntry, len(result.Untracked))
	for _, entry := range result.Untracked {
		byPath[entry.Path] = entry
	}
	require.Equal(t, 3, byPath["a-big.txt"].Insertions)
	require.Equal(t, -1, byPath["b-big.txt"].Insertions)
	require.Equal(t, -1, byPath["b-big.txt"].Deletions)
	require.Contains(t, result.Warnings,
		"1 untracked file(s) could not be counted (unreadable, non-regular, oversized or beyond the scan budget)")
}

func TestStatusCleanRepository(t *testing.T) {
	root := newTestRepo(t)
	service := newTestService(t, root, Limits{})
	result, err := service.Status(context.Background(), RepoRequest{Scope: "cwd"})
	require.NoError(t, err)
	require.True(t, result.Clean)
	require.Empty(t, result.Staged)
	require.Empty(t, result.Unstaged)
	require.Empty(t, result.Untracked)
}

func TestStatusRenameDetected(t *testing.T) {
	root := newTestRepo(t)
	runGit(t, root, "mv", "a.txt", "renamed.txt")

	service := newTestService(t, root, Limits{})
	result, err := service.Status(context.Background(), RepoRequest{Scope: "cwd"})
	require.NoError(t, err)

	require.Len(t, result.Renames, 1)
	require.Equal(t, RenameEntry{From: "a.txt", To: "renamed.txt", Status: "R"}, result.Renames[0])
	require.Len(t, result.Staged, 1)
	require.Equal(t, "renamed.txt", result.Staged[0].Path)
	require.Equal(t, "R", result.Staged[0].Status)
	require.Equal(t, "a.txt", result.Staged[0].From)
	require.Empty(t, result.Unstaged)
}

// 二进制文件的 numstat 是 `-\t-`，必须体现为 binary=true（不是 0/0）。
func TestStatusBinaryFlagFromNumstat(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, filepath.Join(root, "bin.dat"), string([]byte{9, 8, 7, 0, 255, 1}))
	runGit(t, root, "add", "bin.dat")

	service := newTestService(t, root, Limits{})
	result, err := service.Status(context.Background(), RepoRequest{Scope: "cwd"})
	require.NoError(t, err)

	require.Len(t, result.Staged, 1)
	require.Equal(t, "bin.dat", result.Staged[0].Path)
	require.True(t, result.Staged[0].Binary)
	require.Equal(t, 0, result.Staged[0].Insertions)
	require.Equal(t, 0, result.Staged[0].Deletions)
}

func TestStatusConflicts(t *testing.T) {
	root := newTestRepo(t)
	runGit(t, root, "checkout", "-q", "-b", "feature")
	writeFile(t, filepath.Join(root, "a.txt"), "feature side\n")
	runGit(t, root, "add", "a.txt")
	runGit(t, root, "commit", "-q", "-m", "feature change")
	runGit(t, root, "checkout", "-q", "main")
	writeFile(t, filepath.Join(root, "a.txt"), "main side\n")
	runGit(t, root, "add", "a.txt")
	runGit(t, root, "commit", "-q", "-m", "main change")

	// merge 冲突会以非零退出结束，这里允许失败。
	merge := exec.Command("git", "-C", root, "merge", "feature")
	require.Error(t, merge.Run())

	service := newTestService(t, root, Limits{})
	result, err := service.Status(context.Background(), RepoRequest{Scope: "cwd"})
	require.NoError(t, err)

	require.Len(t, result.Conflicts, 1)
	require.Equal(t, "a.txt", result.Conflicts[0].Path)
	require.Equal(t, "!", result.Conflicts[0].Status)
	require.False(t, result.Clean)
}

// porcelain v2 的未知/未请求记录类型只记 warning 并跳过，不猜测语义（§5.5）。
func TestParsePorcelainV2WarningsAndHeaders(t *testing.T) {
	raw := "# branch.oid 0123456789abcdef0123456789abcdef01234567\x00" +
		"# branch.head (detached)\x00" +
		"# branch.ab +2 -3\x00" +
		"1 M. N... 100644 100644 100644 aaaa bbbb a.txt\x00" +
		"? untracked.txt\x00" +
		"! ignored.txt\x00" +
		"S bogus record\x00" +
		"2 R. N... 100644 100644 100644 aaaa bbbb R100 new path.txt\x00old path.txt\x00"

	result, err := parsePorcelainV2([]byte(raw))
	require.NoError(t, err)

	require.True(t, result.Repo.Detached)
	require.Equal(t, "0123456", result.Repo.Head)
	require.Equal(t, 2, result.Repo.Ahead)
	require.Equal(t, 3, result.Repo.Behind)
	require.Len(t, result.Staged, 2)
	require.Equal(t, "new path.txt", result.Staged[1].Path)
	require.Equal(t, "old path.txt", result.Staged[1].From)
	require.Len(t, result.Untracked, 1)
	require.Equal(t, []RenameEntry{{From: "old path.txt", To: "new path.txt", Status: "R"}}, result.Renames)
	require.Len(t, result.Warnings, 2)
	require.Contains(t, result.Warnings[0], "unrecognized porcelain v2 record (!)")
	require.Contains(t, result.Warnings[1], "unrecognized porcelain v2 record (S)")
}

func TestParsePorcelainV2MalformedOrdinaryRecord(t *testing.T) {
	result, err := parsePorcelainV2([]byte("1 M. N... 100644\x00"))
	require.NoError(t, err)
	require.Empty(t, result.Staged)
	require.Len(t, result.Warnings, 1)
	require.Contains(t, result.Warnings[0], "unrecognized porcelain v2 record (1)")
}

// numstat 的 -z 输出：重命名条目下路径是独立的 NUL 字段（旧→新），两个键都要登记。
func TestParseNumstatRenameAndBinary(t *testing.T) {
	raw := "2\t2\ta.txt\x00-\t-\tbin.dat\x000\t0\t\x00old name\x00new name\x00"
	entries := parseNumstat([]byte(raw))

	require.Equal(t, numstatEntry{insertions: 2, deletions: 2}, entries["a.txt"])
	require.Equal(t, numstatEntry{binary: true}, entries["bin.dat"])
	require.Equal(t, numstatEntry{}, entries["old name"])
	require.Equal(t, numstatEntry{}, entries["new name"])
	require.NotContains(t, entries, "")
}
