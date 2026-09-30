package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// 三入口之一（runtime-server 启动装配）：mode=off / 未接入（nil 句柄）时
// Recorder 必须为 nil（零写入）；mode=shadow 且本进程为 owner 时可装配采集器。
func TestBootRuntimeServerKnowledgeRecorderAssembly(t *testing.T) {
	require.Nil(t, knowledgeRecorderFor(nil), "未接入（nil）时采集器必须为 nil")

	offCfg := &runtimecfg.RuntimeConfig{}
	offCfg.Knowledge = knowledge.Config{Mode: knowledge.ModeOff}
	require.Nil(t, bootRuntimeServerKnowledge(offCfg, ""), "off 不得接入知识层")
	require.Nil(t, bootRuntimeServerKnowledge(nil, ""))

	root := t.TempDir()
	shadowCfg := &runtimecfg.RuntimeConfig{}
	shadowCfg.Knowledge = knowledge.Config{Mode: knowledge.ModeShadow}
	shadowCfg.Workspace.Root = root
	act := bootRuntimeServerKnowledge(shadowCfg, "")
	require.NotNil(t, act)
	t.Cleanup(func() { _ = act.Close() })
	require.NotNil(t, knowledgeRecorderFor(act), "shadow + owner 时 runtime-server 入口必须可装配采集器")
	require.Same(t, knowledgeRecorderFor(act), act.Recorder(), "装配口复用 Activation 的同一采集器实例")
}
