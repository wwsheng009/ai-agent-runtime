package agentconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// v4 K-3：roles.<role> 读入时映射为 task_types.<task_type> + deprecation warning；
// 显式 task_types 不被覆盖；无别名的自定义 role 保留在 Roles 兜底。
func TestValidateSubagentRoutingConfig_MapsRolesToTaskTypes(t *testing.T) {
	enabled := true
	cfg := &AICLISubagentRoutingConfig{
		Enabled:           &enabled,
		DefaultDifficulty: "normal",
		Levels: map[string]AICLISubagentRouteProfile{
			"normal": {},
		},
		TaskTypes: map[string]map[string]AICLISubagentRouteProfile{
			"verify": { // 显式已存在：不得被 roles.verifier 覆盖
				"hard": {Provider: "explicit", Model: "explicit-verify"},
			},
		},
		Roles: map[string]map[string]AICLISubagentRouteProfile{
			"verifier": {"normal": {Provider: "local", Model: "role-verify"}},
			"writer":   {"normal": {Provider: "local", Model: "role-writer"}},
			"auditor":  {"normal": {Provider: "local", Model: "custom-auditor"}},
		},
	}

	warnings, err := ValidateSubagentRoutingConfig("aicli.subagents.routing", cfg)
	require.NoError(t, err)

	// 映射 + deprecation warning。显式 task_types.verify 已存在 ⇒ 不被
	// roles.verifier 覆盖（"显式 task_types 优先"），其 normal 难度也不并入。
	assert.Equal(t, map[string]AICLISubagentRouteProfile{
		"hard": {Provider: "explicit", Model: "explicit-verify"},
	}, cfg.TaskTypes["verify"])

	// writer → implement 新增映射。
	require.Contains(t, cfg.TaskTypes, "implement")
	assert.Equal(t, "role-writer", cfg.TaskTypes["implement"]["normal"].Model)

	// deprecation warning 覆盖两个可映射 role。
	joined := ""
	for _, warning := range warnings {
		joined += warning + "\n"
	}
	assert.Contains(t, joined, "roles.verifier deprecated: mapped to aicli.subagents.routing.task_types.verify")
	assert.Contains(t, joined, "roles.writer deprecated: mapped to aicli.subagents.routing.task_types.implement")

	// 无别名自定义 role 不搬进封闭枚举，保留在 Roles。
	assert.Contains(t, cfg.Roles, "auditor")
	assert.NotContains(t, cfg.TaskTypes, "auditor")
}

// v4 K-4：未知 task_types 配置键 → warning（含 task_type_unknown:<v>）且不报错。
func TestValidateSubagentRoutingConfig_UnknownTaskTypeKeyWarns(t *testing.T) {
	enabled := true
	cfg := &AICLISubagentRoutingConfig{
		Enabled:           &enabled,
		DefaultDifficulty: "normal",
		Levels:            map[string]AICLISubagentRouteProfile{"normal": {}},
		TaskTypes: map[string]map[string]AICLISubagentRouteProfile{
			"debugging": {"normal": {}},
		},
	}
	warnings, err := ValidateSubagentRoutingConfig("aicli.subagents.routing", cfg)
	require.NoError(t, err)
	found := false
	for _, warning := range warnings {
		if warning == "aicli.subagents.routing.task_types key \"debugging\" is unknown (task_type_unknown:debugging); entry ignored for routing" {
			found = true
		}
	}
	assert.True(t, found, "warnings=%v", warnings)
}
