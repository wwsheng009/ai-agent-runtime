package runtimeapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// TestSetAICLIConfig_PreservesSkillsRuntime 是 2026-09-18 修复的回归锁定：
// runtime-server 唯一的配置注入点是 SetAICLIConfig，其快照若丢弃 SkillsRuntime，
// catalog 注入（SK-1/SK-2）与 document_mode/discipline_block/catalog_budget
// 三个灰度开关会静默失效（cfg 恒为 nil，无任何报错）。
func TestSetAICLIConfig_PreservesSkillsRuntime(t *testing.T) {
	handler := NewHandler(runtimeskill.NewRegistry(nil), nil, nil)
	disabled := false
	handler.SetAICLIConfig(&agentconfig.Config{
		AICLI: &agentconfig.AICLIConfig{},
		SkillsRuntime: &agentconfig.SkillsRuntimeConfig{
			DocumentMode:       "auto",
			CatalogBudgetChars: 4321,
			DisciplineBlock:    &disabled,
		},
	})

	cfg := handler.runtimeSkillsConfig()
	require.NotNil(t, cfg, "skills runtime config must survive the AICLI snapshot")
	require.True(t, cfg.DocumentModeAuto())
	require.Equal(t, 4321, cfg.CatalogBudget())
	require.False(t, cfg.DisciplineBlockEnabled())
}

// TestSetAICLIConfig_NilSkillsRuntimeStaysNil 锁定缺省语义：配置本来没有
// SkillsRuntime 时，访问器保持 nil（调用方按"未配置"处理，而不是伪造默认值）。
func TestSetAICLIConfig_NilSkillsRuntimeStaysNil(t *testing.T) {
	handler := NewHandler(runtimeskill.NewRegistry(nil), nil, nil)
	handler.SetAICLIConfig(&agentconfig.Config{AICLI: &agentconfig.AICLIConfig{}})
	require.Nil(t, handler.runtimeSkillsConfig())
}
