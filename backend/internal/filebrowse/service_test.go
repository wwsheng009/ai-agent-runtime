package filebrowse

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
)

// fakeRoots 是测试用的作用域解析器：workspace/session 各一张表。
type fakeRoots struct {
	workspaces map[string]string
	sessions   map[string]string
	listed     []fsscope.WorkspaceRoot
	listErr    error
}

func (f *fakeRoots) WorkspaceRoot(_ context.Context, id string) (string, bool, error) {
	value, ok := f.workspaces[id]
	return value, ok, nil
}

func (f *fakeRoots) SessionRoot(_ context.Context, id string) (string, bool, error) {
	value, ok := f.sessions[id]
	return value, ok, nil
}

func (f *fakeRoots) ListWorkspaceRoots(_ context.Context) ([]fsscope.WorkspaceRoot, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listed, nil
}

func newTestService(t *testing.T, root string) (*Service, *fakeRoots) {
	t.Helper()
	roots := &fakeRoots{
		workspaces: map[string]string{"wd_test": root},
		sessions:   map[string]string{},
		listed:     []fsscope.WorkspaceRoot{{ID: "wd_test", Path: root, Name: "test-root"}},
	}
	service := NewService(Deps{Roots: roots, Cwd: root})
	require.NotNil(t, service)
	return service, roots
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, content, 0o644))
}

func encodeCursorForTest(t *testing.T, payload string) string {
	t.Helper()
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func findRoot(roots []Root, scope string) Root {
	for _, item := range roots {
		if item.Scope == scope {
			return item
		}
	}
	return Root{}
}

func listAll(t *testing.T, service *Service, req ListRequest) []Entry {
	t.Helper()
	var collected []Entry
	cursor := req.Cursor
	for page := 0; page < 50; page++ {
		req.Cursor = cursor
		result, err := service.List(context.Background(), req)
		require.NoError(t, err)
		collected = append(collected, result.Entries...)
		if !result.HasMore {
			require.Empty(t, result.NextCursor, "has_more=false 时不应给 next_cursor")
			return collected
		}
		require.NotEmpty(t, result.NextCursor, "has_more=true 时必须给 next_cursor")
		cursor = result.NextCursor
	}
	t.Fatal("pagination did not terminate")
	return nil
}

func TestListPaginationIsCompleteAndUnique(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 25; index++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("file-%02d.txt", index)), []byte("x"))
	}
	service, _ := newTestService(t, root)
	entries := listAll(t, service, ListRequest{Scope: "workspace:wd_test", Limit: 10})
	require.Len(t, entries, 25)
	seen := map[string]bool{}
	for _, entry := range entries {
		require.False(t, seen[entry.Name], "duplicated entry %s", entry.Name)
		seen[entry.Name] = true
	}
	for index := 0; index < 25; index++ {
		require.True(t, seen[fmt.Sprintf("file-%02d.txt", index)])
	}
}

func TestListCursorInvalidIsRejected(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), []byte("x"))
	service, _ := newTestService(t, root)
	_, err := service.List(context.Background(), ListRequest{Scope: "workspace:wd_test", Cursor: "not-base64!!"})
	require.Error(t, err)
	require.True(t, fsscope.IsCode(err, CodeCursorInvalid))
	require.Equal(t, 400, fsscope.StatusOf(err, 0))

	// 合法 base64 但不是本目录/本排序的游标 → 同样拒绝，不静默回退第一页。
	foreign := `{"v":1,"s":"name_asc","dir":"other","n":"a.txt"}`
	_, err = service.List(context.Background(), ListRequest{Scope: "workspace:wd_test", Cursor: encodeCursorForTest(t, foreign)})
	require.True(t, fsscope.IsCode(err, CodeCursorInvalid))
}

