package agent

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Phase 5 变更源 1（edit hook）的 agent 侧测试：工具执行 ctx 必须携带会话知识层的
// 变更接收方（`context_knowledge_layer` 实现 knowledge.ChangeNotifier 时），
// 未挂载知识层时不得注入（off 基线零副作用）。

type recordingChangeNotifier struct{ paths []string }

func (r *recordingChangeNotifier) MarkChanged(paths ...string) {
	r.paths = append(r.paths, paths...)
}

func TestToolCallContextsBindKnowledgeChangeNotifier(t *testing.T) {
	notifier := &recordingChangeNotifier{}
	agent := &Agent{config: &Config{Options: map[string]interface{}{
		"context_knowledge_layer": notifier,
	}}}

	contexts := map[string]context.Context{
		"toolCallContext":         toolCallContext(context.Background(), []types.ToolCall{}, "", nil, agent, "session-1", 0),
		"approvedToolCallContext": approvedToolCallContext(context.Background(), agent),
	}
	for name, ctx := range contexts {
		bound := toolctx.FileChangeNotifierFromContext(ctx)
		if bound == nil {
			t.Fatalf("%s: 变更接收方必须绑定", name)
		}
		bound("demo/a.go")
	}
	if len(notifier.paths) != len(contexts) {
		t.Fatalf("接收方调用记录不符: %v", notifier.paths)
	}
	for _, p := range notifier.paths {
		if p != "demo/a.go" {
			t.Fatalf("接收方路径不符: %v", notifier.paths)
		}
	}
}

func TestToolCallContextWithoutKnowledgeLayerHasNoNotifier(t *testing.T) {
	cases := map[string]*Agent{
		"未配置":       {config: &Config{}},
		"nil agent": nil,
		"类型不符": {config: &Config{Options: map[string]interface{}{
			"context_knowledge_layer": "not-a-notifier",
		}}},
	}
	for name, agent := range cases {
		ctx := approvedToolCallContext(context.Background(), agent)
		if got := toolctx.FileChangeNotifierFromContext(ctx); got != nil {
			t.Fatalf("%s: 不应绑定变更接收方", name)
		}
		ctx = toolCallContext(context.Background(), []types.ToolCall{}, "", nil, agent, "session-1", 0)
		if got := toolctx.FileChangeNotifierFromContext(ctx); got != nil {
			t.Fatalf("%s: toolCallContext 不应绑定变更接收方", name)
		}
	}
}
