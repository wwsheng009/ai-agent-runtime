package style

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// StatusSegmentKind classifies status line segments for priority folding.
type StatusSegmentKind int

const (
	StatusSegState StatusSegmentKind = iota
	StatusSegModel
	StatusSegPath
	StatusSegUsage
	StatusSegBalance
	StatusSegMode
	StatusSegMeta
	// StatusSegProvider distinguishes the provider identity from other metadata
	// such as the git branch. Incremental status updates use this semantic
	// anchor to preserve the canonical model → provider → balance order.
	StatusSegProvider
)

// RunState is the primary chat/runtime state shown in the status line.
type RunState string

const (
	RunReady     RunState = "Ready"
	RunStreaming RunState = "Streaming"
	RunThinking  RunState = "Thinking"
	RunWaiting   RunState = "Waiting"
	RunError     RunState = "Error"
	RunIdle      RunState = "Idle"
	RunRunning   RunState = "Running"
	RunWorking   RunState = "Working"
	RunReasoning RunState = "Reasoning"
)

// StatusSegment is one foldable piece of the status line.
type StatusSegment struct {
	Kind     StatusSegmentKind
	Text     string
	Priority int
	MinWidth int
	Link     string
	Role     Role
}

// StatusLineModel is the structured status line input.
type StatusLineModel struct {
	State     RunState
	StateText string // optional display override for State
	StateRole Role   // optional semantic role override (for example Approval)
	// HideState omits the implicit Ready/state prefix. This is used by idle
	// model-first status lines where the first segment is already meaningful.
	HideState bool
	Segments  []StatusSegment
	Separator string
}

// StatusLineDocument builds a render document for the status line.
// Layout folding by width is applied when width > 0.
func StatusLineDocument(model StatusLineModel, width int) render.Document {
	sep := model.Separator
	if sep == "" {
		sep = " · "
	}
	stateText := ""
	if !model.HideState {
		stateText = model.StateText
		if stateText == "" {
			stateText = string(model.State)
		}
		if stateText == "" {
			stateText = string(RunReady)
		}
	}

	spans := make([]render.Span, 0, 1+2*len(model.Segments))
	if stateText != "" {
		stateRole := model.StateRole
		if stateRole == "" {
			stateRole = roleForRunState(model.State, stateText)
		}
		spans = append(spans, render.Span{
			Text:  stateText,
			Style: render.Style{Role: string(stateRole)},
		})
	}

	segments := model.Segments
	if width > 0 {
		segments = foldSegments(stateText, segments, sep, width)
	}
	for _, seg := range segments {
		if strings.TrimSpace(seg.Text) == "" {
			continue
		}
		role := seg.Role
		if role == "" {
			role = RoleTextMuted
		}
		if len(spans) > 0 {
			spans = append(spans, render.Span{
				Text:  sep,
				Style: render.Style{Role: string(RoleTextMuted)},
			})
		}
		spans = append(spans, render.Span{
			Text:  seg.Text,
			Style: render.Style{Role: string(role)},
			Link:  seg.Link,
		})
	}

	line := render.Line{Spans: spans}
	if width > 0 && render.LineWidth(line) > width {
		line = render.Truncate(line, width, "...")
	}
	return render.SingleLineDoc(line.Spans...)
}

func roleForRunState(state RunState, text string) Role {
	switch state {
	case RunReady, RunIdle:
		return RoleSuccess
	case RunStreaming, RunRunning, RunWorking:
		return RoleTool
	case RunThinking, RunReasoning:
		return RoleReasoning
	case RunWaiting:
		return RoleWarning
	case RunError:
		return RoleError
	}
	return roleForStateText(text)
}

func classifyRunState(text string) RunState {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "ready", "idle", "就绪", "已完成":
		return RunReady
	case "streaming", "running", "working", "输出中", "执行工具", "规划中":
		return RunStreaming
	case "thinking", "reasoning", "思考":
		return RunThinking
	case "waiting", "pending", "busy", "等待", "等待审批", "等待回答", "审批", "回答", "选择", "确认", "密钥", "导航", "选择选项", "确认操作", "输入密钥", "面板导航", "停止中", "停止":
		return RunWaiting
	case "error", "failed", "interrupted", "cancelled", "canceled", "失败":
		return RunError
	default:
		if strings.HasPrefix(text, "执行工具") {
			return RunStreaming
		}
		return RunState(text)
	}
}

func roleForStateText(text string) Role {
	switch classifyRunState(text) {
	case RunReady, RunIdle:
		return RoleSuccess
	case RunStreaming, RunRunning, RunWorking:
		return RoleTool
	case RunThinking, RunReasoning:
		return RoleReasoning
	case RunWaiting:
		return RoleWarning
	case RunError:
		return RoleError
	default:
		return RoleInfo
	}
}

func foldSegments(stateText string, segments []StatusSegment, sep string, width int) []StatusSegment {
	if width <= 0 || len(segments) == 0 {
		return segments
	}
	// Copy and sort by priority descending so we drop lowest priority first.
	remaining := append([]StatusSegment(nil), segments...)
	for len(remaining) > 0 {
		w := render.Width(stateText)
		for _, seg := range remaining {
			w += render.Width(sep) + render.Width(seg.Text)
		}
		if w <= width {
			return remaining
		}
		// Drop lowest priority (highest Priority number wins retention? Plan:
		// "按 priority 从低到高删除" — delete from low to high, so low number = more important.
		dropAt := 0
		for i := 1; i < len(remaining); i++ {
			if remaining[i].Priority > remaining[dropAt].Priority {
				dropAt = i
			}
		}
		remaining = append(remaining[:dropAt], remaining[dropAt+1:]...)
	}
	return remaining
}
