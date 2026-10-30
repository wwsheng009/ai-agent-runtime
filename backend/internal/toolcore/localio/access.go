// Package localio is the machine-local implementation of the tool-layer I/O seam
// (toolcore.Access), plus the resolver every shared core uses to pick one.
//
// This package is a MOVE, not a layer. Each method body below is exactly the
// os.* call the shared cores used to make inline (design §4.4: "localAccess 不是
// 新写的抽象层，是搬家"). That identity is the point of Phase 2.5a: the seam is
// judged on nothing but "did behaviour change", and the answer is constructed to
// be no - the view and ls unit tests were not edited and still pass.
//
// The compile-time assertion at the bottom is part of that argument: *os.File
// satisfies toolcore.FileHandle as-is, so the local path allocates no wrapper.
package localio

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// Access serves the filesystem of the machine this process runs on. It is
// stateless, so the shared value is safe for concurrent use - the property that
// lets a core hold it without a lock or a per-call construction.
var Access toolcore.Access = osAccess{}

type osAccess struct{}

func (osAccess) Stat(_ context.Context, path string) (fs.FileInfo, error) {
	return os.Stat(path)
}

// Lstat is the call filepath.WalkDir made at the root, kept separate from Stat so a
// listing cannot follow its own root out of the authorized tree.
func (osAccess) Lstat(_ context.Context, path string) (fs.FileInfo, error) {
	return os.Lstat(path)
}

// Open deliberately returns the *os.File unchanged rather than adapting it: the
// handle must stay seekable and streaming, because the view core probes a BOM,
// rewinds, and then windows the file line by line.
func (osAccess) Open(_ context.Context, path string) (toolcore.FileHandle, error) {
	return os.Open(path)
}

func (osAccess) ReadDir(_ context.Context, path string) ([]fs.DirEntry, error) {
	return os.ReadDir(path)
}

// EvalLinkPath is the host's own canonical form for one path, which is what
// filepath.EvalSymlinks has always returned for a search running on this machine.
//
// It lives here rather than in a core on purpose: a core must not be able to ask
// "which path is this" of the filesystem, because that question has one authority in the
// tool layer (design §4.2) and this method exists only so that following links can
// terminate. A remote backend that cannot answer it does not implement the interface, and
// the feature is declined rather than approximated - see toolcore.SymlinkResolver.
func (osAccess) EvalLinkPath(_ context.Context, path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

// For resolves the accessor a core should use for one call: the Runtime's when it
// provides one, otherwise the local filesystem.
//
// The default is not a fallback hack. It is what makes the migration incremental:
// a surface that has not been converted keeps the behaviour it always had, so
// "nothing changed" is the expected result for every core that was not touched,
// and a regression can only come from the core being converted.
func For(rt toolcore.Runtime) toolcore.Access {
	if rt == nil {
		return Access
	}
	if provider, ok := rt.(toolcore.FileAccessProvider); ok {
		if access := provider.FileAccess(); access != nil {
			return access
		}
	}
	return Access
}

// IsLocal reports whether an accessor serves the filesystem of the machine running
// this process, as opposed to a machine the caller pointed it at.
//
// It is a type assertion and deliberately not `access == Access`: interface
// comparison panics at run time when the dynamic type is not comparable, so an
// osAccess that ever grows a slice or map field would turn a quiet predicate into a
// crash in whichever core asked it. Only this package can answer the question, since
// only it knows its own implementation type.
//
// The reason a core is allowed to ask at all: some facts are properties of the
// machine running the tool rather than of the files being read, and they have no
// answer on a remote accessor. A wrapped local accessor is reported as not-local on
// purpose - wrapping means something is being intercepted, so the core must not
// claim it is talking to its own host.
func IsLocal(access toolcore.Access) bool {
	_, ok := access.(osAccess)
	return ok
}

// _ proves the move adds no adapter: the operating system's own file type is
// already a valid seam handle.
var _ toolcore.FileHandle = (*os.File)(nil)

// _ proves the host accessor carries the link capability, so a core that declines
// follow mode when it is missing never declines on this machine by accident.
var _ toolcore.SymlinkResolver = osAccess{}

// _ proves the host accessor also answers the filesystem-boundary question, for the same
// reason: a core that declines one_file_system without it must never decline here.
var _ toolcore.FilesystemIdentifier = osAccess{}
