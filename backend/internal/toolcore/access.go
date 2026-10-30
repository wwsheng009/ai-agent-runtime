package toolcore

// access.go is the file-I/O seam described in
// docs/plan/tool-layer-io-seam-design-20261028.md §4.2, Phase 2.5a.
//
// Why it exists: a shared tool core that calls os.Stat can only describe the
// machine it runs on. That single assumption is what forced the envd backend to
// grow a private copy of every file tool, and every copy is one more place where
// two backends can disagree about the same file. The seam adds no feature; it
// removes the ability to reach the host filesystem from inside a core, which the
// driftguard ratchet then measures.
//
// What this phase deliberately leaves out, and why:
//
//   - Write/MkdirAll/Remove/Rename: their call sites still use os.* inline
//     (Phase 3.5a moves them). Declaring them now would add interface surface
//     with no caller, and an unprobed method is only a guess at the shape a real
//     second backend can satisfy.
//   - Path identity (Resolve/Relative): that question already has a seam.
//     Paths() and Permissions() run before any data access (design §4.1
//     discipline 1), and re-declaring it here would create a second authority
//     over the same path - the exact failure mode this refactor is meant to end.
//   - RandomRead: it lands with its first real consumer, when the envd view
//     switches from a doubling prefix fetch to Client.ReadRange (Phase 2.5b).
//
// The vocabulary is io/fs rather than an invented mirror: fs.FileInfo and
// fs.DirEntry are already backend-neutral, and *os.File satisfies FileHandle
// exactly, so the local implementation is a move and not a wrapper.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// Access is the read side of the file-I/O seam. Every method takes a context so
// an accessor that performs I/O over a channel (an HTTP or RPC round trip) can
// honour cancellation and timeouts; the local implementation ignores it, because
// a syscall cannot be cancelled and pretending otherwise would be decorative.
//
// Implementations must not resolve, jail-check or authorize the path: the core
// did that before reaching here (design §4.1 discipline 1), and a second check
// inside the accessor would be a second policy.
type Access interface {
	// Stat describes one already-authorized path. A missing path must return an
	// error that IsNotExist reports as absent; a permission problem must NOT be
	// reported as absent, or a core would answer "file not found" for a file that
	// exists and turn a recovery hint into a lie.
	Stat(ctx context.Context, path string) (fs.FileInfo, error)

	// Lstat describes the path itself rather than what it points at. It exists
	// because a core that decides whether to *descend* must not follow a symlink:
	// filepath.WalkDir used Lstat at the root, and a listing that follows a
	// directory symlink out of the authorized tree turns a jail into a suggestion.
	// The two methods are deliberately separate - whichever one a caller needs is a
	// statement about the caller's safety obligation, not a preference.
	Lstat(ctx context.Context, path string) (fs.FileInfo, error)

	// Open returns a streamable handle. It must not buffer the whole file: a core
	// that pages through 20,000 lines relies on reading only what it windows.
	Open(ctx context.Context, path string) (FileHandle, error)

	// ReadDir lists one directory level without descending. Ordering is the
	// caller's business; the local implementation returns the OS order, which is
	// sorted on most platforms and re-sorted by each core anyway.
	ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error)
}

// FileHandle is an open file a core may stream from.
//
// Seek is part of the contract because the encoding probe reads a BOM and then
// rewinds: a handle that cannot seek would either lose the first four bytes or
// force a whole-file read, and both change what the window shows. *os.File
// satisfies this interface exactly, so the local path allocates no wrapper.
type FileHandle interface {
	io.Reader
	io.Seeker
	Stat() (fs.FileInfo, error)
	io.Closer
}

// FileAccessProvider is the optional Runtime extension that hands a core a
// non-local accessor. A Runtime that does not implement it gets the local
// filesystem, which is what makes the migration incremental: an unconverted
// surface keeps byte-identical behaviour, and the rollback of any one step is the
// deletion of one injection.
type FileAccessProvider interface {
	FileAccess() Access
}

