package remotetools

// envd_access.go is the target-side implementation of the shared I/O seam
// (toolcore.Access). It is the piece that makes the search cores able to describe the
// target at all: until now the only production implementation was localio, so every
// "backend-neutral" claim the seam work produced was a possibility, not a path.
//
// Three rules shape this file, and each one comes from a failure the seam line already
// documented:
//
//   - No second authority over paths. A core resolved and jailed the path before it got
//     here, so nothing below re-checks the jail and nothing here decides whether a path
//     is allowed. The link resolution below exists only to answer "what is this a link
//     to", which is what a caller following links needs in order to terminate a cycle.
//   - Absence is a claim, not a convenience. The seam's contract says a missing path must
//     classify as absent while a permission problem must NOT, because the alternative
//     turns a recovery hint into a lie. That mapping is done once, in envdError.
//   - Open does not buffer. A remote file can be gigabytes and the view core pages
//     through it; fetching the whole body to serve a 4 KiB read would make the seam
//     expensive exactly where it was introduced to be cheap.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// envdFileAPI is the slice of the envd client this adapter needs. Declaring it here,
// rather than taking a *envdclient.Client, is what lets the contract tests below run
// against a described tree instead of a live daemon - and the seam's whole value
// proposition is that the core cannot tell the difference.
type envdFileAPI interface {
	Stat(ctx context.Context, filePath string) (envdclient.Entry, error)
	ListDir(ctx context.Context, dirPath string, depth uint32) ([]envdclient.Entry, error)
	ReadRange(ctx context.Context, filePath string, offset, size int64) (*envdclient.FileRead, error)
}

// envdReadChunk is the window size Open fetches at a time. It is a variable for the same
// reason the budget knobs are: a test must be able to force several windows across a short
// file without allocating a megabyte.
var envdReadChunk int64 = 64 << 10

// envdLinkHops bounds symlink resolution. The target reports the FINAL component's linkness
// (intermediate components are followed by the daemon itself, which is exactly POSIX
// resolution) and, on envd >= 0.10.0, an already-resolved target - so one hop normally
// terminates. The loop stays because a chain can still be a chain (and because older
// daemons answer with raw targets), and a cycle in it must become an error the way ELOOP
// does, not a loop in this process.
const envdLinkHops = 40

type envdAccess struct {
	api envdFileAPI
	// chunk is the read window for Open. Zero falls back to envdReadChunk; it is a
	// field rather than only a global so a test can force several windows across a short
	// file without allocating a megabyte.
	chunk int64
}

// NewEnvdAccess builds the seam adapter over one envd connection. The caller is the
// remote runtime's FileAccess, which is what hands it to the cores.
func NewEnvdAccess(client *envdclient.Client) toolcore.Access {
	return &envdAccess{api: client}
}

// compile-time assertions. The first is the whole point of the file; the second and third
// are the two capabilities envd CAN answer from its file channel alone, and the absence of
// a FilesystemIdentifier assertion below is deliberate: Entry has no device and no inode,
// so claiming one would be the forged identity design §2 Q2 forbids.
var (
	_ toolcore.Access          = (*envdAccess)(nil)
	_ toolcore.SymlinkResolver = (*envdAccess)(nil)
	_ toolcore.TargetPathStyle = (*envdAccess)(nil)
)

// envdError maps an envd failure onto the seam's error vocabulary.
//
// not_found is the only code that means absent. permission_denied, unauthenticated,
// invalid_argument and transport failures keep their own meaning: if a permission problem
// were reported as absence, a core would answer "no such file" about a file that exists
// and the caller would be sent looking in the wrong place.
func envdError(op, filePath string, err error) error {
	if err == nil {
		return nil
	}
	var connectErr *envdclient.ConnectError
	if errors.As(err, &connectErr) && connectErr.IsNotFound() {
		return fmt.Errorf("%s %s: %w", op, filePath, fs.ErrNotExist)
	}
	return fmt.Errorf("%s %s: %w", op, filePath, err)
}

