package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDefaultToolsForRoleCoversRuntimeTaskVocabulary 钉住 2026-09-30 缺口：
// 模型实际使用的角色词表（spawn_agent agent_type: explore/general/plan；
// spawn_subagents task_type: config/explore/generate/implement/...）此前不在
// DefaultToolsForRole 的精确角色名里，落到 nil（"继承父策略"）的同时能力面也
// 丢掉 exec_shell/network，子代理静默失去 shell。家族归一后这些词都必须命中
// 既有默认表，未知词仍保持 nil（不擅自扩权）。
func TestDefaultToolsForRoleCoversRuntimeTaskVocabulary(t *testing.T) {
	research := DefaultToolsForRole("explore")
	assert.Contains(t, research, toolNameShell)
	assert.Contains(t, research, toolNameFetch)
	assert.Contains(t, research, toolNameWebSearch)
	assert.Equal(t, DefaultToolsForRole("researcher"), research)
	assert.Equal(t, DefaultToolsForRole("researcher"), DefaultToolsForRole("understand"))
	assert.Equal(t, DefaultToolsForRole("researcher"), DefaultToolsForRole("plan"))
	assert.Nil(t, DefaultToolsForRole("plan_planner"), "unknown aliases must not be invented")

	write := DefaultToolsForRole("implement")
	assert.Contains(t, write, toolNameShell)
	assert.Contains(t, write, toolNameEdit)
	assert.Contains(t, write, toolNameApplyPatch)
	assert.Equal(t, DefaultToolsForRole("implementer"), write)
	for _, role := range []string{"generate", "modify", "refactor", "migrate", "integration", "config", "security", "general", "worker"} {
		assert.Equal(t, DefaultToolsForRole("implementer"), DefaultToolsForRole(role), "role %q", role)
	}

	test := DefaultToolsForRole("verify")
	assert.Contains(t, test, toolNameShell)
	assert.Equal(t, DefaultToolsForRole("verifier"), test)
	assert.Equal(t, DefaultToolsForRole("verifier"), DefaultToolsForRole("test"))

	assert.Nil(t, DefaultToolsForRole(""))
	assert.Nil(t, DefaultToolsForRole("banana"))
}
