package tools

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

// newCodeIndexResolver is the test-only convenience form of the shared-client
// aware production constructor.
func newCodeIndexResolver(cfg knowledge.Config, workspaceRoot string) tools.CodeIndexResolver {
	return newCodeIndexResolverWithShared(cfg, workspaceRoot, nil)
}
