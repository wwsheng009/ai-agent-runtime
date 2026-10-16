package toolargs_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolargs"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
)

// TestArgTableCarriesOnlyCanonicalNames 钉住"一份词表"：表键只登记规范名。
//
// 别名（bash / execute_shell_command / rg / read）由 NormalizeToolkitToolName
// 归一到规范名后命中同一行，不再各占一行——手抄的别名行漏过 rg 正是它们该消失
// 的理由。若有人为了"让别名也能查表"再补一行别名，本测试立刻失败。
func TestArgTableCarriesOnlyCanonicalNames(t *testing.T) {
	names := toolargs.ToolkitToolNames()
	require.NotEmpty(t, names, "参数别名表不能为空")

	for _, name := range names {
		require.Equal(t, name, toolregistry.Canonical(name),
			"参数别名表登记了非规范名 %q：别名只应存在于 toolregistry 的别名表里", name)
	}
}

// TestArgAliasesResolveToCanonicalSpec 钉住别名与规范名共享**同一份**参数契约：
// executor 的参数晋升与 policy 的参数分类都按同一个键取表，别名一旦漏登记就会
// 掉进未登记工具回退（rg 曾经如此：路径参数不再按运行时路径审查，模型发
// query / target 之类的别名也不会被晋升成 pattern / path）。
func TestArgAliasesResolveToCanonicalSpec(t *testing.T) {
	for alias, canonical := range map[string]string{
		"bash":                  "shell",
		"execute_shell_command": "shell",
		"rg":                    "grep",
		"read":                  "view",
	} {
		want, ok := toolargs.ToolkitArgAliasesFor(canonical)
		require.True(t, ok, "规范名 %q 必须在参数别名表里", canonical)

		got, ok := toolargs.ToolkitArgAliasesFor(alias)
		require.True(t, ok,
			"别名 %q 必须解析到 %q 的参数契约，否则掉进未登记工具回退", alias, canonical)
		require.Equal(t, want, got,
			"别名 %q 与规范名 %q 的参数契约必须逐字一致", alias, canonical)
	}
}

// TestRegistryAliasesAllResolve 结构性闭合：注册表里任何登记了参数契约的工具，
// 其全部现役别名都必须解析到同一份契约。别名表后来新增的拼写自动进入本断言，
// 不再依赖人手同步两张表。
func TestRegistryAliasesAllResolve(t *testing.T) {
	checked := 0
	for _, entry := range toolregistry.Entries() {
		want, ok := toolargs.ToolkitArgAliasesFor(entry.Name)
		if !ok {
			continue
		}
		for _, alias := range entry.Aliases {
			got, ok := toolargs.ToolkitArgAliasesFor(alias)
			require.True(t, ok, "%s 的别名 %q 未解析到参数契约", entry.Name, alias)
			require.Equal(t, want, got, "%s 的别名 %q 参数契约与规范名不一致", entry.Name, alias)
			checked++
		}
	}
	require.NotZero(t, checked, "没有任何别名被检查：注册表与参数表可能已经脱节")
}
