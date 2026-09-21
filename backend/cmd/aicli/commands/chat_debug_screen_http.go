package commands

import (
	"encoding/json"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
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

// chatWebScreenMessage 是 web 客户端结构化会话消息：角色 + 正文。
// role 取值与 scene.CellKind / session 消息 role 一一映射：
// user / assistant / reasoning / tool / system / command / diagnostic / runtime。
type chatWebScreenMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatDebugScreenSnapshot 是 /debug/chat/screen 的 JSON 响应体。
type chatDebugScreenSnapshot struct {
	Available bool     `json:"available"`
	Reason    string   `json:"reason,omitempty"`
	Width     int      `json:"width,omitempty"`
	Height    int      `json:"height,omitempty"`
	Lines     []string `json:"lines,omitempty"`
	Text      string   `json:"text,omitempty"`
	// Messages 是结构化消息列表（仅 buildChatWebScreenSnapshot 填充；
	// 调试端点保持纯文本语义，不设置该字段）。前端据此做角色气泡渲染。
	Messages []chatWebScreenMessage `json:"messages,omitempty"`
	// MessageWindow 是结构化 messages 的分页元信息（windowChatWebMessages
	// 填充）。前端据此判断是否还有更早消息，以及下次上滚加载的游标。
	MessageWindow *chatWebMessageWindowInfo `json:"message_window,omitempty"`
}

// chatWebMessageWindow 描述结构化 messages 的分页窗口（绝对索引，左闭右开），
// 与 /web/api/screen 的 msg_limit / msg_before 查询参数一一对应：
//   - 两个字段都缺省（<=0）：不分页，返回完整 messages（历史行为，兼容既有调用方）；
//   - Limit>0：最多返回 Limit 条；Before<=0 时归一为消息总数（取最新一页）；
//   - Before>0：窗口右边界（排他），用于「加载更早消息」（配合前端已加载起始索引）。
type chatWebMessageWindow struct {
	Before int
	Limit  int
}

// active 报告窗口参数是否由调用方显式指定（用于决定是否重建 lines/text）。
func (w chatWebMessageWindow) active() bool { return w.Before > 0 || w.Limit > 0 }

// chatWebMessageWindowInfo 是 JSON 响应中的分页元信息：
// Start 为本次返回的第一条消息的绝对索引（>0 表示还有更早消息，上滚加载时
// 以 msg_before=Start 作为游标）；End 为排他右边界；Total 为消息总数。
type chatWebMessageWindowInfo struct {
	Total   int  `json:"total"`
	Start   int  `json:"start"`
	End     int  `json:"end"`
	Limit   int  `json:"limit,omitempty"`
	HasMore bool `json:"has_more"`
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
	messages := sessionTranscriptMessages(session)
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
	return buildChatWebScreenSnapshotFull()
}

// buildChatWebScreenSnapshotFor 按窗口参数返回快照：
//   - 窗口激活（调用方显式指定 msg_limit / msg_before）→ 窗口化提取，只物化
//     窗口内的结构化消息（O(窗口) 而非 O(总量)，见 buildChatWebScreenSnapshotWindowed）；
//   - 窗口未激活 → 完整 transcript（历史行为逐字节兼容）。
func buildChatWebScreenSnapshotFor(window chatWebMessageWindow) *chatDebugScreenSnapshot {
	if window.active() {
		return buildChatWebScreenSnapshotWindowed(window)
	}
	return buildChatWebScreenSnapshotFull()
}

// buildChatWebScreenSnapshotFull 返回完整 transcript 快照（历史行为）。
func buildChatWebScreenSnapshotFull() *chatDebugScreenSnapshot {
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
			snap.Messages = transcriptFallbackMessages(state.Transcript.Cells)
			return snap
		}
	}
	if session.RuntimeEventBridge != nil {
		if sceneSnap := session.RuntimeEventBridge.sceneSnapshot(); sceneSnap != nil {
			if lines := transcriptFallbackSnapshot(sceneSnap); len(lines) > 0 {
				snap.Available = true
				snap.Lines = lines
				snap.Text = strings.Join(lines, "\n")
				snap.Messages = transcriptFallbackSnapshotMessages(sceneSnap)
				return snap
			}
		}
	}
	if lines := sessionTranscriptFallbackLines(session); len(lines) > 0 {
		snap.Available = true
		snap.Lines = lines
		snap.Text = strings.Join(lines, "\n")
		snap.Messages = sessionTranscriptFallbackMessages(session)
		return snap
	}
	snap.Available = false
	snap.Reason = "no conversation content"
	return snap
}

