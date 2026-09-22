package modelrouting

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// v4（plan §5/§8.1「task_type floor（P4）」表驱动）：floor 只认显式 task_type，
// 经 rank-max 抬档并写 difficulty_floor_by_task_type:<t> 证据；off 模式不抬档。
func TestResolveExplicitDifficulty_TaskTypeFloor(t *testing.T) {
	cases := []struct {
		name     string
		taskType string
		wantDiff string
		wantWarn string
	}{
		{"migrate 缺省 easy → hard", TaskTypeMigrate, DifficultyHard, "difficulty_floor_by_task_type:migrate"},
		{"security 同高信号底 → hard", TaskTypeSecurity, DifficultyHard, "difficulty_floor_by_task_type:security"},
		{"verify 底 normal（不抬 hard）", TaskTypeVerify, DifficultyNormal, "difficulty_floor_by_task_type:verify"},
		{"implement 底 hard", TaskTypeImplement, DifficultyHard, "difficulty_floor_by_task_type:implement"},
		{"explore 底 easy：显式 easy 保持", TaskTypeExplore, DifficultyEasy, ""},
		{"归一容忍大小写与空白", "  Migrate ", DifficultyHard, "difficulty_floor_by_task_type:migrate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := resolveWithTask(t, promotionConfig(nil), TaskHint{
				Difficulty: DifficultyEasy,
				TaskType:   tc.taskType,
			})
			assert.Equal(t, tc.wantDiff, decision.Difficulty)
			if tc.wantWarn == "" {
				assert.NotContains(t, decision.Warnings, "difficulty_promoted_over_explicit")
				return
			}
			assert.Equal(t, SourceExplicitPromoted, decision.DifficultySource)
			assert.Contains(t, decision.Warnings, "difficulty_promoted_over_explicit")
			assert.Contains(t, decision.Warnings, tc.wantWarn)
		})
	}
}

// floor 只抬不降：显式 hard + explore 不会被 floor 拉低（G3 单调性）。
func TestResolveExplicitDifficulty_TaskTypeFloorNeverDowngrades(t *testing.T) {
	decision := resolveWithTask(t, promotionConfig(nil), TaskHint{
		Difficulty: DifficultyHard,
		TaskType:   TaskTypeExplore,
	})
	assert.Equal(t, DifficultyHard, decision.Difficulty)
	assert.Equal(t, "explicit", decision.DifficultySource)
	assert.NotContains(t, decision.Warnings, "difficulty_promoted_over_explicit")
}

// 未知 task_type → task_type_unknown:<v> 且档位不因它变（A11 后半）；「忽略」指
// 不为它回退猜词推导类别——难度关键词安全网是正交输入（plan §8 Step 2 并列），
// 不受未知类别影响，故这里用无关键词 goal 隔离被测行为。
func TestResolveExplicitDifficulty_UnknownTaskTypeWarnsWithoutFloor(t *testing.T) {
	decision := resolveWithTask(t, promotionConfig(nil), TaskHint{
		Difficulty: DifficultyEasy,
		TaskType:   "bogus-class",
		Goal:       "list the handler files",
	})
	assert.Equal(t, DifficultyEasy, decision.Difficulty, "未知 task_type 不得参与 floor")
	assert.Equal(t, "explicit", decision.DifficultySource)
	assert.Contains(t, decision.Warnings, "task_type_unknown:bogus-class")
	assert.NotContains(t, decision.Warnings, "difficulty_floor_by_task_type:bogus-class")
}

// off 模式：task_type floor 不改变档位（三态开关正交，doc8 §8.1「off 与今天逐字一致」）。
func TestResolveExplicitDifficulty_OffModeIgnoresTaskTypeFloor(t *testing.T) {
	cfg := promotionConfig(func(c *agentconfig.AICLISubagentRoutingConfig) {
		c.PromoteExplicitDifficulty = PromoteExplicitOff
	})
	decision := resolveWithTask(t, cfg, TaskHint{
		Difficulty: DifficultyEasy,
		TaskType:   TaskTypeMigrate,
	})
	assert.Equal(t, DifficultyEasy, decision.Difficulty)
	assert.Equal(t, "explicit", decision.DifficultySource)
	assert.NotContains(t, decision.Warnings, "difficulty_promoted_over_explicit")
	assert.NotContains(t, decision.Warnings, "difficulty_floor_by_task_type:migrate")
}

// warn 模式：只写 floor 证据、不动档位（与关键词 warn 语义一致）。
func TestResolveExplicitDifficulty_WarnModeKeepsDeclaredWithFloorWarning(t *testing.T) {
	cfg := promotionConfig(func(c *agentconfig.AICLISubagentRoutingConfig) {
		c.PromoteExplicitDifficulty = PromoteExplicitWarn
	})
	decision := resolveWithTask(t, cfg, TaskHint{
		Difficulty: DifficultyEasy,
		TaskType:   TaskTypeMigrate,
	})
	assert.Equal(t, DifficultyEasy, decision.Difficulty)
	assert.Equal(t, "explicit", decision.DifficultySource)
	assert.Contains(t, decision.Warnings, "difficulty_promoted_over_explicit")
	assert.Contains(t, decision.Warnings, "difficulty_floor_by_task_type:migrate")
	assert.Contains(t, decision.Warnings, "difficulty_promotion_warn_only")
}

