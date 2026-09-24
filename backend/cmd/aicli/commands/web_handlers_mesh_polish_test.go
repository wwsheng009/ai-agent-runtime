package commands

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// P2 前端打磨项的 asset 契约门禁（S13 / Web 子方案 §5.2 回退路径、§7.3 标题后缀）。
//
// 与 web_handlers_mesh_realtime_test.go 同套路：前端无构建步骤，真实浏览器行为
// 只能在手工清单里验（docs/aicli/web-testing.md §2.7.3），这里锁定那些
// 「删掉后页面照常加载、只是悄悄退化」的字符串契约。

// fetchChatWebAsset 取回一个前端 asset 的正文（契约测试的公共前置）。
func fetchChatWebAsset(t *testing.T, name string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleChatWebPage(rec, httptest.NewRequest(http.MethodGet, ChatWebPath+name, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status = %d, want 200", name, rec.Code)
	}
	return rec.Body.String()
}

// TestChatWebSessionsAssetHasSpawnRefusalText 断言 spawn 失败文案的 code 映射
// 与 §5.2 回退路径（refused → CLI `aicli-mesh open --print-url`；失败 → `aicli-mesh show`）
// 都在 sessions.js 里。
func TestChatWebSessionsAssetHasSpawnRefusalText(t *testing.T) {
	body := fetchChatWebAsset(t, "js/sessions.js")

	want := []string{
		// code → 文案映射表（P2 ③：策略拒绝要说清「谁拒的、怎么绕过」）。
		"SPAWN_CODE_TEXT",
		"mesh_spawn_not_allowed",
		"mesh_disabled",
		"mesh_nonloopback_denied",
		"mesh_workspace_missing",
		// 收敛开关下的跨工作区写拒绝（架构 §9.3；前端目前只在 spawn/call 信封里见到）。
		"mesh_cross_workspace_denied",
		"mesh_spawn_timeout",
		"mesh_spawn_failed",
		// §5.2 回退路径：refused 不在前端重试，改用 CLI 面。
		"aicli-mesh open ",
		"--print-url",
		// 失败态的诊断命令（节点档案 / 心跳）。
		"aicli-mesh show ",
		// 调用点必须把会话 id 传进去，回退命令才是可复制的（不是 <session> 占位）。
		"spawnFailureText(json, id)",
	}
	for _, token := range want {
		if !strings.Contains(body, token) {
			t.Errorf("js/sessions.js 缺少 %q（P2 ③ spawn refused 文案契约）", token)
		}
	}
}

// TestChatWebTitleHasMeshNodeSuffix 断言窗口标题的节点后缀（P2 ⑤ / §7.3）：
// chat.js 在标题末尾拼 meshNodeSuffix()，sessions.js 提供实现并在 self 段
// 变化时重算（否则后缀要等下一次状态翻转才出现）。
func TestChatWebTitleHasMeshNodeSuffix(t *testing.T) {
	chat := fetchChatWebAsset(t, "js/chat.js")
	for _, token := range []string{
		"meshNodeSuffix",
		`document.title = prefix + "aicli micro web client" + meshNodeSuffix()`,
	} {
		if !strings.Contains(chat, token) {
			t.Errorf("js/chat.js 缺少 %q（P2 ⑤ 标题节点后缀契约）", token)
		}
	}

	sessions := fetchChatWebAsset(t, "js/sessions.js")
	for _, token := range []string{
		"export function meshNodeSuffix",
		// 降级：网格不可用（self 为空）时后缀为空串，标题保持原样（§4.7）。
		`if (!meshSelf) { return ""; }`,
		"meshSelf.workspace_name",
		"meshSelf.node_id",
		// self 段到达 / 消失都要重算标题。
		"updateTitle(); // 标题节点后缀（P2 ⑤）：self 到达 / 消失都要重算一次",
	} {
		if !strings.Contains(sessions, token) {
			t.Errorf("js/sessions.js 缺少 %q（P2 ⑤ 标题节点后缀契约）", token)
		}
	}
}
