package commands

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// runPresenterSwitchSession 跑一遍固定的混合会话序列（user → command →
// turn-1 assistant → error → turn-2 assistant），返回 coordinator 完整输出。
// envOn 控制 AICLI_SCENE_PRESENTER 迁移开关：false 走旧路径（双跑 + 探针），
// true 时完整块可见行由 Scene 投影驱动（blockSourceFn 接管 writeRowsLocked）。
// 同时返回 bridge 的 Scene 快照，供投影断言使用。
func runPresenterSwitchSession(t *testing.T, envOn bool) (string, *chatRuntimeEventBridge) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)
	if envOn {
		t.Setenv("AICLI_SCENE_PRESENTER", "1")
	} else {
		t.Setenv("AICLI_SCENE_PRESENTER", "")
	}
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge

	evs := renderParityTwoTurnEvents()
	coord := newChatInteractionCoordinator(session)
	var out bytes.Buffer
	coord.SetWriter(&out)

	// user 块（live 提交点自动注入 Scene：submitUserInput）。
	coord.RenderSubmittedUserInput("hi")
	// 命令结果块（live 提交点注入 Scene：KindCommand 终态块）。
	coord.RenderCommandDocument(renderParityCommandDoc())
	// turn-1：你好。
	for _, ev := range evs[:5] {
		bridge.encodeRenderModelEvent(ev)
	}
	coord.RenderAssistant("你好")
	// 操作错误块（live 提交点注入 Scene：KindSystem 终态块）。
	coord.RenderError(errors.New("boom"))
	// turn-2：世界。
	for _, ev := range evs[5:] {
		bridge.encodeRenderModelEvent(ev)
	}
	coord.RenderAssistant("世界")
	return out.String(), bridge
}

// TestPresenterSwitch_SceneModeOutputMatchesLegacy 固化迁移开关的核心等价：
// AICLI_SCENE_PRESENTER 开启后（完整块可见行以 Scene 投影为权威），与关闭
// 时（旧路径渲染）的终端输出逐行完全一致。这是 §8 第 5 项"切换真实 Scene
// presenter"的可回退性证明：任一时刻可关回旧路径而输出不变。
func TestPresenterSwitch_SceneModeOutputMatchesLegacy(t *testing.T) {
	legacyOut, legacyBridge := runPresenterSwitchSession(t, false)
	sceneOut, sceneBridge := runPresenterSwitchSession(t, true)

	if legacyOut != sceneOut {
		legacyLines := strings.Split(strings.TrimRight(legacyOut, "\n"), "\n")
		sceneLines := strings.Split(strings.TrimRight(sceneOut, "\n"), "\n")
		var b strings.Builder
		b.WriteString("legacy vs scene output differ\n")
		max := len(legacyLines)
		if len(sceneLines) > max {
			max = len(sceneLines)
		}
		for i := 0; i < max; i++ {
			var l, s string
			if i < len(legacyLines) {
				l = legacyLines[i]
			} else {
				l = "<missing>"
			}
			if i < len(sceneLines) {
				s = sceneLines[i]
			} else {
				s = "<missing>"
			}
			mark := " "
			if l != s {
				mark = "!"
			}
			b.WriteString(mark + " legacy=" + l + " scene=" + s + "\n")
		}
		t.Fatalf("%s\nlegacy raw=%q\nscene raw=%q", b.String(), legacyOut, sceneOut)
	}

	// 场景模式会话中探针仍注入：Scene 驱动的语义正文行与 Scene 投影
	// 逐块 matched，证明切换未破坏双跑对照。
	blocks, matched, missed, lastErr := sceneBridge.textParityStats()
	if blocks == 0 || matched != blocks || missed != 0 {
		t.Fatalf("scene-mode parity: blocks=%d matched=%d missed=%d last=%q",
			blocks, matched, missed, lastErr)
	}
	// 旧路径会话探针同样全绿（对照基线未被本次改动破坏）。
	blocks, matched, missed, lastErr = legacyBridge.textParityStats()
	if blocks == 0 || matched != blocks || missed != 0 {
		t.Fatalf("legacy parity: blocks=%d matched=%d missed=%d last=%q",
			blocks, matched, missed, lastErr)
	}
	t.Logf("scene-mode output identical to legacy (%d bytes, %d blocks matched)",
		len(sceneOut), matched)
}

// TestPresenterSwitch_SceneModeOutputIsSceneProjection 强断言 Scene 模式的
// 可见输出就是 Scene 投影（LayoutTranscript 分组 + 样式 chrome）本身：即
// 完整块文本确实来自 Scene，而非旧 cell source。防止 blockSourceFn 未来
// 退化（如返回空后回退旧路径却不报 mismatch）而不被发现。
func TestPresenterSwitch_SceneModeOutputIsSceneProjection(t *testing.T) {
	sceneOut, bridge := runPresenterSwitchSession(t, true)

	snap := bridge.sceneSnapshot()
	if snap == nil || len(snap.Cells) == 0 {
		t.Fatal("scene snapshot empty in scene mode")
	}
	var want []string
	for _, g := range sceneBlockGroups(snap) {
		for i, line := range g.lines {
			if i == 0 && g.kind == scene.KindUser && line == "" {
				continue // user 前导 gap 由 prompt 重绘输出，不在块行内
			}
			switch g.kind {
			case scene.KindUser:
				want = append(want, "> "+line)
			case scene.KindSystem:
				if line == "" {
					// 与产品 sceneBlockSource 对称：跨块 gap 空行由
					// writeRowsLocked 的 gapBlank 输出，不加 chrome。
					want = append(want, line)
				} else {
					want = append(want, ui.GetTheme(ui.ThemeAuto).ErrorIcon+"  "+line)
				}
			default:
				want = append(want, line)
			}
		}
	}
	got := strings.TrimRight(sceneOut, "\n")
	if got != strings.Join(want, "\n") {
		t.Fatalf("scene-mode output != scene projection\n got=%q\nwant=%q", got, strings.Join(want, "\n"))
	}
	t.Logf("scene-mode output matches scene projection (%d lines)", len(want))
}

// TestPresenterSwitch_EnvParsing 固化迁移开关的取值解析（大小写不敏感，
// 未设置/空/未知值一律关闭——回退安全）。
func TestPresenterSwitch_EnvParsing(t *testing.T) {
	cases := map[string]bool{
		"1":       true,
		"true":    true,
		"TRUE":    true,
		"on":      true,
		"yes":     true,
		" On ":    true,
		"":        false,
		"0":       false,
		"false":   false,
		"off":     false,
		"garbage": false,
	}
	for v, want := range cases {
		t.Setenv("AICLI_SCENE_PRESENTER", v)
		if got := scenePresenterModeFromEnv(); got != want {
			t.Errorf("AICLI_SCENE_PRESENTER=%q: got %v want %v", v, got, want)
		}
	}
}
