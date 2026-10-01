package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// W7 激活切片 + Phase 5 变更源 1：aicli（TUI 与 ACP 共用 buildLocalChatAgent →
// applyLocalChatContextOptions）会话装配——mode=on 注入档位 + Layer；
// shadow 只发布 Layer（编辑类工具的变更接收方，绝不注入 prompt）；
// off（默认）/ nil 零新增；与 context/workspace 段是否存在无关。

func activateLocalChatKnowledgeForTest(t *testing.T, mode knowledge.Mode) *knowledge.Activation {
	t.Helper()
	act, err := knowledge.Activate(context.Background(), knowledge.Config{Mode: mode}, t.TempDir(),
		knowledge.ActivationOptions{SkipInitialIndex: true})
	require.NoError(t, err)
	require.NotNil(t, act)
	t.Cleanup(func() { _ = act.Close() })
	return act
}

func TestApplyLocalChatKnowledgeOptionsAssembly(t *testing.T) {
	onAct := activateLocalChatKnowledgeForTest(t, knowledge.ModeOn)

	// 空 context/workspace 段也必须装配（知识层不依赖这两段配置）。
	cfg := &agent.Config{}
	applyLocalChatContextOptions(cfg, &runtimecfg.RuntimeConfig{}, onAct)
	require.Equal(t, contextmgr.KnowledgeModeSignals, cfg.Options["context_knowledge_mode"])
	layer, ok := cfg.Options["context_knowledge_layer"].(*knowledge.Layer)
	require.True(t, ok, "装配必须注入 *knowledge.Layer, got %#v", cfg.Options["context_knowledge_layer"])
	require.Equal(t, knowledge.ModeOn, layer.Mode())

	// runtimeConfig 为 nil 时知识层装配仍然生效（装配顺序先于 context 段读取）。
	nilConfigCfg := &agent.Config{}
	applyLocalChatContextOptions(nilConfigCfg, nil, onAct)
	require.Equal(t, contextmgr.KnowledgeModeSignals, nilConfigCfg.Options["context_knowledge_mode"])
	require.NotNil(t, nilConfigCfg.Options["context_knowledge_layer"])

	// shadow：只建索引、绝不注入 prompt（不设 mode），但发布 layer 句柄——
	// 它是编辑类工具的变更接收方（Phase 5 变更源 1），只让索引保鲜。
	shadowAct := activateLocalChatKnowledgeForTest(t, knowledge.ModeShadow)
	shadowCfg := &agent.Config{}
	applyLocalChatContextOptions(shadowCfg, &runtimecfg.RuntimeConfig{}, shadowAct)
	require.NotContains(t, shadowCfg.Options, "context_knowledge_mode", "shadow 绝不注入 prompt")
	shadowLayer, ok := shadowCfg.Options["context_knowledge_layer"].(*knowledge.Layer)
	require.True(t, ok, "shadow 必须发布 layer 句柄, got %#v", shadowCfg.Options["context_knowledge_layer"])
	require.Equal(t, knowledge.ModeShadow, shadowLayer.Mode())

	// 默认回归：off（nil Activation）且无 context/workspace 段 → Options 保持 nil。
	offCfg := &agent.Config{}
	applyLocalChatContextOptions(offCfg, &runtimecfg.RuntimeConfig{}, nil)
	require.Nil(t, offCfg.Options, "off assembly must stay byte-identical to baseline")
}