// buildChatWebScreenSnapshotWindowed 只物化窗口内的结构化消息：先计数确定窗口
// 边界，再按区间提取，避免长会话每轮刷新都构造全量 messages / lines（O(总量)
// → O(窗口)）。三个内容源的优先级与 buildChatWebScreenSnapshotFull 一致，且
// 计数与提取共用同一套过滤/投影规则，结果与「先取全量再切片」逐字节一致
// （等价性由 chat_debug_screen_window_test.go 的用例守住）。
func buildChatWebScreenSnapshotWindowed(window chatWebMessageWindow) *chatDebugScreenSnapshot {
	snap := &chatDebugScreenSnapshot{}
	session := chatDebugDisplaySession()
	if session == nil {
		snap.Available = false
		snap.Reason = "no active chat session"
		return snap
	}
	if session.Interaction != nil && session.Interaction.uiActor != nil {
		cells := session.Interaction.uiActor.AppState().Transcript.Cells
		if total := countTranscriptCellMessages(cells); total > 0 {
			return snap.fillWindowedMessages(total, window, func(start, end int) []chatWebScreenMessage {
				return transcriptFallbackMessagesRange(cells, start, end)
			})
		}
	}
	if session.RuntimeEventBridge != nil {
		if sceneSnap := session.RuntimeEventBridge.sceneSnapshot(); sceneSnap != nil {
			if total := countTranscriptSnapshotMessages(sceneSnap); total > 0 {
				return snap.fillWindowedMessages(total, window, func(start, end int) []chatWebScreenMessage {
					return transcriptFallbackSnapshotMessagesRange(sceneSnap, start, end)
				})
			}
		}
	}
	messages := sessionTranscriptMessages(session)
	if total := countSessionTranscriptMessages(messages); total > 0 {
		return snap.fillWindowedMessages(total, window, func(start, end int) []chatWebScreenMessage {
			return sessionTranscriptMessagesRange(messages, start, end)
		})
	}
	snap.Available = false
	snap.Reason = "no conversation content"
	return snap
}

// fillWindowedMessages 写入窗口内消息、分页元信息，并按窗口消息重建 lines/text。
// 窗口归一与行前缀规则与 windowChatWebMessages 相同，保证窗口化提取与
// 「先取全量再切片」的结果一致。
func (s *chatDebugScreenSnapshot) fillWindowedMessages(total int, window chatWebMessageWindow, extract func(start, end int) []chatWebScreenMessage) *chatDebugScreenSnapshot {
	start, end := resolveChatWebMessageWindow(total, window)
	msgs := extract(start, end)
	s.Available = true
	s.Messages = msgs
	s.MessageWindow = &chatWebMessageWindowInfo{
		Total:   total,
		Start:   start,
		End:     end,
		Limit:   window.Limit,
		HasMore: start > 0,
	}
	lines := chatWebLinesForMessages(msgs)
	s.Lines = lines
	s.Text = strings.Join(lines, "\n")
	return s
}

// marshalChatWebScreenJSON 返回 web 屏幕快照的缩进 JSON 字节。
func marshalChatWebScreenJSON() ([]byte, error) {
	return json.MarshalIndent(buildChatWebScreenSnapshot(), "", "  ")
}

