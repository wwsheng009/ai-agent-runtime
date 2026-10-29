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
	"io"
	"io/fs"
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