// envdInfo is one Entry as an fs.FileInfo.
//
// Mode needs a decision stated out loud: envd sends the permission bits (493 == 0755) while
// the *kind* arrives separately as Type. So the type bits are taken from Type and the
// permission bits are masked off Mode - taking Mode verbatim would report a directory with
// no ModeDir, and a walk would treat every directory on the target as a file.
type envdInfo struct {
	entry envdclient.Entry
	// name overrides Entry.Name when the entry was reached through a link path, so the
	// caller keeps the name it asked about (the same thing os.Stat does for a link).
	name string
}

func (i envdInfo) Name() string {
	if i.name != "" {
		return i.name
	}
	if i.entry.Name != "" {
		return i.entry.Name
	}
	return path.Base(i.entry.Path)
}

func (i envdInfo) Size() int64 { return i.entry.SizeBytes() }

func (i envdInfo) ModTime() time.Time {
	// ModifiedTime is a string on the wire. A value we cannot parse becomes the zero
	// time rather than an error: a listing whose one timestamp is odd must still list the
	// file, and an unknown mtime sorts as unknown instead of failing the whole search.
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(i.entry.ModifiedTime))
	if err != nil {
		return time.Time{}
	}
	return ts
}

// IsDir answers "may a walk descend here", not "what did Type say": envd >= 0.10.0 reports a
// link to a directory as Type DIRECTORY alongside is_symlink, and a listing that treated
// that as a directory would descend through a link the local walker refuses to follow. The
// link flag therefore wins over the type, in both wire encodings.
func (i envdInfo) IsDir() bool { return !i.entry.IsLink() && i.entry.IsDir() }

func (i envdInfo) Mode() fs.FileMode {
	mode := fs.FileMode(i.entry.Mode & 0o7777)
	// The link case is checked first on purpose: with the >= 0.10.0 vocabulary a
	// link-to-directory also answers IsDir(), and a ModeDir bit would reintroduce the
	// descent this adapter exists to refuse. The permission bits are left as the daemon
	// sent them (on that vocabulary they are the target's, which is what the target-user
	// readability gate reads).
	switch {
	case i.entry.IsLink():
		mode |= fs.ModeSymlink
	case i.entry.IsDir():
		mode |= fs.ModeDir
	}
	return mode
}

// Sys returns nil, and that is the honest answer: the envd entry carries no device, no
// inode and no platform struct, so there is nothing to expose. Consumers that need a
// platform-specific field must decline the feature rather than invent it - which is what
// sort=accessed/created (design §2 Q4) and one_file_system (design §2 Q2) now do.
func (i envdInfo) Sys() any { return nil }

// PathSeparator reports '/' and that is a fact about envd, not a preference: the daemon
// runs on Linux and every path in its API is POSIX. It matters because the cores build
// child paths themselves - a Windows client walking this target with the host's separator
// would spell \srv\work\a.txt and every stat would miss, silently returning an empty
// result rather than an error.
func (a *envdAccess) PathSeparator() rune { return '/' }

// Lstat describes the path itself. A symlink stays a symlink, which is what lets a
// listing refuse to descend out of the authorized tree.
func (a *envdAccess) Lstat(ctx context.Context, filePath string) (fs.FileInfo, error) {
	entry, err := a.api.Stat(ctx, filePath)
	if err != nil {
		return nil, envdError("lstat", filePath, err)
	}
	return envdInfo{entry: entry, name: path.Base(filePath)}, nil
}

// Stat follows the final component's link chain, like os.Stat.
func (a *envdAccess) Stat(ctx context.Context, filePath string) (fs.FileInfo, error) {
	_, entry, err := a.resolve(ctx, filePath)
	if err != nil {
		return nil, err
	}
	// The name is the base of the path that was asked about, which is what os.Stat
	// reports for a link too - not the target's own name, or every link on the target
	// would appear under a different file name than the one the caller listed.
	return envdInfo{entry: entry, name: path.Base(filePath)}, nil
}