func TestListHidesInternalAndHiddenEntries(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "visible.txt"), []byte("x"))
	writeFile(t, filepath.Join(root, ".hidden.txt"), []byte("x"))
	writeFile(t, filepath.Join(root, uploadDirName, "up_1.part"), []byte("x"))

	service, _ := newTestService(t, root)
	result, err := service.List(context.Background(), ListRequest{Scope: "workspace:wd_test"})
	require.NoError(t, err)
	require.Len(t, result.Entries, 1)
	require.Equal(t, "visible.txt", result.Entries[0].Name)

	withHidden, err := service.List(context.Background(), ListRequest{Scope: "workspace:wd_test", ShowHidden: true})
	require.NoError(t, err)
	names := make([]string, 0, len(withHidden.Entries))
	for _, entry := range withHidden.Entries {
		names = append(names, entry.Name)
	}
	require.Contains(t, names, ".hidden.txt")
	require.NotContains(t, names, uploadDirName, "内部上传目录必须始终隐藏")
}

func TestListSortAndTruncateFlag(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "beta.txt"), []byte("22"))
	writeFile(t, filepath.Join(root, "Alpha.txt"), []byte("1"))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "dir"), 0o755))
	service, _ := newTestService(t, root)

	result, err := service.List(context.Background(), ListRequest{Scope: "workspace:wd_test"})
	require.NoError(t, err)
	require.Equal(t, SortTypeThenName, result.Sort)
	require.Equal(t, "dir", result.Entries[0].Type, "默认排序目录在前")
	require.Equal(t, "Alpha.txt", result.Entries[1].Name, "同层按名称不区分大小写排序")
	require.False(t, result.Truncated)

	result, err = service.List(context.Background(), ListRequest{Scope: "workspace:wd_test", Sort: SortSizeDesc})
	require.NoError(t, err)
	require.Equal(t, "beta.txt", result.Entries[0].Name)
	require.Equal(t, SortSizeDesc, result.Sort)
}

func TestStatReportsMetadata(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.md"), []byte("# hi"))
	service, _ := newTestService(t, root)
	stat, err := service.Stat(context.Background(), PathRequest{Scope: "workspace:wd_test", Path: "notes.md"})
	require.NoError(t, err)
	require.Equal(t, "notes.md", stat.Path)
	require.Equal(t, "file", stat.Type)
	require.True(t, stat.IsText)
	require.Equal(t, ".md", stat.Ext)
	require.Equal(t, int64(4), stat.Size)
	require.NotEmpty(t, stat.Mime)

	_, err = service.Stat(context.Background(), PathRequest{Scope: "workspace:wd_test", Path: "missing.md"})
	require.True(t, fsscope.IsCode(err, fsscope.CodePathNotFound))
}

func TestPreviewTextImageBinaryAndTooLarge(t *testing.T) {
	root := t.TempDir()
	service, _ := newTestService(t, root)

	writeFile(t, filepath.Join(root, "small.txt"), []byte("line1\nline2\n"))
	preview, err := service.Preview(context.Background(), PreviewRequest{Scope: "workspace:wd_test", Path: "small.txt"})
	require.NoError(t, err)
	require.Equal(t, KindText, preview.Kind)
	require.NotNil(t, preview.Text)
	require.Equal(t, "line1\nline2\n", *preview.Text)
	require.Equal(t, 2, preview.LineCount)
	require.False(t, preview.Truncated)

	writeFile(t, filepath.Join(root, "big.txt"), []byte(strings.Repeat("a", 4096)))
	preview, err = service.Preview(context.Background(), PreviewRequest{Scope: "workspace:wd_test", Path: "big.txt", MaxBytes: 1024})
	require.NoError(t, err)
	require.True(t, preview.Truncated)
	require.Equal(t, int64(1024), preview.LimitBytes)
	require.Len(t, *preview.Text, 1024)

	writeFile(t, filepath.Join(root, "binary.bin"), []byte{0x61, 0x00, 0x62})
	preview, err = service.Preview(context.Background(), PreviewRequest{Scope: "workspace:wd_test", Path: "binary.bin"})
	require.NoError(t, err)
	require.Equal(t, KindBinary, preview.Kind)
	require.Equal(t, ReasonNulByte, preview.Reason)

	writeFile(t, filepath.Join(root, "invalid.bin"), []byte{0xff, 0xfe, 0xfd})
	preview, err = service.Preview(context.Background(), PreviewRequest{Scope: "workspace:wd_test", Path: "invalid.bin"})
	require.NoError(t, err)
	require.Equal(t, KindBinary, preview.Kind)
	require.Equal(t, ReasonInvalidUTF8, preview.Reason)

	writeFile(t, filepath.Join(root, "big.png"), []byte(strings.Repeat("i", int(DefaultLimits().PreviewImageBytes)+1)))
	preview, err = service.Preview(context.Background(), PreviewRequest{Scope: "workspace:wd_test", Path: "big.png"})
	require.NoError(t, err)
	require.Equal(t, KindTooLarge, preview.Kind)
	require.Equal(t, ReasonTooLarge, preview.Reason)

	writeFile(t, filepath.Join(root, "tiny.png"), []byte{0x89, 0x50, 0x4e, 0x47})
	preview, err = service.Preview(context.Background(), PreviewRequest{Scope: "workspace:wd_test", Path: "tiny.png"})
	require.NoError(t, err)
	require.Equal(t, KindImage, preview.Kind)
	require.Equal(t, "iVBORw==", preview.DataBase64)
}

