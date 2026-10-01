package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// W7 激活切片 + Phase 5 变更源 1：runtimeapi / runtime-server 会话装配——
// mode=on 时注入 `context_knowledge_mode=signals` + `*knowledge.Layer`；
// shadow 只发布 Layer（编辑类工具的变更接收方，绝不注入 prompt）；
// off（默认）/ 激活失败（nil）零新增，options 与改动前逐字节一致。

func activateKnowledgeAssemblyForTest(t *testing.T, mode knowledge.Mode) *knowledge.Activation {
	t.Helper()
	root := t.TempDir()
	act, err := knowledge.Activate(context.Background(), knowledge.Config{Mode: mode}, root,
		knowledge.ActivationOptions{SkipInitialIndex: true})
	require.NoError(t, err)
	require.NotNil(t, act)
	t.Cleanup(func() { _ = act.Close() })
	return act
}

func TestContextOptionsFromRuntimeConfigKnowledgeAssembly(t *testing.T) {
	config := &runtimecfg.RuntimeConfig{}
	config.Knowledge = knowledge.Config{Mode: knowledge.ModeOn}

	onAct := activateKnowledgeAssemblyForTest(t, knowledge.ModeOn)
	onOptions := contextOptionsFromRuntimeConfig(config, onAct)
	require.Equal(t, "signals", onOptions["context_knowledge_mode"])
	layer, ok := onOptions["context_knowledge_layer"].(*knowledge.Layer)
	require.True(t, ok, "装配必须注入 *knowledge.Layer（版本观测），got %#v", onOptions["context_knowledge_layer"])
	require.Equal(t, knowledge.ModeOn, layer.Mode())

	// shadow：ModeShadow 契约是"绝不注入 prompt"（不设 mode），但发布 layer 句柄——
	// 它是编辑类工具的变更接收方（Phase 5 变更源 1），只让索引保鲜。
	shadowAct := activateKnowledgeAssemblyForTest(t, knowledge.ModeShadow)
	shadowOptions := contextOptionsFromRuntimeConfig(config, shadowAct)
	require.NotContains(t, shadowOptions, "context_knowledge_mode", "shadow 绝不注入 prompt")
	shadowLayer, ok := shadowOptions["context_knowledge_layer"].(*knowledge.Layer)
	require.True(t, ok, "shadow 必须发布 layer 句柄, got %#v", shadowOptions["context_knowledge_layer"])
	require.Equal(t, knowledge.ModeShadow, shadowLayer.Mode())

	// off / 激活失败（nil）：零新增（与改动前逐字节一致）。
	require.Nil(t, contextOptionsFromRuntimeConfig(config, nil))
	require.NotContains(t, contextOptionsFromRuntimeConfig(config, nil), "context_knowledge_layer")
}

// runtime-server 的装配路径：main.go 只做 SetKnowledgeActivation，会话 options
// 由 handler 字段消费——本用例把这两步串起来（等价于 main.go:1317-1324）。
func TestHandlerKnowledgeActivationFlowsIntoSessionOptions(t *testing.T) {
	config := &runtimecfg.RuntimeConfig{}
	config.Knowledge = knowledge.Config{Mode: knowledge.ModeOn}
	onAct := activateKnowledgeAssemblyForTest(t, knowledge.ModeOn)

	handler := &Handler{}
	handler.SetKnowledgeActivation(onAct)
	options := contextOptionsFromRuntimeConfig(config, handler.knowledgeActivation)
	require.Equal(t, "signals", options["context_knowledge_mode"])
	require.NotNil(t, options["context_knowledge_layer"])
}
