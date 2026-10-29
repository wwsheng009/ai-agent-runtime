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

// Open deliberately returns the *os.File unchanged rather than adapting it: the
// handle must stay seekable and streaming, because the view core probes a BOM,
// rewinds, and then windows the file line by line.
func (osAccess) Open(_ context.Context, path string) (toolcore.FileHandle, error) {
	return os.Open(path)
}

func (osAccess) ReadDir(_ context.Context, path string) ([]fs.DirEntry, error) {
	return os.ReadDir(path)
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

// _ proves the move adds no adapter: the operating system's own file type is
// already a valid seam handle.
var _ toolcore.FileHandle = (*os.File)(nil)