func TestPreviewDoesNotMisreportTruncatedUTF8AsBinary(t *testing.T) {
	root := t.TempDir()
	service, _ := newTestService(t, root)
	// 每个中文字符 3 字节；故意在字符中间截断，必须仍判定为 text。
	writeFile(t, filepath.Join(root, "cjk.txt"), []byte(strings.Repeat("中", 100)))
	preview, err := service.Preview(context.Background(), PreviewRequest{Scope: "workspace:wd_test", Path: "cjk.txt", MaxBytes: 10})
	require.NoError(t, err)
	require.Equal(t, KindText, preview.Kind)
	require.True(t, preview.Truncated)
	require.Equal(t, 9, len(*preview.Text))
}

func TestPreviewAndDownloadRejectDirectories(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0o755))
	service, _ := newTestService(t, root)
	_, err := service.Preview(context.Background(), PreviewRequest{Scope: "workspace:wd_test", Path: "sub"})
	require.True(t, fsscope.IsCode(err, fsscope.CodePathNotFile))
	_, err = service.OpenDownload(context.Background(), PathRequest{Scope: "workspace:wd_test", Path: "sub"})
	require.True(t, fsscope.IsCode(err, fsscope.CodePathNotFile))
}

func TestListRootsProbesGitAndSweepsExpiredUploads(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), []byte("x"))
	service, _ := newTestService(t, root)

	roots, err := service.ListRoots(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, roots)
	found := false
	for _, item := range roots {
		if item.Scope == "workspace:wd_test" {
			found = true
			require.True(t, item.Exists)
		}
	}
	require.True(t, found, "注册工作目录必须出现在 /fs/roots")

	// 过期会话：ListRoots 顺带懒清理（不做后台定时器）。
	uploadDir := filepath.Join(root, uploadDirName)
	require.NoError(t, os.MkdirAll(uploadDir, 0o700))
	part := filepath.Join(uploadDir, "up_expired.part")
	writeFile(t, part, []byte("junk"))
	state := uploadState{
		UploadID:  "up_expired",
		PartPath:  part,
		Size:      4,
		ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	}
	require.NoError(t, service.writeSidecar(state))
	_, err = service.ListRoots(context.Background())
	require.NoError(t, err)
	require.NoFileExists(t, part)
	require.NoFileExists(t, state.sidecarPath())
}

