package remotetools

// Contract tests for the envd seam adapter. These are not convenience tests: the seam has
// three rules that only hold because somebody checks them - absence must classify as absent
// while a permission problem must not, Open must not buffer, and the accessor must not
// claim a capability the transport cannot answer. Each one below has a failure mode that
// would otherwise be silent and wrong on the target rather than loud here.

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// fakeEnvdFiles is a described target: files with bytes, directories, and links that point
// at other entries in the same description.
type fakeEnvdFiles struct {
	entries map[string]envdclient.Entry
	// listing holds each directory's children in the target's own order. The order is
	// explicit rather than derived from a map so "the accessor did not sort" is an
	// observation and not an accident of iteration.
	listing   map[string][]string
	content   map[string][]byte
	windows   []string
	listCalls []string
	statErr   error
	listErr   error
	rangeErr  error
	statCalls map[string]int
}

func (f *fakeEnvdFiles) Stat(_ context.Context, filePath string) (envdclient.Entry, error) {
	if f.statErr != nil {
		return envdclient.Entry{}, f.statErr
	}
	if f.statCalls == nil {
		f.statCalls = map[string]int{}
	}
	f.statCalls[filePath]++
	entry, ok := f.entries[filePath]
	if !ok {
		return envdclient.Entry{}, &envdclient.ConnectError{Code: "not_found", Message: "file not found: " + filePath}
	}
	return entry, nil
}

func (f *fakeEnvdFiles) ListDir(_ context.Context, dirPath string, depth uint32) ([]envdclient.Entry, error) {
	f.listCalls = append(f.listCalls, dirPath+"@"+strconv.FormatUint(uint64(depth), 10))
	if f.listErr != nil {
		return nil, f.listErr
	}
	if depth != 1 {
		return nil, &envdclient.ConnectError{Code: "invalid_argument", Message: "this fake only answers one level"}
	}
	names, ok := f.listing[dirPath]
	if !ok {
		return nil, &envdclient.ConnectError{Code: "not_found", Message: "directory not found: " + dirPath}
	}
	out := make([]envdclient.Entry, 0, len(names))
	for _, name := range names {
		entry, found := f.entries[path.Join(dirPath, name)]
		if !found {
			return nil, &envdclient.ConnectError{Code: "internal", Message: "listing names an unknown entry " + name}
		}
		out = append(out, entry)
	}
	return out, nil
}

func (f *fakeEnvdFiles) ReadRange(_ context.Context, filePath string, offset, size int64) (*envdclient.FileRead, error) {
	if f.rangeErr != nil {
		return nil, f.rangeErr
	}
	body, ok := f.content[filePath]
	if !ok {
		return nil, &envdclient.ConnectError{Code: "not_found", Message: "file not found: " + filePath}
	}
	if offset > int64(len(body)) {
		offset = int64(len(body))
	}
	end := offset + size
	if end > int64(len(body)) {
		end = int64(len(body))
	}
	f.windows = append(f.windows, filePath+"@"+strconv.FormatInt(offset, 10)+"+"+strconv.FormatInt(end-offset, 10))
	data := append([]byte(nil), body[offset:end]...)
	return &envdclient.FileRead{Data: data, StatusCode: 206, Total: int64(len(body)), Partial: end < int64(len(body))}, nil
}

func fakeFileEntry(filePath string, size int64) envdclient.Entry {
	return envdclient.Entry{
		Name: path.Base(filePath), Path: filePath, Type: envdclient.FileTypeFile,
		Size: strconv.FormatInt(size, 10), Mode: 0o644, ModifiedTime: "2026-01-02T03:04:05Z",
	}
}

func fakeDirEntry(dirPath string) envdclient.Entry {
	return envdclient.Entry{
		Name: path.Base(dirPath), Path: dirPath, Type: envdclient.FileTypeDirectory,
		Size: "4096", Mode: 0o755, ModifiedTime: "2026-01-02T03:04:05Z",
	}
}

func fakeLinkEntry(filePath, target string) envdclient.Entry {
	return envdclient.Entry{
		Name: path.Base(filePath), Path: filePath, Type: envdclient.FileTypeSymlink,
		Size: "0", Mode: 0o777, SymlinkTarget: target,
	}
}

func newFakeAccess(t *testing.T, fake *fakeEnvdFiles, chunk int64) *envdAccess {
	t.Helper()
	if fake.entries == nil {
		fake.entries = map[string]envdclient.Entry{}
	}
	return &envdAccess{api: fake, chunk: chunk}
}

