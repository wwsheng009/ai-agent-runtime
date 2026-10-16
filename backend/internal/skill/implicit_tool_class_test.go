package skill

import "testing"

// TestClassifyImplicitTool 钉住隐式调用判定的工具分类：
// 运行时期内置拼写由 toolregistry 的别名表推导（故 execute_shell_command、view
// 这类既有拼写不会再漏），第三方 skill / MCP 名字仍由本包的显式表兜底。
func TestClassifyImplicitTool(t *testing.T) {
	cases := []struct {
		name string
		want implicitToolClass
	}{
		// 运行期内置 shell 家族（规范名 + 别名）
		{"shell", implicitToolShell},
		{"bash", implicitToolShell},
		{"execute_shell_command", implicitToolShell},
		{"mcp__bash", implicitToolShell},
		{"  BASH  ", implicitToolShell},
		// 运行期内置读取家族（规范名 + 别名）
		{"view", implicitToolRead},
		{"read", implicitToolRead},
		// 第三方名字：显式登记 + 子串兜底
		{"run_shell_command", implicitToolShell},
		{"sh", implicitToolShell},
		{"exec", implicitToolShell},
		{"cat", implicitToolRead},
		{"head", implicitToolRead},
		{"read_file", implicitToolRead},
		{"mcp__read_file", implicitToolRead},
		// 未登记的第三方工具：既不猜成 shell，也不猜成 read
		{"grep", implicitToolUnknown},
		{"rg", implicitToolUnknown},
		{"glob", implicitToolUnknown},
		{"todos", implicitToolUnknown},
		{"", implicitToolUnknown},
		// 既有子串规则的产物（"exec" 命中 aicli_exec）：本轮去重不改判定口径，
		// 只把它从"顺带命中"变成被测试钉住的显式预期。
		{"aicli_exec", implicitToolShell},
	}

	for _, tc := range cases {
		if got := classifyImplicitTool(tc.name); got != tc.want {
			t.Errorf("classifyImplicitTool(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestImplicitToolNamesExcludeRegistrySpellings 钉住去重：运行期内置的现役拼写
// 不再出现在本包的显式表里——它们由注册表推导，手抄一份必然漂移。
func TestImplicitToolNamesExcludeRegistrySpellings(t *testing.T) {
	for _, name := range []string{"shell", "bash", "execute_shell_command", "view", "read"} {
		if _, ok := implicitShellToolNames[name]; ok {
			t.Errorf("implicitShellToolNames 不应再手写内置拼写 %q", name)
		}
		if _, ok := implicitReadToolNames[name]; ok {
			t.Errorf("implicitReadToolNames 不应再手写内置拼写 %q", name)
		}
	}
}
