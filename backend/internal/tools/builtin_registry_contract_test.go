package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
)

// 本文件是 P2（注册点收敛）的漂移闸门。
//
// 收敛后的职责划分：
//   - toolregistry.Entry.ToolkitBuiltin 声明"**有哪些**"内置 toolkit 工具（T6 真源）；
//   - builtinToolkitRegistrations 回答"**怎么构造**"（构造参数只有宿主知道）。
//
// 两个集合必须相等：任一侧单独增删都会让下面的断言失败，从而把"注册表登记了却
// 没人注册"（工具对模型不可见）与"注册了却不在注册表"（落回启发式判定，P1 病类）
// 挡在 CI 之外。
//
// code.*（ADR-0004 code gate）走同一模式：Entry.CodeTool 声明"有哪些"，
// codeToolRegistrations 回答"怎么造 + 属哪一类"（见本文件末尾的两个断言）。

// declaredToolkitBuiltins 返回注册表声明的内置 toolkit 工具规范名 -> Entry。
func declaredToolkitBuiltins(t *testing.T) map[string]toolregistry.Entry {
	t.Helper()
	declared := map[string]toolregistry.Entry{}
	for _, entry := range toolregistry.Entries() {
		if entry.ToolkitBuiltin {
			declared[entry.Name] = entry
		}
	}
	// 反向自检：声明集合若被清空，下面的双向断言会"真空通过"，必须显式挡住。
	require.NotEmpty(t, declared, "toolregistry 未声明任何 ToolkitBuiltin：契约测试会真空通过，先修声明面")
	return declared
}

func TestBuiltinRegistrationTableMatchesRegistryDeclaration(t *testing.T) {
	declared := declaredToolkitBuiltins(t)

	table := map[string]int{}
	for _, spec := range builtinToolkitRegistrations {
		require.NotEmpty(t, spec.Canonical, "构造行必须声明它归属的注册表规范名")
		require.NotNil(t, spec.New, "构造行 %q 没有构造函数", spec.Canonical)
		// 构造行必须用**规范名**对账；写成别名会让双向断言失去意义。
		entry, ok := toolregistry.Lookup(spec.Canonical)
		require.True(t, ok, "构造行 %q 在注册表里不是可识别的工具身份", spec.Canonical)
		require.Equal(t, spec.Canonical, entry.Name,
			"构造行的 Canonical 必须是规范名（别名 %q 应由 Entry.Aliases 声明）", spec.Canonical)
		table[spec.Canonical]++
	}
	require.NotEmpty(t, table, "构造表为空：内置工具会整体不注册")

	// 方向一：声明 ⊆ 构造表。缺行 = 该工具永远不会注册（模型看不到它）。
	for name := range declared {
		require.NotZero(t, table[name],
			"注册表把 %q 声明为 ToolkitBuiltin，但 builtinToolkitRegistrations 没有构造行"+
				"（该工具不会被注册，等于对模型不可见）", name)
	}
	// 方向二：构造表 ⊆ 声明。多行 = 注册了却不在注册表（落回启发式判定）。
	for name, count := range table {
		require.Contains(t, declared, name,
			"%q 被 toolkit 构造表注册，但注册表未声明它 ToolkitBuiltin（身份漂移）", name)
		require.Greater(t, count, 0)
	}
}