func TestCompleteUploadRenamesIntoPlaceAndReportsAction(t *testing.T) {
	root := t.TempDir()
	service, _ := newTestService(t, root)
	content := []byte("hello upload")
	sum := sha256.Sum256(content)

	init, err := service.InitUpload(context.Background(), UploadInitRequest{
		Scope:  "workspace:wd_test",
		Dir:    "",
		Name:   "payload.bin",
		Size:   int64(len(content)),
		SHA256: hex.EncodeToString(sum[:]),
	})
	require.NoError(t, err)
	require.NotEmpty(t, init.UploadID)
	require.Equal(t, int64(0), init.Offset)
	require.False(t, init.Completed)

	_, err = service.PutChunk(context.Background(), UploadChunkRequest{
		UploadID: init.UploadID, Start: 0, Data: content[:5],
	})
	require.NoError(t, err)
	// 乱序分片：起点与已落盘 offset 不一致 → 409 + expected_offset。
	_, err = service.PutChunk(context.Background(), UploadChunkRequest{
		UploadID: init.UploadID, Start: 3, Data: content[5:],
	})
	require.True(t, fsscope.IsCode(err, CodeUploadOffsetMismat))
	require.Equal(t, 409, fsscope.StatusOf(err, 0))
	require.Equal(t, int64(5), fsscope.DetailsOf(err)["expected_offset"])

	status, err := service.UploadStatus(context.Background(), init.UploadID)
	require.NoError(t, err)
	require.Equal(t, int64(5), status.Offset)
	require.Equal(t, int64(len(content)), status.Size)

	_, err = service.PutChunk(context.Background(), UploadChunkRequest{
		UploadID: init.UploadID, Start: 5, Data: content[5:],
	})
	require.NoError(t, err)

	result, err := service.CompleteUpload(context.Background(), UploadCompleteRequest{UploadID: init.UploadID})
	require.NoError(t, err)
	require.Equal(t, UploadActionCreate, result.Action)
	require.Equal(t, "payload.bin", result.Path)
	require.Equal(t, hex.EncodeToString(sum[:]), result.SHA256)
	written, readErr := os.ReadFile(filepath.Join(root, "payload.bin"))
	require.NoError(t, readErr)
	require.Equal(t, content, written)
	require.NoFileExists(t, filepath.Join(root, uploadDirName, init.UploadID+".part"))
}

func TestCompleteUploadRejectsBadChecksumAndKeepsTempFile(t *testing.T) {
	root := t.TempDir()
	service, _ := newTestService(t, root)
	content := []byte("abc")
	init, err := service.InitUpload(context.Background(), UploadInitRequest{
		Scope: "workspace:wd_test", Name: "bad.bin", Size: 3,
	})
	require.NoError(t, err)
	_, err = service.PutChunk(context.Background(), UploadChunkRequest{UploadID: init.UploadID, Start: 0, Data: content})
	require.NoError(t, err)
	_, err = service.CompleteUpload(context.Background(), UploadCompleteRequest{UploadID: init.UploadID, SHA256: strings.Repeat("0", 64)})
	require.True(t, fsscope.IsCode(err, CodeUploadChecksum))
	require.Equal(t, 409, fsscope.StatusOf(err, 0))
	// 临时文件保留，便于前端「重传最后一片」。
	require.FileExists(t, filepath.Join(root, uploadDirName, init.UploadID+".part"))
	require.NoFileExists(t, filepath.Join(root, "bad.bin"))
}

func TestUploadConflictPolicies(t *testing.T) {
	root := t.TempDir()
	service, _ := newTestService(t, root)
	writeFile(t, filepath.Join(root, "dup.txt"), []byte("original"))

	_, err := service.InitUpload(context.Background(), UploadInitRequest{
		Scope: "workspace:wd_test", Name: "dup.txt", Size: 4,
	})
	require.True(t, fsscope.IsCode(err, CodeTargetExists))
	require.Equal(t, 409, fsscope.StatusOf(err, 0))

	renameInit, err := service.InitUpload(context.Background(), UploadInitRequest{
		Scope: "workspace:wd_test", Name: "dup.txt", Size: 4, ConflictPolicy: ConflictPolicyRename,
	})
	require.NoError(t, err)
	require.Equal(t, "dup (1).txt", renameInit.Target.Path)
	_, err = service.PutChunk(context.Background(), UploadChunkRequest{UploadID: renameInit.UploadID, Start: 0, Data: []byte("next")})
	require.NoError(t, err)
	renamed, err := service.CompleteUpload(context.Background(), UploadCompleteRequest{UploadID: renameInit.UploadID})
	require.NoError(t, err)
	require.Equal(t, UploadActionRename, renamed.Action)
	require.Equal(t, "dup (1).txt", renamed.Path)

	overwriteInit, err := service.InitUpload(context.Background(), UploadInitRequest{
		Scope: "workspace:wd_test", Name: "dup.txt", Size: 5, ConflictPolicy: ConflictPolicyOverwrite,
	})
	require.NoError(t, err)
	_, err = service.PutChunk(context.Background(), UploadChunkRequest{UploadID: overwriteInit.UploadID, Start: 0, Data: []byte("brand")})
	require.NoError(t, err)
	overwritten, err := service.CompleteUpload(context.Background(), UploadCompleteRequest{UploadID: overwriteInit.UploadID})
	require.NoError(t, err)
	require.Equal(t, UploadActionOverwrite, overwritten.Action)
	content, readErr := os.ReadFile(filepath.Join(root, "dup.txt"))
	require.NoError(t, readErr)
	require.Equal(t, "brand", string(content))
}

func TestAbortUploadRemovesArtifactsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	service, _ := newTestService(t, root)
	init, err := service.InitUpload(context.Background(), UploadInitRequest{Scope: "workspace:wd_test", Name: "temp.bin", Size: 10})
	require.NoError(t, err)
	part := filepath.Join(root, uploadDirName, init.UploadID+".part")
	sidecar := filepath.Join(root, uploadDirName, init.UploadID+".json")
	require.FileExists(t, part)
	require.FileExists(t, sidecar)
	require.NoError(t, service.AbortUpload(context.Background(), init.UploadID))
	require.NoFileExists(t, part)
	require.NoFileExists(t, sidecar)
	require.NoError(t, service.AbortUpload(context.Background(), init.UploadID))
	_, err = service.UploadStatus(context.Background(), init.UploadID)
	require.True(t, fsscope.IsCode(err, CodeUploadExpired))
	require.Equal(t, 410, fsscope.StatusOf(err, 0))
}

func TestUploadLimitsAndValidation(t *testing.T) {
	root := t.TempDir()
	service, _ := newTestService(t, root)
	service.limits.MaxUploadBytes = 8

	_, err := service.InitUpload(context.Background(), UploadInitRequest{Scope: "workspace:wd_test", Name: "big.bin", Size: 9})
	require.True(t, fsscope.IsCode(err, CodeUploadTooLarge))
	require.Equal(t, 400, fsscope.StatusOf(err, 0))

	_, err = service.InitUpload(context.Background(), UploadInitRequest{Scope: "workspace:wd_test", Name: "small.bin", Size: 1, ChunkSize: 10})
	require.True(t, fsscope.IsCode(err, CodeChunkSizeInvalid))

	_, err = service.InitUpload(context.Background(), UploadInitRequest{Scope: "workspace:wd_test", Name: "../escape.bin", Size: 1})
	require.True(t, fsscope.IsCode(err, CodeUploadNameInvalid))

	_, err = service.InitUpload(context.Background(), UploadInitRequest{Scope: "workspace:wd_test", Dir: "missing", Name: "a.bin", Size: 1})
	require.True(t, fsscope.IsCode(err, fsscope.CodePathNotFound))

	_, err = service.InitUpload(context.Background(), UploadInitRequest{Scope: "workspace:wd_test", Name: "a.bin", Size: 1, ConflictPolicy: "whatever"})
	require.True(t, fsscope.IsCode(err, CodeConflictPolicyInvalid))
}

func TestUploadSessionExpiryIsLazy(t *testing.T) {
	root := t.TempDir()
	service, _ := newTestService(t, root)
	init, err := service.InitUpload(context.Background(), UploadInitRequest{Scope: "workspace:wd_test", Name: "slow.bin", Size: 4})
	require.NoError(t, err)

	service.now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	_, err = service.PutChunk(context.Background(), UploadChunkRequest{UploadID: init.UploadID, Start: 0, Data: []byte("abcd")})
	require.True(t, fsscope.IsCode(err, CodeUploadExpired))
	require.Equal(t, 410, fsscope.StatusOf(err, 0))
	require.NoFileExists(t, filepath.Join(root, uploadDirName, init.UploadID+".part"))
}

