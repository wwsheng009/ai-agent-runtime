package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// bare /mcp 与显式别名要求交互菜单；显式子命令必须继续走纯文本面板。
func TestChatMCPPickerRequested(t *testing.T) {
	for _, command := range []string{"/mcp", "/mcp select", "/mcp pick", "/mcp menu", "/mcp choose"} {
		if !chatMCPPickerRequested(command) {
			t.Fatalf("%q 应请求交互菜单", command)
		}
	}
	for _, command := range []string{
		"/mcp list", "/mcp ls", "/mcp status context7", "/mcp help",
		"/mcp enable x", "/mcp disable x", "/mcp remove x", "/mcp reload",
		"/mcp add foo https://example.com/mcp",
	} {
		if chatMCPPickerRequested(command) {
			t.Fatalf("%q 不得请求交互菜单", command)
		}
	}
}

// 无全屏表面（测试环境即无 TTY）时必须降级为列表文本，且不产生 picker effect。
func TestExecuteStructuredMCPCommandDegradesWithoutSurface(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}

	for _, command := range []string{"/mcp", "/mcp select"} {
		result := executeStructuredMCPCommand(session, command)
		if result.OpenMCPPicker != nil {
			t.Fatalf("%s 在无表面时不得请求选择器: %#v", command, result.OpenMCPPicker)
		}
		text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
		if text == "" {
			t.Fatalf("%s 降级后应给出列表文本", command)
		}
		// 降级结果绝不能是"未知子命令 select"这类解析错误。
		if strings.Contains(text, "未知子命令") {
			t.Fatalf("%s 降级结果不应报未知子命令:\n%s", command, text)
		}
	}
}

// 动作集合必须覆盖状态/启停/移除/热重载，且全部落在既有 /mcp 子命令上。
func TestChatMCPPickerActionsMapToExistingSubcommands(t *testing.T) {
	enabled := chatMCPPickerActions("context7", true)
	labels := make([]string, 0, len(enabled))
	byLabel := map[string]mcpPickerAction{}
	for _, action := range enabled {
		labels = append(labels, action.Label)
		byLabel[action.Label] = action
	}
	want := []string{chatMCPPickerActionStatus, "停用", chatMCPPickerActionReload, chatMCPPickerActionRemove, chatMCPPickerCancelLabel}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Fatalf("动作顺序 = %v, want %v", labels, want)
	}
	if got := byLabel[chatMCPPickerActionStatus].Command; got != "/mcp status context7" {
		t.Fatalf("状态动作 = %q", got)
	}
	if got := byLabel["停用"].Command; got != "/mcp disable context7" {
		t.Fatalf("停用动作 = %q", got)
	}
	if !byLabel[chatMCPPickerActionRemove].Confirm {
		t.Fatal("移除必须要求二次确认")
	}
	if got := byLabel[chatMCPPickerCancelLabel].Command; got != "" {
		t.Fatalf("返回上一层不应携带命令: %q", got)
	}

	// 停用的 server：动作变为"启用"。
	disabled := chatMCPPickerActions("local-fs", false)
	found := ""
	for _, action := range disabled {
		if action.Label == "启用" {
			found = action.Command
		}
		if action.Label == "停用" {
			t.Fatalf("已停用 server 不应再给停用动作: %#v", disabled)
		}
	}
	if found != "/mcp enable local-fs" {
		t.Fatalf("启用动作 = %q", found)
	}
}

// server 列表行必须复用 /mcp list 的同一份投影（标题行 + 详情行）。
func TestBuildChatMCPPickerServerItemsReusesListProjection(t *testing.T) {
	items := []mcpadmin.Item{
		{Config: mcpconfig.MCPConfig{
			Name:    "context7",
			Type:    "streamable",
			URL:     "https://mcp.context7.com/mcp",
			Command: "",
		}},
	}
	rows := buildChatMCPPickerServerItems(items)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	if !strings.Contains(rows[0].Title, "context7") || !strings.Contains(rows[0].Title, "已停用") {
		t.Fatalf("标题应复用 list 投影（含状态标记）: %q", rows[0].Title)
	}
	if !strings.Contains(rows[0].Detail, "mcp.context7.com") {
		t.Fatalf("详情应带 endpoint: %q", rows[0].Detail)
	}
	if rows[0].SearchText != "context7" {
		t.Fatalf("搜索文本应为 server 名: %q", rows[0].SearchText)
	}
}

func TestBuildChatMCPPickerActionItems(t *testing.T) {
	rows := buildChatMCPPickerActionItems(chatMCPPickerActions("x", true))
	if len(rows) != 5 {
		t.Fatalf("动作行数 = %d", len(rows))
	}
	for _, row := range rows {
		if strings.TrimSpace(row.Title) == "" || row.SearchText == "" {
			t.Fatalf("动作行必须可搜索且有标题: %#v", row)
		}
	}
}