// marshalChatWebScreenJSONWindow 返回按窗口裁剪后的 web 屏幕快照 JSON 字节。
func marshalChatWebScreenJSONWindow(window chatWebMessageWindow) ([]byte, error) {
	snap := buildChatWebScreenSnapshotFor(window)
	if !window.active() {
		// 未指定窗口：仍写入分页元信息（Total/Start/End/HasMore），
		// 便于调用方统一处理响应形状；messages 保持全量。
		windowChatWebMessages(snap, window)
	}
	return json.MarshalIndent(snap, "", "  ")
}

// chatWebMessageWindowMaxLimit 限制 msg_limit 的上限：单次响应最多搬运的
// 消息条数，避免调用方用超大 limit 绕过窗口化意图（完整 transcript 仍可
// 不传参数获取）。
const chatWebMessageWindowMaxLimit = 500

// resolveChatWebMessageWindow 把请求窗口归一为绝对索引区间 [start, end)：
// 默认取最新一页（before<=0 视为总量），before 超出总量时钳到总量，limit 超出
// before 时钳到 before。全量提取（windowChatWebMessages）与窗口化提取
// （fillWindowedMessages）共用同一套规则，保证两条路径窗口边界一致。
func resolveChatWebMessageWindow(total int, window chatWebMessageWindow) (start, end int) {
	if total <= 0 {
		return 0, 0
	}
	before := window.Before
	if before <= 0 || before > total {
		before = total
	}
	limit := window.Limit
	if limit <= 0 || limit > before {
		limit = before
	}
	return before - limit, before
}

// windowChatWebMessages 按窗口裁剪结构化 messages，并写入分页元信息。
//
// 窗口激活时同步以窗口内消息重建 Lines/Text：否则长会话下会出现「裁掉了
// messages 却仍把全量 lines/text 塞进响应」的假优化。未显式分页时保持
// 既有 lines/text 派生路径不变（历史行为逐字节兼容）。
func windowChatWebMessages(snap *chatDebugScreenSnapshot, window chatWebMessageWindow) {
	if snap == nil || len(snap.Messages) == 0 {
		return
	}
	total := len(snap.Messages)
	start, end := resolveChatWebMessageWindow(total, window)
	snap.Messages = snap.Messages[start:end]
	snap.MessageWindow = &chatWebMessageWindowInfo{
		Total:   total,
		Start:   start,
		End:     end,
		Limit:   window.Limit,
		HasMore: start > 0,
	}
	if !window.active() {
		return
	}
	lines := chatWebLinesForMessages(snap.Messages)
	snap.Lines = lines
	snap.Text = strings.Join(lines, "\n")
}

// chatWebLinesForMessages 由结构化消息重建纯文本行，前缀规则与
// transcriptFallbackCells / sessionTranscriptFallbackLines 保持一致。
func chatWebLinesForMessages(msgs []chatWebScreenMessage) []string {
	if len(msgs) == 0 {
		return nil
	}
	lines := make([]string, 0, len(msgs))
	for i := range msgs {
		lines = append(lines, chatWebLineForMessage(msgs[i]))
	}
	return lines
}

// chatWebLineForMessage 按 role 生成与语义 transcript 一致的纯文本行。
func chatWebLineForMessage(msg chatWebScreenMessage) string {
	switch msg.Role {
	case "user":
		return "user> " + msg.Content
	case "system":
		return "[system] " + msg.Content
	case "tool":
		return "[tool] " + msg.Content
	case "reasoning":
		return "[reasoning] " + msg.Content
	case "command":
		return "cmd> " + msg.Content
	case "diagnostic":
		return "[diag] " + msg.Content
	default:
		// assistant / runtime：正文直出。
		return msg.Content
	}
}

// ---------------------------------------------------------------------------
// 结构化消息辅助函数（web 客户端 role-based 气泡渲染数据源）
// ---------------------------------------------------------------------------