func TestEnvdAccessStatFollowsWhileLstatReportsTheLink(t *testing.T) {
	fake := &fakeEnvdFiles{
		entries: map[string]envdclient.Entry{
			"/work/link":  fakeLinkEntry("/work/link", "/work/real"),
			"/work/real":  fakeDirEntry("/work/real"),
			"/work/rel":   fakeLinkEntry("/work/rel", "real"),
			"/work/note":  fakeFileEntry("/work/note", 5),
			"/work/tolin": fakeLinkEntry("/work/tolin", "note"),
		},
	}
	access := newFakeAccess(t, fake, 0)

	info, err := access.Lstat(context.Background(), "/work/link")
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&fs.ModeSymlink == 0 || info.IsDir() {
		t.Fatalf("Lstat must report the link itself, got mode %v isdir %v", info.Mode(), info.IsDir())
	}

	followed, err := access.Stat(context.Background(), "/work/link")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !followed.IsDir() {
		t.Fatalf("Stat must follow the link to the directory, got mode %v", followed.Mode())
	}
	// os.Stat names the path it was asked about, not the target: a walk that printed the
	// target's base name would report a file the caller never listed.
	if followed.Name() != "link" {
		t.Fatalf("Name = %q, want the asked-about base %q", followed.Name(), "link")
	}

	// A relative link target is relative to the directory holding the link.
	rel, err := access.Stat(context.Background(), "/work/rel")
	if err != nil || !rel.IsDir() {
		t.Fatalf("relative target resolution: err=%v info=%v", err, rel)
	}
	toFile, err := access.EvalLinkPath(context.Background(), "/work/tolin")
	if err != nil || toFile != "/work/note" {
		t.Fatalf("EvalLinkPath = %q, err %v, want /work/note", toFile, err)
	}
}

func TestEnvdAccessLinkCycleBecomesAnError(t *testing.T) {
	fake := &fakeEnvdFiles{
		entries: map[string]envdclient.Entry{
			"/work/a": fakeLinkEntry("/work/a", "/work/b"),
			"/work/b": fakeLinkEntry("/work/b", "/work/a"),
		},
	}
	access := newFakeAccess(t, fake, 0)

	if _, err := access.Stat(context.Background(), "/work/a"); err == nil {
		t.Fatal("a link cycle must be an error; returning the entry would send the caller down the loop")
	} else if !strings.Contains(err.Error(), "链接环") {
		t.Fatalf("the error must say why it gave up: %v", err)
	}
	// A broken link is absence, the same way a dangling path is absence for os.Stat.
	fake.entries["/work/dangling"] = fakeLinkEntry("/work/dangling", "/work/nowhere")
	if _, err := access.Stat(context.Background(), "/work/dangling"); !toolcore.IsNotExist(err) {
		t.Fatalf("a broken link must classify as absent, got %v", err)
	}
}

// fakeLinkEntryV2 is a link as envd >= 0.10.0 reports it: is_symlink marks the entry itself
// while Type describes the TARGET (the daemon's documented meaning), and SymlinkTarget is
// the already-resolved absolute target. The pre-upgrade daemon never sent this shape.
func fakeLinkEntryV2(filePath string, targetType envdclient.FileType, target string) envdclient.Entry {
	return envdclient.Entry{
		Name: path.Base(filePath), Path: filePath, Type: targetType,
		Size: "0", Mode: 0o755, SymlinkTarget: target, IsSymlink: true,
		ModifiedTime: "2026-01-02T03:04:05Z",
	}
}

