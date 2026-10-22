package remotetools

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// artifactFacade keeps the direct-handler tests (namespace rejection) while
// production registers the shared toolcore Handler.
type artifactFacade struct {
	handler   toolcore.Handler
	cfg       *Config
	workspace *Workspace
	artifacts *ArtifactStore
}

func (f *artifactFacade) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	runtime := newRemoteRuntime(ctx, f.cfg, f.workspace, nil, f.artifacts).(*remoteRuntime)
	result, err := f.handler.Execute(ctx, &toolcore.Call{Args: params, Runtime: runtime})
	if err != nil {
		return nil, err
	}
	return toolkit.ResultToToolkit(result), nil
}

func newRemoteArtifactReadTool(cfg *Config, workspace *Workspace, artifacts *ArtifactStore) *artifactFacade {
	return &artifactFacade{
		handler:   toolcore.NewArtifactReadHandler(toolcore.Definition{Name: ToolArtifactRead}),
		cfg:       cfg,
		workspace: workspace,
		artifacts: artifacts,
	}
}