// chatWebRoleForCellKind 映射 cell.Kind 到 web 消息 role 值。
func chatWebRoleForCellKind(kind scene.CellKind) string {
	switch kind {
	case scene.KindUser:
		return "user"
	case scene.KindAssistant:
		return "assistant"
	case scene.KindReasoning:
		return "reasoning"
	case scene.KindToolChain:
		return "tool"
	case scene.KindRuntimeEvent:
		return "runtime"
	case scene.KindSupplement:
		return "assistant"
	case scene.KindSystem:
		return "system"
	case scene.KindCommand:
		return "command"
	case scene.KindDiagnostic:
		return "diagnostic"
	default:
		return "assistant"
	}
}

// transcriptFallbackMessages 从语义 transcript cells 派生结构化消息列表。
func transcriptFallbackMessages(cells []scene.TranscriptCell) []chatWebScreenMessage {
	return transcriptFallbackMessagesRange(cells, 0, len(cells))
}

// countTranscriptCellMessages 统计 cells 中可展示的消息条数（Source 非空）。
// 只计数不分配：窗口化路径先据此确定窗口边界，再按区间提取窗口内消息，
// 避免长会话每次刷新都构造全量消息切片。
func countTranscriptCellMessages(cells []scene.TranscriptCell) int {
	count := 0
	for i := range cells {
		if cells[i].Source != "" {
			count++
		}
	}
	return count
}

// transcriptFallbackMessagesRange 只提取 cells 过滤后 [start, end) 区间的消息。
// 过滤规则与 countTranscriptCellMessages 完全一致（Source 为空不计入索引空间），
// 因此 [0, len(cells)) 等价于全量提取。
func transcriptFallbackMessagesRange(cells []scene.TranscriptCell, start, end int) []chatWebScreenMessage {
	if len(cells) == 0 || start < 0 || end <= start {
		return nil
	}
	msgs := make([]chatWebScreenMessage, 0, end-start)
	idx := 0
	for i := range cells {
		cell := &cells[i]
		if cell.Source == "" {
			continue
		}
		if idx >= end {
			break
		}
		if idx >= start {
			msgs = append(msgs, chatWebScreenMessage{
				Role:    chatWebRoleForCellKind(cell.Kind),
				Content: cell.Source,
			})
		}
		idx++
	}
	if len(msgs) == 0 {
		return nil
	}
	return msgs
}

// transcriptFallbackSnapshotMessages 适配 bridge Scene 快照（指针切片）
// 到结构化消息列表。
func transcriptFallbackSnapshotMessages(snap *scene.Snapshot) []chatWebScreenMessage {
	if snap == nil || len(snap.Cells) == 0 {
		return nil
	}
	return transcriptFallbackSnapshotMessagesRange(snap, 0, len(snap.Cells))
}

// countTranscriptSnapshotMessages 统计 Scene 快照中可展示的消息条数（跳过 nil
// 与 Source 为空的 cell），口径与 transcriptFallbackSnapshotMessagesRange 一致。
func countTranscriptSnapshotMessages(snap *scene.Snapshot) int {
	if snap == nil {
		return 0
	}
	count := 0
	for _, cell := range snap.Cells {
		if cell != nil && cell.Source != "" {
			count++
		}
	}
	return count
}

// transcriptFallbackSnapshotMessagesRange 只提取 Scene 快照过滤后 [start, end)
// 区间的消息；不再先复制整份指针切片到值切片。
func transcriptFallbackSnapshotMessagesRange(snap *scene.Snapshot, start, end int) []chatWebScreenMessage {
	if snap == nil || len(snap.Cells) == 0 || start < 0 || end <= start {
		return nil
	}
	msgs := make([]chatWebScreenMessage, 0, end-start)
	idx := 0
	for _, cell := range snap.Cells {
		if cell == nil || cell.Source == "" {
			continue
		}
		if idx >= end {
			break
		}
		if idx >= start {
			msgs = append(msgs, chatWebScreenMessage{
				Role:    chatWebRoleForCellKind(cell.Kind),
				Content: cell.Source,
			})
		}
		idx++
	}
	if len(msgs) == 0 {
		return nil
	}
	return msgs
}