func TestUploadDeduplicatesWhenHashMatches(t *testing.T) {
	root := t.TempDir()
	service, _ := newTestService(t, root)
	content := []byte("same bytes")
	writeFile(t, filepath.Join(root, "same.txt"), content)
	sum := sha256.Sum256(content)
	init, err := service.InitUpload(context.Background(), UploadInitRequest{
		Scope: "workspace:wd_test", Name: "same.txt", Size: int64(len(content)),
		SHA256: hex.EncodeToString(sum[:]), ConflictPolicy: ConflictPolicyFail,
	})
	require.NoError(t, err)
	require.True(t, init.Completed)
	require.True(t, init.Deduplicated)
	require.Empty(t, init.UploadID)
}

func TestOpenDownloadProvidesETagAndHandle(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "data.bin"), []byte("0123456789"))
	service, _ := newTestService(t, root)
	target, err := service.OpenDownload(context.Background(), PathRequest{Scope: "workspace:wd_test", Path: "data.bin"})
	require.NoError(t, err)
	defer target.Close()
	require.Equal(t, int64(10), target.Size)
	require.Equal(t, "data.bin", target.Name)
	require.Equal(t, fmt.Sprintf("\"%d-%d\"", target.Size, target.ModTime.UnixNano()), target.ETag())
	require.NotEmpty(t, target.ContentType)
	buffer := make([]byte, 4)
	_, err = target.File.ReadAt(buffer, 0)
	require.NoError(t, err)
	require.Equal(t, "0123", string(buffer))
}

func TestCountPreviewLinesMatchesFrontendSemantics(t *testing.T) {
	require.Equal(t, 0, CountPreviewLines(""))
	require.Equal(t, 0, CountPreviewLines("\n"))
	require.Equal(t, 1, CountPreviewLines("a"))
	require.Equal(t, 1, CountPreviewLines("a\n"))
	require.Equal(t, 2, CountPreviewLines("a\nb"))
	require.Equal(t, 2, CountPreviewLines("a\r\nb"))
	require.Equal(t, 3, CountPreviewLines("a\nb\nc\n"))
}

func TestListRejectsScopeEscapeThroughSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), []byte("nope"))
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable in this environment: %v", err)
	}
	service, _ := newTestService(t, root)
	_, err := service.List(context.Background(), ListRequest{Scope: "workspace:wd_test", Path: "link"})
	require.True(t, fsscope.IsCode(err, fsscope.CodePathOutsideScope))
	_, err = service.Stat(context.Background(), PathRequest{Scope: "workspace:wd_test", Path: "link/secret.txt"})
	require.True(t, fsscope.IsCode(err, fsscope.CodePathOutsideScope))
}

func TestListRootsGitProbeDegradesGracefully(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	service, _ := newTestService(t, root)
	roots, err := service.ListRoots(context.Background())
	require.NoError(t, err)
	item := findRoot(roots, "workspace:wd_test")
	require.True(t, item.Exists)
	// 假的 .git 目录不是合法仓库；探测失败只置 false + probe_error，不报错。
	require.False(t, item.IsGitRepo)
	require.NotEmpty(t, item.ProbeError)
}

func TestListRootsDetectsRealGitRepository(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available in this environment")
	}
	root := t.TempDir()
	command := exec.Command(gitBin, "init", "--quiet")
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Skipf("git init unavailable: %v (%s)", runErr, string(output))
	}
	service, _ := newTestService(t, root)
	roots, err := service.ListRoots(context.Background())
	require.NoError(t, err)
	item := findRoot(roots, "workspace:wd_test")
	require.True(t, item.IsGitRepo)
	require.Empty(t, item.ProbeError)
	require.NotEmpty(t, item.GitRoot)
}

func TestNormalizeTargetPathHelpers(t *testing.T) {
	require.Equal(t, "dir/name.txt", listPath("dir", "name.txt"))
	require.Equal(t, "name.txt", listPath("", "name.txt"))
	require.Equal(t, "dir", parentOf("dir/name.txt"))
	require.Equal(t, "", parentOf("name.txt"))
	require.True(t, IsHiddenName(".env"))
	require.False(t, IsHiddenName("env"))
	require.True(t, isInternalName(uploadDirName))
}
