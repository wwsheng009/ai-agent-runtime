package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// 回归：启动路径（DiscoverOnly + 热加载，即 aicli chat 宿主形态）遇到非标准
// skill 文件（缺 YAML frontmatter 的 SKILL.md）时，NewManager 不得失败——
// 只收集告警供 chat 宿主投递动态状态栏，合法邻居照常注册。
// 2026-10-18 deskctl 实例："能力面初始化失败: [SKILL_LOAD_FAILED]
// failed to discover skill file: ...SKILL.md: ... missing YAML frontmatter"。
func TestManager_NewManager_NonStandardSkillWarnsWithoutFailing(t *testing.T) {
	skillDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# desktop-basic\n\nno frontmatter here\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(`name: bootstrap-non-standard-neighbor
description: valid neighbor skill
triggers:
  - type: keyword
    values: ["boot"]
    weight: 1
`), 0o644))

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.HotReload.Enabled = true
	cfg.HotReload.DebounceDelay = 50 * time.Millisecond

	manager, err := NewManager(&Options{
		Config:          cfg,
		SkillDirs:       []string{skillDir},
		DiscoverOnly:    true,
		EnableHotReload: true,
		MCPManager:      &bootstrapMCPManager{},
		ResourceManager: &bootstrapResourceManager{},
	})
	require.NoError(t, err, "non-standard skill files must not fail capability surface init")
	t.Cleanup(func() { _ = manager.Stop() })

	item, ok := manager.Registry().Get("bootstrap-non-standard-neighbor")
	require.True(t, ok, "valid neighbor skill must still be registered")
	require.NotNil(t, item)

	warnings := manager.TakeSkillLoadWarnings()
	require.NotEmpty(t, warnings)
	require.Contains(t, strings.Join(warnings, "\n"), "SKILL.md")

	// 取走后清空：避免同一告警被动态栏重复投递。
	require.Empty(t, manager.TakeSkillLoadWarnings())
}
