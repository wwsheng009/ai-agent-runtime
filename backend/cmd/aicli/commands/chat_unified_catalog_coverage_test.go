package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// TestUnifiedCatalogCommandsNeverFallToUnknown 机械守卫（L5-3 Batch C 验收）：
// 目录全集（主名 + 别名）中的每个命令都必须被结构化分派认领。任何
// handled=false 都会在统一渲染会话里落入 typed "未知命令" 回落——这意味着
// 该命令对统一用户不可用，属于必须修复的认领缺口。
func TestUnifiedCatalogCommandsNeverFallToUnknown(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coord := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 20)
	surface.SetPhysicalWritesEnabled(false)
	coord.SetSurface(surface)
	var terminal bytes.Buffer
	if !coord.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	coord.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coord)

	names := make([]string, 0, 80)
	for _, spec := range chatSlashCommandCatalog() {
		names = append(names, spec.Name)
		names = append(names, spec.Aliases...)
	}
	if len(names) < 70 {
		t.Fatalf("catalog names=%d, want the full 72-name set", len(names))
	}
	for _, name := range names {
		if _, handled, err := tryExecuteStructuredChatCommand(session, name); !handled {
			t.Errorf("catalog command %s is not claimed by unified dispatch (would fall to 未知命令, err=%v)", name, err)
		}
	}
}

// TestUnifiedKnownCommandVariantsStayTyped 覆盖 Batch C 补齐的参数拒绝面：
// 已知命令的非法参数/缺参在统一会话中必须给出 typed 单元格，不得误报"未知命令"。
func TestUnifiedKnownCommandVariantsStayTyped(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coord := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 20)
	surface.SetPhysicalWritesEnabled(false)
	coord.SetSurface(surface)
	var terminal bytes.Buffer
	if !coord.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	coord.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coord)
	terminal.Reset()

	commands := []struct {
		input  string
		marker string
	}{
		{input: "/status extra", marker: "错误: /status 不接受参数"},
		{input: "/new extra", marker: "错误: /new 不接受参数"},
		{input: "/history extra", marker: "错误: /history 不接受参数"},
		{input: "/load", marker: "错误: 需要指定会话 ID"},
		{input: "/title", marker: "错误: 需要指定会话标题"},
		{input: "/rename", marker: "错误: 需要指定会话标题"},
		{input: "/goal --json", marker: "/goal --json 仅在非交互/脚本投影可用"},
	}
	raw := captureStdout(t, func() {
		for _, test := range commands {
			if dispatchChatCommand(session, test.input, false) {
				t.Fatalf("%s unexpectedly requested chat exit", test.input)
			}
		}
	})
	if raw != "" {
		t.Fatalf("unified known-command variants wrote raw stdout:\n%q", raw)
	}
	coord.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coord)

	var transcript strings.Builder
	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != len(commands) {
		count := 0
		if snapshot != nil {
			count = len(snapshot.Cells)
		}
		t.Fatalf("semantic command cells=%d want %d", count, len(commands))
	}
	for index, test := range commands {
		transcript.WriteString(snapshot.Cells[index].Source)
		transcript.WriteByte('\n')
		if !strings.Contains(snapshot.Cells[index].Source, test.marker) {
			t.Fatalf("cell[%d] for %s did not contain %q: %+v", index, test.input, test.marker, snapshot.Cells[index])
		}
	}
	if strings.Contains(transcript.String(), "未知命令") {
		t.Fatalf("known-command variants must not report 未知命令:\n%s", transcript.String())
	}
}

// TestUnifiedFallbackDistinguishesKnownAndUnknown 锁定 Batch C 的两档回落：
// 真正未知的命令报"未知命令"；目录内命令的未支持形式给出明确的不支持说明
// （而不是误导性的"未知命令"）。
func TestUnifiedFallbackDistinguishesKnownAndUnknown(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coord := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 20)
	surface.SetPhysicalWritesEnabled(false)
	coord.SetSurface(surface)
	var terminal bytes.Buffer
	if !coord.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	coord.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coord)
	terminal.Reset()

	if dispatchChatCommand(session, "/not-a-command", false) {
		t.Fatal("/not-a-command unexpectedly requested chat exit")
	}
	// /memory search 缺查询：目录内命令的未支持形式 → 两档回落中的"不受支持"档。
	if dispatchChatCommand(session, "/memory search", false) {
		t.Fatal("/memory search unexpectedly requested chat exit")
	}
	coord.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coord)

	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 2 {
		count := 0
		if snapshot != nil {
			count = len(snapshot.Cells)
		}
		t.Fatalf("semantic command cells=%d want 2", count)
	}
	if !strings.Contains(snapshot.Cells[0].Source, "错误: 未知命令: /not-a-command") {
		t.Fatalf("unknown command cell missing 未知命令 text: %+v", snapshot.Cells[0])
	}
	if !strings.Contains(snapshot.Cells[1].Source, "/memory 的当前参数或状态形式不受支持") {
		t.Fatalf("known-command fallback cell missing 不受支持 text: %+v", snapshot.Cells[1])
	}
}