// role 别名推导（§8.1）：task_type 缺省时 verifier/writer/researcher 推导隐式类别
// 进 RouteDecision.TaskType（审计可见），但**不参与 floor**——难度与 v3 逐字一致。
func TestResolveEffectiveTaskTypeRoleAliasKeepsV3Difficulty(t *testing.T) {
	cases := []struct {
		role       string
		readOnly   bool
		wantType   string
		wantDiff   string
		wantSource string
	}{
		{"verifier", false, TaskTypeVerify, DifficultyNormal, SourceExplicitPromoted},
		{"writer", false, TaskTypeImplement, DifficultyNormal, SourceExplicitPromoted}, // v3 角色底 normal，不得被 implement(hard) 抬走
		{"researcher", false, TaskTypeExplore, DifficultyEasy, "explicit"},             // v3 无角色底：easy 保持
		{"writer", true, "", DifficultyEasy, "explicit"},                               // 只读 writer 不推导
		{"custom-auditor", false, "", DifficultyEasy, "explicit"},                      // 无别名不推导
	}
	for _, tc := range cases {
		name := tc.role + "/readonly=" + strconv.FormatBool(tc.readOnly)
		t.Run(name, func(t *testing.T) {
			decision := resolveWithTask(t, promotionConfig(nil), TaskHint{
				Difficulty: DifficultyEasy,
				Role:       tc.role,
				ReadOnly:   tc.readOnly,
			})
			assert.Equal(t, tc.wantType, decision.TaskType)
			assert.Equal(t, tc.wantDiff, decision.Difficulty, "隐式 task_type 不得改变 v3 档位")
			assert.Equal(t, tc.wantSource, decision.DifficultySource)
		})
	}
}

// 显式 task_type 优先于 role 推导；task_subject 原样（trim）透传进决策。
func TestResolveExplicitTaskTypeWinsOverRoleAndSubjectTrims(t *testing.T) {
	decision := resolveWithTask(t, promotionConfig(nil), TaskHint{
		Difficulty:  DifficultyNormal,
		Role:        "writer",
		TaskType:    TaskTypeExplore,
		TaskSubject: "  list files under backend/internal  ",
	})
	assert.Equal(t, TaskTypeExplore, decision.TaskType)
	assert.Equal(t, "list files under backend/internal", decision.TaskSubject)
	// 显式 normal ≥ explore floor(easy) ⇒ 不升档；writer 角色底 normal ⇒ 也不变。
	assert.Equal(t, DifficultyNormal, decision.Difficulty)
	assert.Equal(t, "explicit", decision.DifficultySource)
}

// profile 查表（K-3）：task_types 命中 → task_type_override；未迁移的 roles 配置
// 在 task_type 缺省时仍按 role_override 命中（迁移窗口行为不变）。
func TestRouteProfileTaskTypesOverrideAndRolesFallback(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled:           &enabled,
		DefaultDifficulty: DifficultyNormal,
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			DifficultyNormal: {Provider: "local", Model: "level-normal"},
		},
		TaskTypes: map[string]map[string]agentconfig.AICLISubagentRouteProfile{
			TaskTypeMigrate: {
				// floor(migrate)=hard：解析后的档位是 hard，profile 也按 hard 查。
				DifficultyHard: {Provider: "remote", Model: "migrate-hard"},
			},
		},
		Roles: map[string]map[string]agentconfig.AICLISubagentRouteProfile{
			"verifier": {
				DifficultyNormal: {Provider: "local", Model: "verify-normal"},
			},
		},
	}
	resolver := Resolver{Config: cfg}
	parent := ParentDefaults{Provider: "parent", Model: "parent-model"}

	// 显式 task_types 命中。
	decision, err := resolver.Resolve(parent, TaskHint{
		Difficulty: DifficultyEasy,
		TaskType:   TaskTypeMigrate,
	})
	require.NoError(t, err)
	assert.Equal(t, SourceTaskTypeOverride, decision.Source)
	assert.Equal(t, DifficultyHard, decision.Difficulty)
	assert.Equal(t, "migrate-hard", decision.Model)

	// 未迁移 roles 兜底：task_type 缺省 + role=verifier → 仍按旧 profile。
	decision, err = resolver.Resolve(parent, TaskHint{
		Difficulty: DifficultyNormal,
		Role:       "verifier",
	})
	require.NoError(t, err)
	assert.Equal(t, SourceRoleOverride, decision.Source)
	assert.Equal(t, "verify-normal", decision.Model)

	// 都未命中 → levels 兜底。
	decision, err = resolver.Resolve(parent, TaskHint{
		Difficulty: DifficultyNormal,
		TaskType:   TaskTypeModify,
	})
	require.NoError(t, err)
	assert.Equal(t, SourceDifficultyLevel, decision.Source)
	assert.Equal(t, "level-normal", decision.Model)
}

// modelrouting 侧静态校验：未知 task_types 配置键 → task_type_unknown warning，
// 不报错（G4/K-4：告警可回溯、不静默）。
func TestValidateConfigWithWarnings_UnknownTaskTypeKeyWarns(t *testing.T) {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled: &enabled,
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			DifficultyNormal: {},
		},
		TaskTypes: map[string]map[string]agentconfig.AICLISubagentRouteProfile{
			"debugging": {DifficultyNormal: {}},
		},
	}
	warnings, err := ValidateConfigWithWarnings(cfg)
	require.NoError(t, err)
	assert.Contains(t, warnings, "task_type_unknown:debugging")
}

// 枚举清单稳定性：TaskTypes() 必须是排序后的 12 类封闭集合（schema enum 与
// 注入片段都消费它；agentconfig.subagentTaskTypes 镜像由本断言守住键一致性）。
func TestTaskTypesClosedEnum(t *testing.T) {
	want := []string{
		"config", "explore", "generate", "implement", "integration",
		"migrate", "modify", "refactor", "security", "test",
		"understand", "verify",
	}
	got := TaskTypes()
	require.Equal(t, want, got)
	for _, taskType := range want {
		_, ok := TaskTypeFloor(taskType)
		assert.True(t, ok, "floor 表缺少 %q", taskType)
	}
	_, ok := TaskTypeFloor("bogus")
	assert.False(t, ok)
}