// TestEnvdAccessConsumesTheIsSymlinkVocabulary pins the second wire encoding. envd >= 0.10.0
// marks the entry itself and lets Type describe the target, so the same on-target facts
// arrive in a different shape; an adapter that only understood the old shape would read
// every link on an upgraded daemon as a plain file or directory.
func TestEnvdAccessConsumesTheIsSymlinkVocabulary(t *testing.T) {
	ctx := context.Background()
	fake := &fakeEnvdFiles{
		entries: map[string]envdclient.Entry{
			"/work/linkdir":  fakeLinkEntryV2("/work/linkdir", envdclient.FileTypeDirectory, "/work/real"),
			"/work/linkfile": fakeLinkEntryV2("/work/linkfile", envdclient.FileTypeFile, "/work/note"),
			"/work/dangling": fakeLinkEntryV2("/work/dangling", envdclient.FileTypeUnspecified, "/work/dangling"),
			"/work/real":     fakeDirEntry("/work/real"),
			"/work/note":     fakeFileEntry("/work/note", 5),
		},
		listing: map[string][]string{"/work": {"linkdir", "linkfile", "dangling", "real", "note"}},
	}
	access := newFakeAccess(t, fake, 0)

	lstat, err := access.Lstat(ctx, "/work/linkdir")
	if err != nil {
		t.Fatalf("Lstat(linkdir): %v", err)
	}
	if lstat.Mode()&fs.ModeSymlink == 0 || lstat.IsDir() {
		t.Fatalf("Lstat must report the link itself, got mode %v isdir %v", lstat.Mode(), lstat.IsDir())
	}

	stat, err := access.Stat(ctx, "/work/linkdir")
	if err != nil || !stat.IsDir() {
		t.Fatalf("Stat must follow the link to the directory: err=%v info=%v", err, stat)
	}
	if stat.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("a followed link must not still claim ModeSymlink: %v", stat.Mode())
	}

	resolved, err := access.EvalLinkPath(ctx, "/work/linkfile")
	if err != nil || resolved != "/work/note" {
		t.Fatalf("EvalLinkPath = %q, err %v, want /work/note", resolved, err)
	}

	// A listing must present the link as a link, not as the directory it resolves to,
	// because that is what keeps a non-following walk from descending it.
	entries, err := access.ReadDir(ctx, "/work")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == "linkdir" && (entry.IsDir() || entry.Type()&fs.ModeSymlink == 0) {
			t.Fatalf("a link-to-directory must stay a link in a listing, got dir=%v type=%v", entry.IsDir(), entry.Type())
		}
	}

	// The daemon answers a chain it cannot resolve with the link's own path; dangling and
	// cyclic are indistinguishable there, so the adapter must not name either - and must
	// not turn "unknown" into absence.
	if _, err := access.Stat(ctx, "/work/dangling"); err == nil {
		t.Fatal("an unresolvable link must be an error, not a followed entry")
	} else {
		if toolcore.IsNotExist(err) {
			t.Fatalf("an unresolvable chain must not be reported as plain absence: %v", err)
		}
		if !strings.Contains(err.Error(), "不可解析") {
			t.Fatalf("the error must say the chain itself could not be resolved: %v", err)
		}
	}
	if info, err := access.Lstat(ctx, "/work/dangling"); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("Lstat of a dangling link must still describe the link: info=%v err=%v", info, err)
	}
}

func TestEnvdAccessReadDirKeepsTheTargetsOrderAndTypes(t *testing.T) {
	fake := &fakeEnvdFiles{
		entries: map[string]envdclient.Entry{
			"/work/z.txt":   fakeFileEntry("/work/z.txt", 1),
			"/work/.hidden": fakeFileEntry("/work/.hidden", 1),
			"/work/a.txt":   fakeFileEntry("/work/a.txt", 1),
			"/work/sub":     fakeDirEntry("/work/sub"),
			"/work/lnk":     fakeLinkEntry("/work/lnk", "/work/sub"),
		},
		// Raw, deliberately not lexical: envd's ListDir documents raw order including
		// dotfiles, and the seam contract says ordering is the caller's business.
		listing: map[string][]string{"/work": {"z.txt", ".hidden", "sub", "a.txt", "lnk"}},
	}
	access := newFakeAccess(t, fake, 0)

	entries, err := access.ReadDir(context.Background(), "/work")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	if strings.Join(got, ",") != "z.txt,.hidden,sub,a.txt,lnk" {
		t.Fatalf("the accessor reordered the listing (%v); the core owns ordering", got)
	}
	kinds := map[string]string{}
	for _, entry := range entries {
		switch {
		case entry.IsDir():
			kinds[entry.Name()] = "dir"
		case entry.Type()&fs.ModeSymlink != 0:
			kinds[entry.Name()] = "link"
		default:
			kinds[entry.Name()] = "file"
		}
	}
	// The distinction between a directory and a symlink-to-directory is what keeps a walk
	// from descending out of the tree: Mode arrives as permission bits only, so the type
	// bits have to be synthesised from Type.
	if kinds["sub"] != "dir" || kinds["lnk"] != "link" || kinds["a.txt"] != "file" {
		t.Fatalf("entry kinds wrong: %v", kinds)
	}
}

