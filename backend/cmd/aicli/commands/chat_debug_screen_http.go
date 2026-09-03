package commands

import (
	"encoding/json"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// ============================================================================
// 当前屏幕内容 HTTP 快照（/debug/chat/screen）
//
// 复用 /debug/chat/status 的 chatDebugDisplaySessionProvider 获取当前活动
// 会话，再从会话的 FixedBottomSurface 合成帧（ComposedFrameForTest）读取
// 当前屏幕显示内容。该端点与 /debug/chat/status 互补：
//   - /debug/chat/status 返回渲染器内部状态（encoder/scene/output/app_state）
//   - /debug/chat/screen 返回"用户当前实际看到的屏幕内容"（合成帧文本）
//
// 无会话 / 无 surface / 空帧时返回 available=false 的轻量响应，便于轮询。
// ============================================================================

// chatDebugScreenSnapshot 是 /debug/chat/screen 的 JSON 响应体。
type chatDebugScreenSnapshot struct {
	Available bool     `json:"available"`
	Reason    string   `json:"reason,omitempty"`
	Width     int      `json:"width,omitempty"`
	Height    int      `json:"height,omitempty"`
	Lines     []string `json:"lines,omitempty"`
	Text      string   `json:"text,omitempty"`
}

// BuildChatDebugScreenSnapshot 返回当前屏幕合成帧的结构化快照。
// 每行以 TrimRight 去除行尾空白（合成帧固定宽度，行尾多为空白）。
func BuildChatDebugScreenSnapshot() *chatDebugScreenSnapshot {
	snap := &chatDebugScreenSnapshot{}
	session := chatDebugDisplaySession()
	if session == nil {
		snap.Available = false
		snap.Reason = "no active chat session"
		return snap
	}
	// 优先派生通道（Presenter Migration）：当前生产渲染器以 AppState 快照为
	// 唯一输出源，legacy surface 合成帧（ComposedFrameForTest）在迁移模式下
	// 只保留状态行，会话正文为空。这里从 UIController 的 AppState 派生完整
	// 文本帧，行序/宽度与 legacy 帧一致（FrameParityWithAppLayout 的 shadow
	// 契约），回退到 legacy 帧仅用于无 uiActor 的兼容场景。
	//
	// 屏幕镜像的权威数据平面是语义 transcript（与 geometry 无关）：
	//   - 第一优先：uiActor 的 AppState 布局帧（有 surface，geometry>0）
	//   - 第二优先：uiActor 的 AppState 语义 cells（unifiedRenderer 已启用）
	//   - 第三优先：runtime event bridge 的 Scene 快照（任何启动形态都会构建，
	//     包括 Win7 无 ANSI 控制台 / headless / 后台服务；uiActor 同步被
	//     UnifiedRendererEnabled 门控，bridge Scene 不依赖该门控）
	//   - 第四优先：会话自身 transcript（session.Messages / RuntimeSession.History，
	//     点击会话后历史消息未注入 bridge 时仍可直接派生，Win7 降级形态下
	//     bridge Scene 只覆盖 live events，历史不重放）
	// 四者都空时才落到 "no active terminal surface" 死信号。
	if session.Interaction != nil && session.Interaction.uiActor != nil {
		state := session.Interaction.uiActor.AppState()
		layout := ui.ComposeAppTextLayout(state)
		if layout.Height > 0 && len(layout.Rows) > 0 {
			lines := make([]string, 0, len(layout.Rows))
			for _, row := range layout.Rows {
				lines = append(lines, strings.TrimRight(row.Text, " "))
			}
			snap.Available = true
			snap.Height = layout.Height
			if layout.Width > 0 {
				snap.Width = layout.Width
			}
			snap.Lines = lines
			snap.Text = strings.Join(lines, "\n")
			return snap
		}
		// 无 geometry（有 surface 或 unifiedRenderer 但未挂载终端尺寸）时回退
		// 到语义 cells：TranscriptState.Cells 与几何无关（app_state.go §5）。
		if lines := transcriptFallbackCells(state.Transcript.Cells); len(lines) > 0 {
			snap.Available = true
			snap.Lines = lines
			snap.Text = strings.Join(lines, "\n")
			return snap
		}
	}
	// bridge Scene 快照：uiActor 同步被 UnifiedRendererEnabled 门控，无 surface
	// 会话（Win7 降级 / headless）不会收到 transcript snapshot，但 bridge 在
	// 事件流到达时照常构建 Scene（chat_runtime_events.go applyChangeSet），
	// 是这类形态下唯一可靠的内容源。
	if session.RuntimeEventBridge != nil {
		if sceneSnap := session.RuntimeEventBridge.sceneSnapshot(); sceneSnap != nil {
			if lines := transcriptFallbackSnapshot(sceneSnap); len(lines) > 0 {
				snap.Available = true
				snap.Lines = lines
				snap.Text = strings.Join(lines, "\n")
				return snap
			}
		}
	}
	// 会话 transcript 兜底：bridge Scene 由 live events 构建，resume 加载的
	// 历史消息不会重放进 bridge。Win7 降级 / headless 无 surface 时，这是
	// 点击会话后仍能展示历史消息的唯一可靠来源；回退到 Surface 之前处理，
	// 避免 Surface == nil 时整个会话内容被丢弃。
	if lines := sessionTranscriptFallbackLines(session); len(lines) > 0 {
		snap.Available = true
		snap.Lines = lines
		snap.Text = strings.Join(lines, "\n")
		return snap
	}
	if session.Surface == nil {
		snap.Available = false
		snap.Reason = "no active terminal surface"
		return snap
	}
	frame := session.Surface.ComposedFrameForTest()
	if len(frame) == 0 {
		snap.Available = false
		snap.Reason = "empty composed frame"
		return snap
	}
	snap.Available = true
	snap.Height = len(frame)
	if len(frame[0]) > 0 {
		snap.Width = len(frame[0])
	}
	lines := make([]string, 0, len(frame))
	for _, row := range frame {
		var sb strings.Builder
		for _, cell := range row {
			if !cell.Cont {
				sb.WriteString(cell.Text)
			}
		}
		lines = append(lines, strings.TrimRight(sb.String(), " "))
	}
	snap.Lines = lines
	snap.Text = strings.Join(lines, "\n")
	return snap
}

// sessionTranscriptFallbackLines 直接从会话 transcript（session.Messages /
// session.RuntimeSession.History）派生纯文本行，不依赖 surface / uiActor /
// bridge Scene。适用于无 surface 形态（Win7 降级 / headless）点击会话后
// 历史消息未注入 bridge 的场景；与 transcriptFallbackCells 的输出格式保持
// 一致（user>/[system]/[tool] 前缀，正文直出）。
func sessionTranscriptFallbackLines(session *ChatSession) []string {
	if session == nil {
		return nil
	}
	messages := session.Messages
	if len(messages) == 0 && session.RuntimeSession != nil {
		messages = session.RuntimeSession.History
	}
	if len(messages) == 0 {
		return nil
	}
	lines := make([]string, 0, len(messages)*2)
	for i := range messages {
		msg := &messages[i]
		text := strings.TrimSpace(msg.Content)
		if text == "" {
			continue
		}
		switch msg.Role {
		case "user":
			lines = append(lines, "user> "+text)
		case "system":
			lines = append(lines, "[system] "+text)
		case "tool":
			lines = append(lines, "[tool] "+text)
		default:
			// assistant / developer / 其他：正文直出。
			lines = append(lines, text)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return lines
}

// transcriptFallbackCells 从语义 transcript cells 派生纯文本行。
//
// 与 ComposeAppTextLayout 的终端投影不同，这里不依赖 geometry：每个语义
// cell 按 Kind 输出单行（多行 Source 保留原样），顺序即对话时序。仅用于
// 无 surface 快照回退；有 surface 时仍走布局投影，保证行序/宽度一致。
func transcriptFallbackCells(cells []scene.TranscriptCell) []string {
	if len(cells) == 0 {
		return nil
	}
	lines := make([]string, 0, len(cells)*2)
	for i := range cells {
		cell := &cells[i]
		if cell.Source == "" {
			continue
		}
		switch cell.Kind {
		case scene.KindReasoning:
			lines = append(lines, "[reasoning] "+cell.Source)
		case scene.KindToolChain:
			lines = append(lines, "[tool] "+cell.Source)
		case scene.KindSystem:
			lines = append(lines, "[system] "+cell.Source)
		case scene.KindUser:
			lines = append(lines, "user> "+cell.Source)
		case scene.KindCommand:
			lines = append(lines, "cmd> "+cell.Source)
		case scene.KindDiagnostic:
			lines = append(lines, "[diag] "+cell.Source)
		default:
			// KindAssistant / KindSupplement / KindRuntimeEvent：正文直出。
			lines = append(lines, cell.Source)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return lines
}

// transcriptFallbackSnapshot 适配 bridge Scene 快照（Cells 为指针切片）到
// transcriptFallbackCells 的值切片输入。
func transcriptFallbackSnapshot(snap *scene.Snapshot) []string {
	if snap == nil || len(snap.Cells) == 0 {
		return nil
	}
	cells := make([]scene.TranscriptCell, 0, len(snap.Cells))
	for _, cell := range snap.Cells {
		if cell == nil {
			continue
		}
		cells = append(cells, *cell)
	}
	return transcriptFallbackCells(cells)
}

// BuildChatDebugScreenText 返回当前屏幕合成帧的纯文本摘要（?format=text）。
// 无会话 / 无 surface / 空帧时返回轻量提示。
func BuildChatDebugScreenText() string {
	snap := BuildChatDebugScreenSnapshot()
	if !snap.Available {
		return "Debug Screen: " + snap.Reason + "\n"
	}
	return snap.Text + "\n"
}

// MarshalChatDebugScreenJSON 返回缩进 JSON 字节，供 HTTP 端点直接写入。
func MarshalChatDebugScreenJSON() ([]byte, error) {
	return json.MarshalIndent(BuildChatDebugScreenSnapshot(), "", "  ")
}

// buildChatWebScreenSnapshot 返回 web 客户端所需的完整会话内容快照。
//
// 与 BuildChatDebugScreenSnapshot 的区别：web 展示的是聊天历史全文，而
// 调试屏幕镜像要反映"终端用户当前实际看到的视口帧"。视口帧受终端高度
// 裁剪（LayoutAppScreen 只保留最后 OutputBottomRow 行），resume 历史会话
// 后 web 端会因此只看到最后一个 turn。这里按完整语义 transcript 派生：
//   - 第一优先：uiActor AppState 的语义 cells（resume 后完整历史注入点）
//   - 第二优先：runtime event bridge 的 Scene 快照
//   - 第三优先：会话自身 transcript（session.Messages / RuntimeSession.History）
//
// 全部为空时返回 available=false（前端保留现有内容，不覆盖为空）。
func buildChatWebScreenSnapshot() *chatDebugScreenSnapshot {
	snap := &chatDebugScreenSnapshot{}
	session := chatDebugDisplaySession()
	if session == nil {
		snap.Available = false
		snap.Reason = "no active chat session"
		return snap
	}
	if session.Interaction != nil && session.Interaction.uiActor != nil {
		state := session.Interaction.uiActor.AppState()
		if lines := transcriptFallbackCells(state.Transcript.Cells); len(lines) > 0 {
			snap.Available = true
			snap.Lines = lines
			snap.Text = strings.Join(lines, "\n")
			return snap
		}
	}
	if session.RuntimeEventBridge != nil {
		if sceneSnap := session.RuntimeEventBridge.sceneSnapshot(); sceneSnap != nil {
			if lines := transcriptFallbackSnapshot(sceneSnap); len(lines) > 0 {
				snap.Available = true
				snap.Lines = lines
				snap.Text = strings.Join(lines, "\n")
				return snap
			}
		}
	}
	if lines := sessionTranscriptFallbackLines(session); len(lines) > 0 {
		snap.Available = true
		snap.Lines = lines
		snap.Text = strings.Join(lines, "\n")
		return snap
	}
	snap.Available = false
	snap.Reason = "no conversation content"
	return snap
}

// marshalChatWebScreenJSON 返回 web 屏幕快照的缩进 JSON 字节。
func marshalChatWebScreenJSON() ([]byte, error) {
	return json.MarshalIndent(buildChatWebScreenSnapshot(), "", "  ")
}
