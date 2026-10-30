package searchcore

import (
	"context"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/localio"
)

// seamAccess resolves "no accessor given" to this process's filesystem, in one place.
//
// Every options struct in this package carries an Access field, so without a single
// definition each entry point would hand-roll its own nil check and the meaning of
// "unset" would drift - while the whole point of leaving it unset is precisely that an
// unconverted caller keeps the behaviour it always had.
func seamAccess(access toolcore.Access) toolcore.Access {
	if access == nil {
		return localio.Access
	}
	return access
}

// seamAccessIsLocal answers the one question a shared core may ask about *where* an
// accessor points: does it serve this process's own filesystem?
//
// It exists for facts that are properties of the host rather than of the files being
// searched, and which therefore have no honest answer on a remote accessor. The global
// gitignore is the first such fact: it lives under a HOME, and HOME belongs to whoever
// runs the tool, not to the machine holding the matches. The predicate is asked of
// localio because only that package knows its own implementation type; see IsLocal.
func seamAccessIsLocal(access toolcore.Access) bool {
	return localio.IsLocal(seamAccess(access))
}

// FilesystemIdentityFunc is the injection seam one_file_system uses to decide whether a
// directory still belongs to the search root's filesystem. Its default is localio's own
// host answer, so moving a mount probe in a test does not require mount privileges -
// which is how both this package's and the local tool's tests exercise the boundary.
//
// It is deliberately not a second implementation: the function below is the only host
// answer, and this variable points at it.
var FilesystemIdentityFunc = localio.FilesystemIdentity

// grepFilesystemIdentity answers "which filesystem is this path on" for one search, and it
// is the only place that decides who answers.
//
// The order is: this process's own filesystem asks the host function above - which is also
// the documented seam every existing test injects - and anything else asks the accessor,
// through an optional capability that a backend may simply not have. The last branch is the
// point of design §2 Q2: a transport that cannot report a device must not have the boundary
// inferred for it. Guessing from path shape would either search a mount the caller excluded
// or stop short of the tree they asked for, and either way the result would look normal.
// Declining is the honest answer, and dropping one_file_system is the workaround.
func grepFilesystemIdentity(ctx context.Context, access toolcore.Access, path string) (string, error) {
	if seamAccessIsLocal(access) {
		return FilesystemIdentityFunc(path)
	}
	identifier, ok := access.(toolcore.FilesystemIdentifier)
	if !ok {
		return "", fmt.Errorf("one_file_system 需要 accessor 实现 toolcore.FilesystemIdentifier（%T 未提供该能力）：无法判断 %s 是否越出搜索根所在的文件系统，请去掉 one_file_system", access, path)
	}
	return identifier.FilesystemIdentity(ctx, path)
}