// TestEnvdAccessModeCarriesTheKindBits is the assertion that a first draft did not have,
// and the absence of it is why a mutation survived: the directory kind was checked through
// DirEntry.IsDir(), which is answered from Entry.Type, so dropping the type bits from Mode()
// changed nothing there. But the search core decides "is this root a single file" with
// info.Mode().IsRegular() (grep.go's file-mode root, the size gate, the engine's argv shape),
// and envd sends Mode as permission bits only - 0755 for a directory. Without the synthesised
// ModeDir bit a remote directory would answer IsRegular()==true and the core would open a
// directory as if it were a file.
func TestEnvdAccessModeCarriesTheKindBits(t *testing.T) {
	fake := &fakeEnvdFiles{entries: map[string]envdclient.Entry{
		"/work/sub":  fakeDirEntry("/work/sub"),
		"/work/note": fakeFileEntry("/work/note", 5),
		"/work/lnk":  fakeLinkEntry("/work/lnk", "/work/note"),
	}}
	access := newFakeAccess(t, fake, 0)
	ctx := context.Background()

	dir, err := access.Stat(ctx, "/work/sub")
	if err != nil {
		t.Fatalf("Stat(dir): %v", err)
	}
	if dir.Mode().IsRegular() {
		t.Fatalf("a directory reported Mode %v, i.e. a regular file: the core would search it as one file", dir.Mode())
	}
	if dir.Mode()&fs.ModeDir == 0 {
		t.Fatalf("a directory lost its ModeDir bit: %v", dir.Mode())
	}

	plain, err := access.Stat(ctx, "/work/note")
	if err != nil {
		t.Fatalf("Stat(file): %v", err)
	}
	if !plain.Mode().IsRegular() {
		t.Fatalf("a plain file must answer IsRegular, got %v", plain.Mode())
	}
	if perm := plain.Mode().Perm(); perm != 0o644 {
		t.Fatalf("permission bits = %v, want 0644", perm)
	}

	// The link's own shape (Lstat) must still say "link", because that is what keeps a
	// listing from descending through it, while Stat resolves to the target's shape.
	link, err := access.Lstat(ctx, "/work/lnk")
	if err != nil {
		t.Fatalf("Lstat(link): %v", err)
	}
	if link.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("Lstat on a link lost ModeSymlink: %v", link.Mode())
	}
	if link.Mode().IsRegular() {
		t.Fatalf("a link must not answer IsRegular: %v", link.Mode())
	}
}

func TestEnvdAccessAbsenceAndPermissionAreNotTheSameAnswer(t *testing.T) {
	// Absence: the seam says a missing path must classify as absent, or a core answers
	// "no such file" about a target it never asked about.
	missing := &fakeEnvdFiles{}
	absent := newFakeAccess(t, missing, 0)
	ctx := context.Background()
	if _, err := absent.Stat(ctx, "/work/gone"); !toolcore.IsNotExist(err) {
		t.Fatalf("Stat absence: %v", err)
	}
	if _, err := absent.Lstat(ctx, "/work/gone"); !toolcore.IsNotExist(err) {
		t.Fatalf("Lstat absence: %v", err)
	}
	if _, err := absent.ReadDir(ctx, "/work/gone"); !toolcore.IsNotExist(err) {
		t.Fatalf("ReadDir absence: %v", err)
	}
	if _, err := absent.Open(ctx, "/work/gone"); !toolcore.IsNotExist(err) {
		t.Fatalf("Open absence: %v", err)
	}

	// Permission: the same seam says a denied path must NOT be reported as absent, because
	// a caller told "not found" goes looking in the wrong place while the file sits there.
	denied := &fakeEnvdFiles{statErr: &envdclient.ConnectError{Code: "permission_denied", Message: "denied"}}
	blocked := newFakeAccess(t, denied, 0)
	_, err := blocked.Stat(ctx, "/work/secret")
	if err == nil {
		t.Fatal("a permission failure must not be swallowed")
	}
	if toolcore.IsNotExist(err) {
		t.Fatalf("a permission problem was classified as absence: %v", err)
	}
	if !errors.Is(err, fs.ErrPermission) && !strings.Contains(err.Error(), "permission_denied") {
		t.Fatalf("the error must still say it was permission: %v", err)
	}
}

