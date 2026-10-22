package remotetools

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// searchFacade lets the engine-selection tests inject a ripgrep runner without
// rebuilding a surface tool. Production now registers the shared searchcore
// handlers; only the test seam lives here.
type searchFacade struct {
	handler   toolcore.Handler
	cfg       *Config
	workspace *Workspace
	rg        ripgrepRunner
}

func (f *searchFacade) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	runtime := newRemoteRuntime(ctx, f.cfg, f.workspace, nil, nil).(*remoteRuntime)
	runtime.search = f.rg
	result, err := f.handler.Execute(ctx, &toolcore.Call{Args: params, Runtime: runtime})
	if err != nil {
		return nil, err
	}
	return toolkit.ResultToToolkit(result), nil
}

func newRemoteGrepTool(cfg *Config, workspace *Workspace) *searchFacade {
	return &searchFacade{
		handler:   searchcore.NewGrepHandler(toolcore.Definition{Name: ToolGrep}),
		cfg:       cfg,
		workspace: workspace,
	}
}

func newRemoteGlobTool(cfg *Config, workspace *Workspace) *searchFacade {
	return &searchFacade{
		handler:   searchcore.NewGlobHandler(toolcore.Definition{Name: ToolGlob}),
		cfg:       cfg,
		workspace: workspace,
	}
}
