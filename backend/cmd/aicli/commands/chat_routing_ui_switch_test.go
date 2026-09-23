package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// 方案 §8.3 / §11 U-6（紧急开关）与 §10.2 I-10（既有视图仅追加迁移提示）单测。

// TestChatRoutingUIDisabledHidesUIEntries 钉住灰度回退开关语义：
// `AICLI_DISABLE_ROUTING_UI=1` 时隐藏 UI 入口（/routing 输出关闭说明、命令目录、
// 状态栏段），解析器与 API 不受影响（本测试不覆盖 API）。
func TestChatRoutingUIDisabledHidesUIEntries(t *testing.T) {
	t.Setenv("AICLI_DISABLE_ROUTING_UI", "1")
	session := routingCommandSessionWithOverride(t)

	text, handled := chatRoutingCommandText(session, "/routing show")
	if !handled || !strings.Contains(text, "AICLI_DISABLE_ROUTING_UI") {
		t.Fatalf("关闭态应给出开关说明：handled=%v\n%s", handled, text)
	}
	if segment := chatSurfaceRoutingStatusSegment(session); segment.full != "" || segment.compact != "" {
		t.Fatalf("关闭态不应显示状态栏 routing 段：%+v", segment)
	}
	for _, spec := range chatSlashCommandCatalog() {
		if spec.Name == "/routing" {
			t.Fatalf("关闭态命令目录不应包含 /routing：%+v", spec)
		}
	}
}

// TestChatRoutingUIEnabledByDefault 是上一条的对照：未设置开关时一切照旧。
func TestChatRoutingUIEnabledByDefault(t *testing.T) {
	t.Setenv("AICLI_DISABLE_ROUTING_UI", "")
	session := routingCommandSessionWithOverride(t)

	text, handled := chatRoutingCommandText(session, "/routing show")
	if !handled || strings.Contains(text, "AICLI_DISABLE_ROUTING_UI") {
		t.Fatalf("默认应正常输出路由摘要：\n%s", text)
	}
	if segment := chatSurfaceRoutingStatusSegment(session); segment.full == "" {
		t.Fatal("默认应显示状态栏 routing 段")
	}
	found := false
	for _, spec := range chatSlashCommandCatalog() {
		if spec.Name == "/routing" {
			found = true
		}
	}
	if !found {
		t.Fatal("默认命令目录应包含 /routing")
	}
}

// TestChatRoutingMigrationHintsOnLegacyViews 钉住 I-10：`/debug routing` 与
// `/agents routing` 的输出语义不变，仅追加会话级路由的迁移提示。
func TestChatRoutingMigrationHintsOnLegacyViews(t *testing.T) {
	session := routingCommandSessionWithOverride(t)

	debugResult, handled := tryExecuteStructuredDebugCommand(session, "/debug routing")
	if !handled {
		t.Fatal("/debug routing 应被结构化调试入口接管")
	}
	plain := ui.RenderDocumentPlain(debugResult.Document())
	if !strings.Contains(plain, "Subagent Routing:") || !strings.Contains(plain, "Migration:") {
		t.Fatalf("/debug routing 应保留原语义并追加迁移提示：\n%s", plain)
	}

	agentsResult := executeStructuredAgentRoutingCommand(session, "routing")
	agentsPlain := ui.RenderDocumentPlain(agentsResult.Document())
	if !strings.Contains(agentsPlain, "Subagent Routing:") || !strings.Contains(agentsPlain, "Migration:") {
		t.Fatalf("/agents routing 应保留原语义并追加迁移提示：\n%s", agentsPlain)
	}
}
