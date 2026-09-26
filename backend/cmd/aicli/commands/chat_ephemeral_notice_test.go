package commands

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// 瞬时通知（恢复摘要 / 退出提示 / 系统状态）只属于当前运行的画面：
// 必须进 Scene，但不得写进会话事件日志——一旦落日志，每次 resume 都会
// 重放历次运行累积的通知（实测同一会话 18 份"已恢复历史会话"、16 份
// "正在退出"、12 份"[Manager] MCP 已启动"，且计数停留在旧代际）。
func TestChatRuntimeEventBridge_EphemeralSupplementNotJournaled(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.eventLogPathOverride = logPath

	bridge.submitEphemeralSupplement("正在退出...")

	raw, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read event log: %v", err)
	}
	if strings.Contains(string(raw), "正在退出") {
		t.Fatalf("ephemeral supplement was journaled: %q", string(raw))
	}
	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 1 || snapshot.Cells[0].Source != "正在退出..." {
		t.Fatalf("ephemeral supplement missing from scene: %+v", snapshot)
	}

	// 对照：同一入口的持久补充仍然落日志（不能把持久语义一起改掉）。
	bridge.submitSupplement("[goal] keep me")
	raw, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	if !strings.Contains(string(raw), `"supplement":"[goal] keep me"`) {
		t.Fatalf("durable supplement missing from journal: %q", string(raw))
	}
}

// 旧日志里已经写着历次运行的通知，重放必须丢弃它们，只恢复真正的补充内容；
// 否则恢复出来的历史会被旧通知（含旧代际计数）淹没。
func TestChatRuntimeEventBridge_ReplaySkipsLegacyTransientNotices(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	journal := strings.Join([]string{
		`{"supplement":"已恢复历史会话: 读取文档（0轮/713条消息）\n"}`,
		`{"supplement":"正在退出...\n"}`,
		`{"supplement":"已中断 - 再次按 Ctrl+C 可退出\n"}`,
		`{"supplement":"[Manager] MCP 已启动: chrome-devtools (工具: 30)\n"}`,
		`{"supplement":"[goal] auto continuation limit reached"}`,
		"",
	}, "\n")
	if err := os.WriteFile(logPath, []byte(journal), 0o644); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.eventLogPathOverride = logPath
	replayed, err := bridge.replayEventLog()
	if err != nil {
		t.Fatalf("replayEventLog: %v", err)
	}
	if replayed != 1 {
		t.Fatalf("replayed = %d, want 1 (only the durable supplement)", replayed)
	}
	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 1 {
		t.Fatalf("replayed cells = %+v, want exactly one", snapshot)
	}
	cell := snapshot.Cells[0]
	if cell.Kind != scene.KindSupplement || cell.Source != "[goal] auto continuation limit reached" {
		t.Fatalf("replayed cell = %+v, want the durable supplement", cell)
	}
}

// 协调器层：RenderEphemeralSupplement 与 RenderLocalSupplement 画面等价，
// 但前者不写会话事件日志。
func TestChatInteractionCoordinator_EphemeralSupplementNotJournaled(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	bridge.eventLogPathOverride = logPath
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	var output bytes.Buffer
	coordinator.SetWriter(&output)

	coordinator.RenderEphemeralSupplement("正在退出...")
	coordinator.waitUIActorIdle()
	raw, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read event log: %v", err)
	}
	if strings.Contains(string(raw), "正在退出") {
		t.Fatalf("coordinator ephemeral supplement was journaled: %q", string(raw))
	}
	if snapshot := bridge.sceneSnapshot(); snapshot == nil || len(snapshot.Cells) != 1 {
		t.Fatalf("ephemeral supplement cell missing: %+v", snapshot)
	}

	coordinator.RenderLocalSupplement("[retry] durable notice")
	coordinator.waitUIActorIdle()
	raw, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	if !strings.Contains(string(raw), `"supplement":"[retry] durable notice"`) {
		t.Fatalf("durable supplement was not journaled: %q", string(raw))
	}
}

// 终端呈现边界：任何来源（历史回放 / runtime 事件补投 / 遗留呈现器）都不得把
// 历次运行的瞬时通知重新打进终端或 Scene；当前运行的通知走各自的瞬时入口，
// 不经过 RenderSupplement，因此这里丢弃不会隐藏本次通知。
func TestAICLITranscriptRenderer_DropsTransientNoticesAnywhere(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprintf("replay=%t", replay), func(t *testing.T) {
			session := &ChatSession{}
			bridge := newChatRuntimeEventBridge(session)
			session.RuntimeEventBridge = bridge
			coordinator := newTestChatInteractionCoordinator(t, session)
			t.Cleanup(coordinator.Shutdown)
			session.Interaction = coordinator
			var output bytes.Buffer
			coordinator.SetWriter(&output)

			renderer := newAICLITranscriptRenderer(session)
			if replay {
				renderer = newAICLIReplayTranscriptRenderer(session)
			}
			for _, notice := range []string{
				"已恢复历史会话: 读取文档（0轮/713条消息）",
				"正在退出...",
				"已中断 - 再次按 Ctrl+C 可退出",
				"[Manager] MCP 已启动: chrome-devtools (工具: 30)",
			} {
				if !renderer.RenderSupplement(notice) {
					t.Fatalf("notice %q was not consumed", notice)
				}
			}
			coordinator.waitUIActorIdle()
			if out := output.String(); strings.Contains(out, "已恢复历史会话") || strings.Contains(out, "正在退出") {
				t.Fatalf("terminal leaked transient notices: %q", out)
			}
			if snapshot := bridge.sceneSnapshot(); snapshot != nil && len(snapshot.Cells) != 0 {
				t.Fatalf("transient notices reached the scene: %+v", snapshot.Cells)
			}
			// 对照：普通补充不受影响。
			output.Reset()
			if !renderer.RenderSupplement("[retry] real supplement") {
				t.Fatalf("durable supplement was dropped")
			}
			coordinator.waitUIActorIdle()
			snapshot := bridge.sceneSnapshot()
			inScene := snapshot != nil && len(snapshot.Cells) == 1 && snapshot.Cells[0].Source == "[retry] real supplement"
			if !inScene && !strings.Contains(output.String(), "[retry] real supplement") {
				t.Fatalf("durable supplement missing: scene=%+v output=%q", snapshot, output.String())
			}
		})
	}
}