func TestDeclaredLiveSpellingsAreRoutableOrRegistered(t *testing.T) {
	declared := declaredToolkitBuiltins(t)

	// 无条件构造行可以安全地实例化并取真实注册名（Condition 非 nil 的行需要有
	// 可用 provider，测试里不构造）。
	instanceOwner := map[string]string{} // 实例名 -> 声明它的规范名
	for _, spec := range builtinToolkitRegistrations {
		if spec.Condition != nil {
			continue
		}
		name := spec.New(nil).Name()
		require.NotEmpty(t, name, "构造行 %q 造出的工具没有名字", spec.Canonical)
		require.NotContains(t, instanceOwner, name,
			"实例名 %q 被重复构造（重复注册会被 registry 静默吞掉）", name)
		instanceOwner[name] = spec.Canonical
	}

	for canonical, entry := range declared {
		// 实例必须落在自己的规范名下：别名实例（read/rg/bash/execute_shell_command）
		// 必须仍是该条目的合法拼写。
		for instance, owner := range instanceOwner {
			if owner != canonical {
				continue
			}
			require.True(t, toolregistry.IsSpellingOf(canonical, instance),
				"构造行把 %q 归到 %q，但注册表不认为它是该条目的拼写", instance, canonical)
			live, ok := toolregistry.LookupLive(instance)
			require.True(t, ok, "构造行造出的 %q 不是注册表中的现役拼写", instance)
			require.Equal(t, canonical, live.Name)
		}

		// 声明面里的每个"现役拼写"都必须可路由，否则模型用这个拼写调用会拿到
		// "tool not found"——这正是 P1 修掉的那类声明面/请求面不一致。
		// 可路由 = 有独立实例，或在派发期被归一（见 canonicalManagedToolName）。
		requireCovered := func(spelling, kind string) {
			_, registered := instanceOwner[spelling]
			routed := canonicalManagedToolName(spelling) != strings.TrimSpace(spelling)
			require.True(t, registered || routed,
				"%q 的%s %q 既没有注册实例、也不被派发期归一（模型调用它会得到 tool not found）",
				canonical, kind, spelling)
		}
		for _, alias := range entry.Aliases {
			requireCovered(alias, "现役别名")
		}

		// 无条件条目必须真有一个以规范名注册的实例（条件条目如 provider 未启用时
		// 缺席是合法的，故不在此处断言）。
		unconditional := false
		for _, spec := range builtinToolkitRegistrations {
			if spec.Canonical == canonical && spec.Condition == nil {
				unconditional = true
				break
			}
		}
		if unconditional {
			requireCovered(canonical, "规范名")
		}
	}
}

// TestAliasInstancesAreRegistered 显式钉住"别名必须有独立实例"这一历史约定：
// read / rg / bash / execute_shell_command 各自持有自己的 schema 与描述，模型面
// 虽然只折叠展示规范名，但调用别名时必须能命中。
//
// 反例是 image_generate：它**没有**独立实例，靠 canonicalManagedToolName 在派发期
// 归一；把它误加进构造表会造出重复面，把它从注册表的现役别名里删掉又会丢兼容性。
func TestAliasInstancesAreRegistered(t *testing.T) {
	instanceOwner := map[string]string{}
	for _, spec := range builtinToolkitRegistrations {
		if spec.Condition != nil {
			continue
		}
		instanceOwner[spec.New(nil).Name()] = spec.Canonical
	}

	aliasInstances := map[string]string{
		"bash":                  "shell",
		"execute_shell_command": "shell",
		"read":                  "view",
		"rg":                    "grep",
	}
	for instance, canonical := range aliasInstances {
		require.Equal(t, canonical, instanceOwner[instance],
			"别名 %q 必须作为独立实例注册到 %q", instance, canonical)
	}

	// image_generate 走派发期归一，不应有独立实例。
	require.NotContains(t, instanceOwner, "image_generate",
		"image_generate 应在派发期归一到 openai_image_generate，不应注册独立实例")
	require.Equal(t, "openai_image_generate", canonicalManagedToolName("image_generate"))
	require.NotContains(t, instanceOwner, "openai_image_generate",
		"图像工具按 provider 可用性条件注册，测试环境不应把它当无条件实例")
}

// declaredCodeTools 返回注册表声明的 code.* 工具（ADR-0004 code gate 条件注册）。
func declaredCodeTools(t *testing.T) map[string]toolregistry.Entry {
	t.Helper()
	declared := map[string]toolregistry.Entry{}
	for _, entry := range toolregistry.Entries() {
		if entry.CodeTool {
			declared[entry.Name] = entry
		}
	}
	// 反向自检：声明集合若被清空，下面的双向断言会"真空通过"，必须显式挡住。
	require.NotEmpty(t, declared, "toolregistry 未声明任何 CodeTool：契约测试会真空通过，先修声明面")
	return declared
}

