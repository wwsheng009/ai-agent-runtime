package remotetools

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/ripgrep"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

// ripgrepRunner binds the resolver-backed ripgrep binary to the shared
// searchcore engine. All flag building, JSON parsing and paging live in that
// engine, so this surface keeps only the binary lookup.
type ripgrepRunner struct {
	path string
}

func resolveRipgrepRunner() ripgrepRunner {
	resolved, err := ripgrep.Resolve()
	if err != nil {
		return ripgrepRunner{}
	}
	return ripgrepRunner{path: strings.TrimSpace(resolved.Path)}
}

func (r ripgrepRunner) available() bool {
	return r.path != ""
}

func (r ripgrepRunner) engine() searchcore.Ripgrep {
	return searchcore.Ripgrep{Path: r.path}
}

func (r ripgrepRunner) glob(ctx context.Context, opts searchcore.GlobOptions) (searchcore.GlobResult, bool, error) {
	return r.engine().Glob(ctx, opts)
}

func (r ripgrepRunner) grep(ctx context.Context, opts searchcore.GrepOptions) (searchcore.GrepResult, bool, error) {
	return r.engine().Grep(ctx, opts)
}
