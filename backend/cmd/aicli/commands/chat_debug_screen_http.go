package commands

import (
	"encoding/json"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
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