// TestCodeToolRegistrationTableMatchesRegistryDeclaration 是 code.* 的双向漂移闸门。
//
// 与 builtinToolkitRegistrations 的闸门同构，但多钉三条 code gate 特有的不变量：
//  1. 表里的 Name 必须等于构造实例的 Name()。sync 用 spec.tool 注册（注册表 key =
//     tool.Name()），却用 spec.name 注销与记账（Unregister / registered / classes）；
//     两者不一致会造成两个静默故障：降档时注销不掉（工具仍对模型可见），以及
//     isCodeTool 认不出该工具（manager.callTool 的第二道防线失效）。
//  2. class 必须来自已知集合：未知 class 会让 codeToolTierIncludes 恒为 false，
//     工具在任何档位都不注册（静默不可见）。
//  3. 身份必须是规范名（code.* 无别名，写成别名会让双向断言失去意义）。
func TestCodeToolRegistrationTableMatchesRegistryDeclaration(t *testing.T) {
	declared := declaredCodeTools(t)
	require.NotEmpty(t, codeToolRegistrations, "code 构造表为空：code.* 工具会整体不注册")

	table := map[string]int{}
	for _, spec := range codeToolRegistrations {
		require.NotEmpty(t, spec.Name, "code 构造行必须声明规范名")
		require.NotNil(t, spec.New, "code 构造行 %q 没有构造函数", spec.Name)

		instance := spec.New()
		require.NotNil(t, instance, "code 构造行 %q 造出 nil 实例", spec.Name)
		require.Equal(t, spec.Name, instance.Name(),
			"code 构造行声明名 %q 与实例注册名 %q 不一致："+
				"sync 用实例名注册、用声明名注销与记账，降档会注销不掉且第二道防线失效",
			spec.Name, instance.Name())

		require.Contains(t, []string{codeToolClassDefinition, codeToolClassRelation}, spec.Class,
			"code 构造行 %q 的 class %q 不在已知集合内", spec.Name, spec.Class)

		entry, ok := toolregistry.Lookup(spec.Name)
		require.True(t, ok, "code 构造行 %q 在注册表里不是可识别的工具身份", spec.Name)
		require.Equal(t, spec.Name, entry.Name, "code 构造行必须用规范名对账")
		table[spec.Name]++
	}

	// 方向一：声明 ⊆ 构造表。缺行 = 该工具永远不注册（模型看不到它）。
	for name := range declared {
		require.NotZero(t, table[name],
			"注册表把 %q 声明为 CodeTool，但 codeToolRegistrations 没有构造行"+
				"（该工具永远不会注册，等于对模型不可见）", name)
	}
	// 方向二：构造表 ⊆ 声明。多行 = 注册了却不在注册表（落回启发式判定）。
	for name := range table {
		require.Contains(t, declared, name,
			"%q 被 code gate 注册，但注册表未声明它 CodeTool（身份漂移）", name)
	}
}

// TestCodeToolAndToolkitBuiltinDeclarationsAreDisjoint 钉住两个声明位互斥：
// 同时为真的条目会落进两条注册路径，造成重复注册与归属不清。
func TestCodeToolAndToolkitBuiltinDeclarationsAreDisjoint(t *testing.T) {
	codeTools := 0
	for _, entry := range toolregistry.Entries() {
		if entry.CodeTool {
			codeTools++
		}
		require.False(t, entry.ToolkitBuiltin && entry.CodeTool,
			"%q 同时被声明为 ToolkitBuiltin 与 CodeTool（两条注册路径重叠）", entry.Name)
	}
	require.NotZero(t, codeTools, "注册表没有任何 CodeTool 声明：本断言会真空通过")
}
