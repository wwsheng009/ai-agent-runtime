package remotetools

// envd_guarded_access.go aligns the seam's reads with the same target-user
// readability gate the bespoke tools already apply.
//
// The daemon's /files endpoint opens files as the daemon process (measured: root),
// while the process channel runs as cfg.User. readGuarded closed that gap for view /
// edit / apply_patch; the shared kernels read through the FileAccess seam instead, so
// without the same gate remote_x__grep would return the contents of a file that
// remote_x__shell cannot open - a privilege asymmetry, and a difference from the
// local backend where the kernel refuses os.Open. Measured on a live 0.2.11 against
// a real target: a 0600 root file inside the workspace came back through grep
// before this decorator existed, while view of the same file was already refused.
//
// This is not a second *path* policy: jailing and authorization still happen before
// a core reaches the accessor (toolcore.Access: implementations must not resolve,
// jail-check or authorize). It answers the other question a transport must answer
// itself - *as whom* the bytes travel - with the same kernel probe the tools use
// (test -r by the target user) and the same world-readable fast path, so one
// implementation of the verdict serves both surfaces.
//
// The verdict is cached per call: one search may open the same file twice (a page
// and its context lines), and the probe is a process spawn.

import (
	"context"
	"fmt"
	"io/fs"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

type envdGuardedAccess struct {
	inner *envdAccess
	be    *envdBackend

	mu   sync.Mutex
	ok   map[string]bool
	deny map[string]error
}

// compile-time assertions: the wrapper must preserve every optional capability the
// adapter answers, or a core would lose a feature merely because the runtime chose
// the guarded path.
var (
	_ toolcore.Access          = (*envdGuardedAccess)(nil)
	_ toolcore.SymlinkResolver = (*envdGuardedAccess)(nil)
	_ toolcore.TargetPathStyle = (*envdGuardedAccess)(nil)
)

func newEnvdGuardedAccess(be *envdBackend) toolcore.Access {
	return &envdGuardedAccess{
		inner: &envdAccess{api: be.client},
		be:    be,
		ok:    map[string]bool{},
		deny:  map[string]error{},
	}
}

func (g *envdGuardedAccess) Stat(ctx context.Context, path string) (fs.FileInfo, error) {
	return g.inner.Stat(ctx, path)
}

func (g *envdGuardedAccess) Lstat(ctx context.Context, path string) (fs.FileInfo, error) {
	return g.inner.Lstat(ctx, path)
}

func (g *envdGuardedAccess) ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error) {
	return g.inner.ReadDir(ctx, path)
}

func (g *envdGuardedAccess) PathSeparator() rune { return g.inner.PathSeparator() }

func (g *envdGuardedAccess) EvalLinkPath(ctx context.Context, path string) (string, error) {
	return g.inner.EvalLinkPath(ctx, path)
}

// Open gates first, then hands out the same chunked handle the adapter builds.
//
// The Stat is shared with the gate on purpose: envdAccess.Open would stat again,
// and a walk opens many files - one RPC fewer per file matters there. The
// directory message mirrors the adapter's, because this function replaces it on
// the guarded path.
func (g *envdGuardedAccess) Open(ctx context.Context, path string) (toolcore.FileHandle, error) {
	info, err := g.inner.Stat(ctx, path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("open %s: 目录不能作为文件读取: %w", path, fs.ErrInvalid)
	}
	if err := g.checkReadable(ctx, path, info); err != nil {
		return nil, err
	}
	return &envdFile{access: g.inner, ctx: ctx, path: path, info: info}, nil
}

// checkReadable returns nil when the target user may open the path, and a
// permission error (never IsNotExist) when it may not. Results are memoized for
// the lifetime of this accessor, which is one call.
func (g *envdGuardedAccess) checkReadable(ctx context.Context, path string, info fs.FileInfo) error {
	own, ok := info.(envdInfo)
	if !ok {
		// Not this adapter's FileInfo: there is nothing to probe against. Treat it
		// as a wiring bug rather than a free pass.
		return fmt.Errorf("read %s: 访问器返回未知的 FileInfo 类型 %T，无法核对目标用户读权限", path, info)
	}
	g.mu.Lock()
	if g.ok[path] {
		g.mu.Unlock()
		return nil
	}
	if err, denied := g.deny[path]; denied {
		g.mu.Unlock()
		return err
	}
	g.mu.Unlock()

	err := g.be.targetCannotRead(ctx, path, own.entry)
	if err == nil {
		g.mu.Lock()
		g.ok[path] = true
		g.mu.Unlock()
		return nil
	}
	g.mu.Lock()
	g.deny[path] = err
	g.mu.Unlock()
	return err
}