// SymlinkResolver is the optional capability that answers, for one already-authorized
// path, which existing path a chain of symbolic links leads to.
//
// It is an optional extension and deliberately NOT a method of Access. Access is the read
// vocabulary: bytes, one directory level, the shape of a path. Link *identity* is not
// another read - it is the question "which path is this", and the tool layer keeps exactly
// one authority for path identity and authorization (Paths()/Permissions(), design §4.2).
// Putting EvalSymlinks into Access would create a second one, and two authorities over the
// same path is the shape of the bug this whole refactor exists to end.
//
// What a consumer may use it for is narrower than what an implementation may answer: cycle
// avoidance while following links. It must never be consulted to decide whether a path is
// allowed - that answer stays with the authorizing resolver, and a core that asked this one
// would be re-deriving policy from a filesystem hint.
//
// A backend that cannot answer it simply does not implement the interface, and the feature
// that needs it is then declined in as many words. Approximating it - skipping the cycle
// check, or treating path+size+mtime as an identity - would trade a refusal for a wrong
// answer, which is the trade this seam refuses everywhere.
type SymlinkResolver interface {
	EvalLinkPath(ctx context.Context, path string) (string, error)
}

// FilesystemIdentifier is the optional capability that answers which filesystem - device,
// volume, mount - a path lives on.
//
// It exists for one question and one only: whether a directory is still inside the
// filesystem the search root sits on, which is what a caller asking to stay on one file
// system needs before the walk descends into a mount it never meant to enter.
//
// Read the name carefully: this is a *filesystem* id, not a file id. It says nothing about
// whether two paths are the same file, and no consumer may use it to deduplicate - the
// local answer is st_dev on Unix and a volume name on Windows, and neither identifies an
// inode. A backend whose transport cannot report a device simply does not implement the
// interface, and the feature that needs it is declined: guessing the boundary from
// path shape would either search a mount the caller excluded or stop short of the tree they
// asked for, and both look like a normal result.
type FilesystemIdentifier interface {
	FilesystemIdentity(ctx context.Context, path string) (string, error)
}

// TargetPathStyle is the optional capability that says how the filesystem behind an
// accessor spells a child path.
//
// Why a core needs to ask at all: a walk builds its child paths itself - it joins a
// directory it just listed with the name of the entry it just saw - and before the seam
// there was only ever one machine, so this process's own separator was always right. With
// a foreign target it is not: a Windows client walking a Linux tree builds
// `\srv\work\a.txt`, and every stat against the daemon misses. The symptom is not an error
// but an empty result, which is the kind of wrong answer the seam exists to eliminate.
//
// The default is deliberately this process's own convention, so a core that has no
// accessor, or an accessor that has not implemented this, behaves exactly as it did before
// (localio is the host, and the backends that talk to a POSIX target are expected to
// implement it as they are converted).
type TargetPathStyle interface {
	// PathSeparator is the separator the target's filesystem uses: '/' for a POSIX
	// target, the host's separator for a host target.
	PathSeparator() rune
}

// JoinTargetPath joins a directory with one child name the way the accessor's filesystem
// spells it. It is the single definition of "how a core builds a child path", so a walk
// cannot accidentally use the process's convention for a foreign tree.
func JoinTargetPath(access Access, dir, name string) string {
	if style, ok := access.(TargetPathStyle); ok && style.PathSeparator() == '/' {
		return path.Join(dir, name)
	}
	return filepath.Join(dir, name)
}

// The rest of this family exists because the ignore subsystem asks more of a path than
// "join this with that": it walks the ancestor chain, scopes a rule to the directory its
// file lived in, and computes paths relative to that scope. Each of those is the same
// host-or-target question, and answering any of them with the host's rules is how a remote
// `.gitignore` goes silently unapplied - no error, just files the target's own repository
// excludes showing up in the page.

// TargetSeparator is the separator string the target uses, for code that builds a prefix
// check by hand (the ignore chain does, to decide whether a path is still under the root).
func TargetSeparator(access Access) string {
	if style, ok := access.(TargetPathStyle); ok && style.PathSeparator() == '/' {
		return "/"
	}
	return string(filepath.Separator)
}

// DirTargetPath is path.Dir or filepath.Dir, depending on whose paths these are.
func DirTargetPath(access Access, p string) string {
	if style, ok := access.(TargetPathStyle); ok && style.PathSeparator() == '/' {
		return path.Dir(p)
	}
	return filepath.Dir(p)
}

