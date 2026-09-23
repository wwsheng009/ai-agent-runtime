package commands

import (
	"strings"
	"testing"
)

// 方案 §10.2 I-9：`/routing` Tab 逐段补全（作用域 → 键 → 值）+ 非法值最近似候选。

func routingCompletionController(t *testing.T, text string) *chatSlashCompletionController {
	t.Helper()
	controller := newChatSlashCompletionController(&ChatSession{})
	controller.UpdateAt(text, len([]rune(text)))
	return controller
}

// routingCompletionCandidates 断言补全弹窗处于参数模式并返回候选。
func routingCompletionCandidates(t *testing.T, text string) []chatSlashCompletionCandidate {
	t.Helper()
	controller := routingCompletionController(t, text)
	if !controller.state.Active || !controller.state.Context.InArguments {
		t.Fatalf("expected /routing argument popup for %q, got %#v", text, controller.state)
	}
	return controller.state.Candidates
}

func requireRoutingCandidates(t *testing.T, text string, expected ...string) {
	t.Helper()
	candidates := routingCompletionCandidates(t, text)
	for _, command := range expected {
		if !containsSlashCandidate(candidates, command) {
			t.Fatalf("expected /routing candidates for %q to include %q, got %#v", text, command, candidates)
		}
	}
}

func requireRoutingCandidatesAbsent(t *testing.T, text string, unexpected ...string) {
	t.Helper()
	candidates := routingCompletionCandidates(t, text)
	for _, command := range unexpected {
		if containsSlashCandidate(candidates, command) {
			t.Fatalf("expected /routing candidates for %q to exclude %q, got %#v", text, command, candidates)
		}
	}
}

func TestChatSlashArgumentCompletionRoutingTopLevel(t *testing.T) {
	t.Parallel()

	requireRoutingCandidates(t, "/routing ",
		"show", "doctor", "on", "off", "main", "sub", "reset", "save", "--to", "--yes", "--json")
}

func TestChatSlashArgumentCompletionRoutingKeyStage(t *testing.T) {
	t.Parallel()

	requireRoutingCandidates(t, "/routing main ",
		"enabled", "allow_expert", "levels", "expensive_levels", "default_difficulty",
		"cost_guard_mode", "max_consecutive_expensive_steps", "level",
		"profiles.easy.provider", "profiles.hard.reasoning_effort")

	// 子 Agent 键空间更窄（§3.5）：只有 enabled/default_difficulty/levels.*。
	requireRoutingCandidates(t, "/routing sub ",
		"enabled", "default_difficulty", "level", "levels.easy.provider", "levels.expert.model")
	requireRoutingCandidatesAbsent(t, "/routing sub ", "allow_expert", "profiles.easy.provider")

	// 前缀过滤仍生效（查询串来自当前 token）。
	requireRoutingCandidates(t, "/routing main cos", "cost_guard_mode")
}

func TestChatSlashArgumentCompletionRoutingValueStage(t *testing.T) {
	t.Parallel()

	requireRoutingCandidates(t, "/routing main enabled ", "on", "off")
	requireRoutingCandidates(t, "/routing main allow_expert ", "on", "off")
	requireRoutingCandidates(t, "/routing main cost_guard_mode ", "soft", "hard")
	requireRoutingCandidates(t, "/routing main default_difficulty ", "easy", "normal", "hard", "expert")
	requireRoutingCandidates(t, "/routing main levels ", "easy", "normal", "hard", "expert")
	requireRoutingCandidates(t, "/routing sub enabled ", "on", "off")
}

func TestChatSlashArgumentCompletionRoutingLevelSugarStage(t *testing.T) {
	t.Parallel()

	// 糖写法：level <level> <field> <value>（§5.4）。
	requireRoutingCandidates(t, "/routing main level ", "easy", "normal", "hard", "expert")
	requireRoutingCandidates(t, "/routing sub level ", "easy", "normal", "hard", "expert")
	requireRoutingCandidates(t, "/routing main level hard ",
		"provider", "model", "reasoning_effort", "thinking_effort", "prompt_cache")
	// `candidates` 是只读字段（§5.3），不参与补全。
	requireRoutingCandidatesAbsent(t, "/routing main level hard ", "candidates")
	requireRoutingCandidates(t, "/routing main level hard prompt_cache ", "on", "off")

	// 只读键路径不给取值候选（写回时命令层同样拒绝）。
	controller := routingCompletionController(t, "/routing main profiles.easy.candidates ")
	if len(controller.state.Candidates) != 0 {
		t.Fatalf("expected no value candidates for read-only field, got %#v", controller.state.Candidates)
	}
}

func TestChatSlashArgumentCompletionRoutingLayerStage(t *testing.T) {
	t.Parallel()

	requireRoutingCandidates(t, "/routing save ", "session", "workspace", "config")
	requireRoutingCandidates(t, "/routing show ", "main", "sub")
	requireRoutingCandidates(t, "/routing reset ", "main", "sub", "session", "workspace", "config", "--yes")
	// flag 可出现在任意位置：`--to` 之后直接给层候选。
	requireRoutingCandidates(t, "/routing reset --to ", "session", "workspace", "config")
	requireRoutingCandidates(t, "/routing main enabled --to ", "session", "workspace", "config")
	requireRoutingCandidates(t, "/routing main enabled --", "--to", "--yes")
	// `reset main` 之后是档位，`--to` 只能以 flag 形式出现。
	requireRoutingCandidates(t, "/routing reset main ", "easy", "normal", "hard", "expert")
}

func TestChatSlashArgumentCompletionRoutingNearestValue(t *testing.T) {
	t.Parallel()

	// 非法值退化为最近似候选（I-9），用户可直接采纳修正后的值。
	requireRoutingCandidates(t, "/routing main cost_guard_mode sof", "soft")
	requireRoutingCandidates(t, "/routing main enabld", "enabled")
	requireRoutingCandidates(t, "/routing sub enabeld", "enabled")

	// 差得太远时不给候选，避免误导。
	controller := routingCompletionController(t, "/routing main cost_guard_mode zzzz")
	if len(controller.state.Candidates) != 0 {
		t.Fatalf("expected no nearest candidates for distant value, got %#v", controller.state.Candidates)
	}
}

func TestChatSlashArgumentCompletionRoutingAcceptsValue(t *testing.T) {
	t.Parallel()

	controller := routingCompletionController(t, "/routing main cost_guard_mode sof")
	if !containsSlashCandidate(controller.state.Candidates, "soft") {
		t.Fatalf("expected prefix match to keep cost_guard_mode candidates, got %#v", controller.state.Candidates)
	}
	if containsSlashCandidate(controller.state.Candidates, "hard") {
		t.Fatalf("expected prefix query to filter out hard, got %#v", controller.state.Candidates)
	}

	controller = routingCompletionController(t, "/routing main enabled ")
	if !containsSlashCandidate(controller.state.Candidates, "on") {
		t.Fatalf("expected enabled value candidates, got %#v", controller.state.Candidates)
	}
	// 唯一候选（前缀已过滤到只剩一个）时接受补全应把值写入行内。
	text := "/routing main cost_guard_mode sof"
	controller = routingCompletionController(t, text)
	nextText, _, ok := controller.ApplyCompletion(text, len([]rune(text)))
	if !ok {
		t.Fatal("expected /routing value completion to be accepted")
	}
	if trimmed := strings.TrimSpace(nextText); trimmed != "/routing main cost_guard_mode soft" {
		t.Fatalf("expected accepted value to be written into the line, got %q", nextText)
	}
}