// sessionTranscriptFallbackMessages 直接从会话 transcript（session.Messages /
// session.RuntimeSession.History）派生结构化消息列表，不依赖 surface / uiActor。
func sessionTranscriptFallbackMessages(session *ChatSession) []chatWebScreenMessage {
	messages := sessionTranscriptMessages(session)
	if len(messages) == 0 {
		return nil
	}
	return sessionTranscriptMessagesRange(messages, 0, len(messages))
}

// sessionTranscriptMessages 返回会话 transcript 消息列表：优先 session.Messages，
// 为空时回退 RuntimeSession.History。
func sessionTranscriptMessages(session *ChatSession) []runtimetypes.Message {
	if session == nil {
		return nil
	}
	if len(session.Messages) > 0 {
		return session.Messages
	}
	if session.RuntimeSession != nil {
		return session.RuntimeSession.History
	}
	return nil
}

// chatWebMessageForHistoryEntry 把单条会话消息投影为结构化消息；ok=false 表示
// 该条内容为空、不计入消息索引空间。计数（countSessionTranscriptMessages）与
// 区间提取（sessionTranscriptMessagesRange）共用它，保证两者口径完全一致。
func chatWebMessageForHistoryEntry(msg *runtimetypes.Message, toolCalls map[string]runtimetypes.ToolCall) (chatWebScreenMessage, bool) {
	text := strings.TrimSpace(msg.Content)
	role := "assistant"
	switch msg.Role {
	case "user":
		role = "user"
	case "system":
		role = "system"
	case "tool":
		role = "tool"
	}
	if role == "tool" {
		// 兜底路径（无 surface/uiActor/Scene，例如非 TTY resume 或启动早期）
		// 也必须使用与实时一致的 compact 工具投影：直接输出模型面向原文
		// 会把 artifact 指针等内部细节当作历史单元格展示。
		call := toolCalls[strings.TrimSpace(msg.ToolCallID)]
		name := firstNonEmptyChatValue(
			strings.TrimSpace(call.Name),
			chatHistoryToolNameFromMetadata(msg.Metadata),
			strings.TrimSpace(msg.ToolCallID),
			"tool",
		)
		if display := chatHistoryToolDisplay(*msg, name, call.Args); display != "" {
			text = display
		}
	}
	if text == "" {
		return chatWebScreenMessage{}, false
	}
	return chatWebScreenMessage{Role: role, Content: text}, true
}

// countSessionTranscriptMessages 统计可展示的消息条数。工具消息的 compact 投影
// 兜底（正文为空但投影非空）同样计入索引空间，口径与区间提取一致。
func countSessionTranscriptMessages(messages []runtimetypes.Message) int {
	if len(messages) == 0 {
		return 0
	}
	toolCalls := indexChatHistoryToolCalls(messages)
	count := 0
	for i := range messages {
		if _, ok := chatWebMessageForHistoryEntry(&messages[i], toolCalls); ok {
			count++
		}
	}
	return count
}

// sessionTranscriptMessagesRange 只提取会话 transcript 过滤后 [start, end) 区间
// 的结构化消息：不构造全量消息切片，窗口外消息也不做展示文本投影。
func sessionTranscriptMessagesRange(messages []runtimetypes.Message, start, end int) []chatWebScreenMessage {
	if len(messages) == 0 || start < 0 || end <= start {
		return nil
	}
	toolCalls := indexChatHistoryToolCalls(messages)
	msgs := make([]chatWebScreenMessage, 0, end-start)
	idx := 0
	for i := range messages {
		msg, ok := chatWebMessageForHistoryEntry(&messages[i], toolCalls)
		if !ok {
			continue
		}
		if idx >= end {
			break
		}
		if idx >= start {
			msgs = append(msgs, msg)
		}
		idx++
	}
	if len(msgs) == 0 {
		return nil
	}
	return msgs
}