// EvalLinkPath answers "where does this chain actually lead" for cycle termination.
//
// It is built from the file channel alone, because the daemon reports the final component
// as a link while resolving the ones before it - the same shape as POSIX - so walking
// SymlinkTarget gives the target's path without spawning anything. A cycle becomes an
// error, which the caller's rule for a failed resolution already handles by refusing to
// descend: that is ELOOP's job, and it must not become this process's job.
//
// WARNING, kept because it is the reason the daemon version is now part of the contract
// (measured 2026-10-31, n9e@172.18.3.238): envd < 0.10.0 reports the FOLLOWED type for a
// symlink (a link to a file comes back as FILE_TYPE_FILE, a link to a directory as
// FILE_TYPE_DIRECTORY, a dangling link as an empty type) and never populates
// SymlinkTarget - here this function then degenerates to an identity resolution and the
// cycle rule it feeds never fires. envd >= 0.10.0 reports is_symlink plus the resolved
// target, which is what the branches below consume; the link probe in
// scripts/envd_seam_access_verify.go stays the tripwire that tells the two apart on a
// live target (red on the old daemon, green on the new one). See the S6 record in
// docs/plan/searchcore-io-seam-migration-20261028.md.
func (a *envdAccess) EvalLinkPath(ctx context.Context, filePath string) (string, error) {
	resolved, _, err := a.resolve(ctx, filePath)
	return resolved, err
}

// resolve walks the link chain and returns the final path plus its entry.
func (a *envdAccess) resolve(ctx context.Context, filePath string) (string, envdclient.Entry, error) {
	current := filePath
	seen := make(map[string]bool, 4)
	for hop := 0; hop < envdLinkHops; hop++ {
		if seen[current] {
			return "", envdclient.Entry{}, fmt.Errorf("解析链接 %s: 链接环（%d 跳后仍指向已访问过的路径）", filePath, hop)
		}
		seen[current] = true
		entry, err := a.api.Stat(ctx, current)
		if err != nil {
			return "", envdclient.Entry{}, envdError("stat", current, err)
		}
		if !entry.IsLink() {
			return current, entry, nil
		}
		target := strings.TrimSpace(entry.SymlinkTarget)
		if target == "" {
			// The daemon marked the entry as a link but sent no target. Claiming absence
			// here would invent a fact about a path that exists as a link.
			return "", envdclient.Entry{}, fmt.Errorf("解析链接 %s: 链接无目标信息（daemon 未提供 symlinkTarget）", current)
		}
		if !path.IsAbs(target) {
			// A relative link target is relative to the directory holding the link -
			// not to the caller's cwd, which is a different machine's notion entirely.
			target = path.Join(path.Dir(current), target)
		}
		target = path.Clean(target)
		if target == current {
			// envd >= 0.10.0 answers a chain it cannot resolve (EvalSymlinks failed, e.g.
			// dangling or ELOOP) with the link's own path. The wire cannot tell those two
			// apart, so the adapter refuses to name one of them - and refuses to convert
			// "unknown" into absence, the same discipline that keeps permission from
			// becoming "not found".
			return "", envdclient.Entry{}, fmt.Errorf("解析链接 %s: 链接目标不可解析（悬空或链接环；daemon 只报告解析后的目标）", current)
		}
		current = target
	}
	return "", envdclient.Entry{}, fmt.Errorf("解析链接 %s: 超过 %d 跳上限", filePath, envdLinkHops)
}

// ReadDir lists one level. The entries come back in the target's own order, deliberately
// unsorted: envd's ListDir documents raw directory order including dotfiles, and the seam
// contract says ordering is the caller's business. The core sorts by name, which is what
// makes paging identical on a host listing (sorted by courtesy) and a remote one (raw).
func (a *envdAccess) ReadDir(ctx context.Context, dirPath string) ([]fs.DirEntry, error) {
	entries, err := a.api.ListDir(ctx, dirPath, 1)
	if err != nil {
		return nil, envdError("listdir", dirPath, err)
	}
	out := make([]fs.DirEntry, 0, len(entries))
	for _, entry := range entries {
		one := entry
		out = append(out, fs.FileInfoToDirEntry(envdInfo{entry: one}))
	}
	return out, nil
}