// IsAbsTargetPath reports whether a path is absolute *on the target*, which is not the same
// question as filepath.IsAbs on this process: "/srv/work/x" is absolute to a POSIX daemon and
// merely "slash-leading" to Windows. The caller-visible version of getting this wrong is an
// ignore_file or pattern_file that exists on the target being anchored onto the search root
// and reported as unreadable.
func IsAbsTargetPath(access Access, p string) bool {
	if style, ok := access.(TargetPathStyle); ok && style.PathSeparator() == '/' {
		return path.IsAbs(p)
	}
	return filepath.IsAbs(p)
}

// NormalizeTargetPath puts a path from a *tool's own output* back into the target's spelling.
// ripgrep prints paths with slashes in the places the matcher wants them (the glob engine
// normalises exactly that way before matching), and a consumer that then stores or joins the
// path has to convert back to the spelling its filesystem uses: filepath.FromSlash for a host
// target, and nothing at all for a POSIX one - converting there would turn "/srv/work/a.txt"
// into "\srv\work\a.txt", which the daemon has never heard of.
func NormalizeTargetPath(access Access, p string) string {
	if style, ok := access.(TargetPathStyle); ok && style.PathSeparator() == '/' {
		return p
	}
	return filepath.FromSlash(p)
}

// CleanTargetPath is path.Clean or filepath.Clean.
func CleanTargetPath(access Access, p string) string {
	if style, ok := access.(TargetPathStyle); ok && style.PathSeparator() == '/' {
		return path.Clean(p)
	}
	return filepath.Clean(p)
}

// RelTargetPath is filepath.Rel for a host target and a self-contained POSIX equivalent for
// a foreign one. The POSIX half is written here rather than delegated because the path
// package has no Rel: Go's only Rel is separator-aware for the running process, so feeding
// it "/srv/work" on Windows is exactly the mistake this family exists to prevent.
func RelTargetPath(access Access, base, target string) (string, error) {
	style, ok := access.(TargetPathStyle)
	if !ok || style.PathSeparator() != '/' {
		return filepath.Rel(base, target)
	}
	return posixRel(base, target)
}

func posixRel(base, target string) (string, error) {
	base, target = path.Clean(base), path.Clean(target)
	if base == target {
		return ".", nil
	}
	if path.IsAbs(base) != path.IsAbs(target) {
		return "", fmt.Errorf("rel %q: 与 %q 一个是绝对路径、一个是相对路径", target, base)
	}
	baseParts, targetParts := splitPosix(base), splitPosix(target)
	common := 0
	for common < len(baseParts) && common < len(targetParts) && baseParts[common] == targetParts[common] {
		common++
	}
	parts := make([]string, 0, len(baseParts)-common+len(targetParts)-common)
	for i := common; i < len(baseParts); i++ {
		parts = append(parts, "..")
	}
	parts = append(parts, targetParts[common:]...)
	if len(parts) == 0 {
		return ".", nil
	}
	return path.Join(parts...), nil
}

// splitPosix turns "/a/b" into ["a", "b"] and "a" into ["a"]; the leading slash is not a
// component, which is what makes the common-prefix walk above correct for absolute paths.
func splitPosix(p string) []string {
	trimmed := strings.TrimPrefix(p, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

// NotExistError is the hook a backend whose transport reports absence with its
// own error type (an RPC status, an HTTP 404) implements so that a single
// classification in the core covers both the local and the remote answer.
type NotExistError interface {
	error

	// NotExist reports that the path is absent - not that it is unreachable,
	// denied, or malformed.
	NotExist() bool
}

// IsNotExist classifies an accessor error as "this path is not there".
//
// It replaces the os.IsNotExist calls the cores used to make inline, and it is
// the only place where that judgement lives: an error travelling over a second
// transport must not acquire a second notion of absence. Local errors match
// through fs.ErrNotExist; a remote accessor contributes NotExistError.
func IsNotExist(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	var absent NotExistError
	if errors.As(err, &absent) {
		return absent.NotExist()
	}
	return false
}