func TestEnvdAccessOpenReadsInWindowsAndCanRewind(t *testing.T) {
	body := []byte("0123456789abcdef") // 16 bytes, four-byte window
	fake := &fakeEnvdFiles{
		entries: map[string]envdclient.Entry{"/work/f": fakeFileEntry("/work/f", int64(len(body)))},
		content: map[string][]byte{"/work/f": body},
	}
	access := newFakeAccess(t, fake, 4)

	handle, err := access.Open(context.Background(), "/work/f")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer handle.Close()

	got, err := io.ReadAll(handle)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("bytes = %q, want %q", got, body)
	}
	// The contract is that Open does not buffer the whole file: the largest window fetched
	// must be the configured chunk, not the file.
	for _, window := range fake.windows {
		if plus := strings.Index(window, "+"); plus > 0 {
			if size, convErr := strconv.Atoi(window[plus+1:]); convErr == nil && size > 4 {
				t.Fatalf("a window fetched %d bytes while the chunk is 4: %v", size, fake.windows)
			}
		}
	}
	if len(fake.windows) < 3 {
		t.Fatalf("expected several windows over a 16-byte file at 4-byte chunks, got %v", fake.windows)
	}

	// Rewind must replay the same bytes, which is what lets the view core probe a BOM and
	// then read the file from the start through one handle.
	if _, err := handle.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	again := make([]byte, len(body))
	if _, err := io.ReadFull(handle, again); err != nil {
		t.Fatalf("read after seek: %v", err)
	}
	if string(again) != string(body) {
		t.Fatalf("after rewind = %q, want %q", again, body)
	}
	if _, err := handle.Seek(-3, io.SeekEnd); err != nil {
		t.Fatalf("Seek from end: %v", err)
	}
	tail, err := io.ReadAll(handle)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if string(tail) != "def" {
		t.Fatalf("tail = %q, want %q", tail, "def")
	}
}

func TestEnvdAccessReportsTimesAndMissingTimesHonestly(t *testing.T) {
	fake := &fakeEnvdFiles{entries: map[string]envdclient.Entry{
		"/work/ok":    fakeFileEntry("/work/ok", 1),
		"/work/bogus": {Name: "bogus", Path: "/work/bogus", Type: envdclient.FileTypeFile, Size: "1", Mode: 0o644, ModifiedTime: "not a time"},
	}}
	access := newFakeAccess(t, fake, 0)

	info, err := access.Stat(context.Background(), "/work/ok")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.ModTime().Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("ModTime = %v, want the parsed wire value", info.ModTime())
	}
	odd, err := access.Stat(context.Background(), "/work/bogus")
	if err != nil {
		t.Fatalf("a strange timestamp must not fail the listing: %v", err)
	}
	if !odd.ModTime().IsZero() {
		t.Fatalf("unparseable time became %v; the zero time is the honest unknown", odd.ModTime())
	}
	if odd.Size() != 1 {
		t.Fatalf("Size = %d, want 1 (the wire sends size as a string)", odd.Size())
	}
}

// TestEnvdAccessDoesNotClaimAFilesystemID is the honesty pin for design §2 Q2. envd's Entry
// carries no device and no inode, so the adapter must not implement the capability: the
// cores then decline one_file_system for a remote search instead of comparing invented
// values, which is the difference between a refusal and a confidently wrong answer.
func TestEnvdAccessDoesNotClaimAFilesystemID(t *testing.T) {
	access := newFakeAccess(t, &fakeEnvdFiles{}, 0)
	if _, ok := toolcore.Access(access).(toolcore.FilesystemIdentifier); ok {
		t.Fatal("the envd accessor claims a filesystem id it cannot read; that would let one_file_system compare invented values")
	}
	if _, ok := toolcore.Access(access).(toolcore.SymlinkResolver); !ok {
		t.Fatal("the envd accessor must provide link resolution: it answers from SymlinkTarget on the file channel, no process spawn needed")
	}
}

func TestEnvdAccessOpenRefusesADirectory(t *testing.T) {
	fake := &fakeEnvdFiles{entries: map[string]envdclient.Entry{"/work/sub": fakeDirEntry("/work/sub")}}
	access := newFakeAccess(t, fake, 0)
	if _, err := access.Open(context.Background(), "/work/sub"); err == nil {
		t.Fatal("opening a directory must be an error, not an empty stream")
	}
}