// Open returns a chunked, seekable handle over the target's range reads.
func (a *envdAccess) Open(ctx context.Context, filePath string) (toolcore.FileHandle, error) {
	info, err := a.Stat(ctx, filePath)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("open %s: 目录不能作为文件读取: %w", filePath, fs.ErrInvalid)
	}
	return &envdFile{access: a, ctx: ctx, path: filePath, info: info}, nil
}

// envdFile is a lazily-filled window over one remote file. It never holds more than a
// chunk, which is the property the view core depends on when it windows 20,000-line files.
type envdFile struct {
	access *envdAccess
	ctx    context.Context
	path   string
	info   fs.FileInfo

	offset int64
	buf    []byte
	bufOff int64 // offset of buf[0] within the file
	closed bool
}

func (f *envdFile) Stat() (fs.FileInfo, error) { return f.info, nil }

func (f *envdFile) Close() error {
	f.closed = true
	f.buf = nil
	return nil
}

func (f *envdFile) Read(p []byte) (int, error) {
	if f.closed {
		return 0, fs.ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	size := f.info.Size()
	if f.offset >= size {
		return 0, io.EOF
	}
	if !f.windowCovers(f.offset) {
		if err := f.fetch(f.offset); err != nil {
			return 0, err
		}
	}
	start := int(f.offset - f.bufOff)
	if start >= len(f.buf) {
		// A window shorter than requested means the target had fewer bytes than the
		// size we were told: report EOF rather than looping for a window that will
		// never arrive.
		return 0, io.EOF
	}
	n := copy(p, f.buf[start:])
	f.offset += int64(n)
	return n, nil
}

// windowCovers reports whether the buffered window still holds the requested offset. A
// chunk larger than the file, or a window that ran past the end, both count as covered
// only when the offset is inside the bytes actually returned.
func (f *envdFile) windowCovers(offset int64) bool {
	return len(f.buf) > 0 && offset >= f.bufOff && offset < f.bufOff+int64(len(f.buf))
}

func (f *envdFile) fetch(offset int64) error {
	want := f.access.window()
	if size := f.info.Size(); size > 0 && offset+want > size {
		want = size - offset
	}
	if want <= 0 {
		want = f.access.window()
	}
	read, err := f.access.readRange(f.ctx, f.path, offset, want)
	if err != nil {
		return err
	}
	f.buf = read
	f.bufOff = offset
	return nil
}

// window reports the configured read chunk, falling back to the package default.
func (a *envdAccess) window() int64 {
	if a.chunk > 0 {
		return a.chunk
	}
	return envdReadChunk
}

// readRange pulls one window of bytes. The context travels: the seam's contract is that an
// accessor over a channel honours cancellation and timeouts, and a range read is the only
// call in this file that moves bulk bytes.
func (a *envdAccess) readRange(ctx context.Context, filePath string, offset, size int64) ([]byte, error) {
	read, err := a.api.ReadRange(ctx, filePath, offset, size)
	if err != nil {
		return nil, envdError("range-read", filePath, err)
	}
	if read == nil {
		return nil, fmt.Errorf("range-read %s: 响应为空", filePath)
	}
	return read.Data, nil
}

// Seek repositions without fetching, which is what lets the view core rewind after a BOM
// probe and read the same bytes through the seam.
func (f *envdFile) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = f.offset + offset
	case io.SeekEnd:
		next = f.info.Size() + offset
	default:
		return 0, fmt.Errorf("seek: 非法 whence %d", whence)
	}
	if next < 0 {
		return 0, fmt.Errorf("seek: 负偏移 %d", next)
	}
	f.offset = next
	return next, nil
}
