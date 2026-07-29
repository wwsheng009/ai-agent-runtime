// Package cell provides typed timeline/tool presentation models that emit
// render.Document values instead of pre-colored display strings.
package cell

import (
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// TimelineKind classifies a transcript/timeline event for role selection.
type TimelineKind int

const (
	TimelineTool TimelineKind = iota
	TimelineReasoning
	TimelinePlanning
	TimelineApproval
	TimelineQuestion
	TimelineTeam
	TimelineTask
	TimelineProgress
	TimelineNotice
	TimelineThinking
	TimelineTip
	TimelineInput
	TimelineUnknown
)

// EventStatus is the lifecycle state of a timeline event.
type EventStatus int

const (
	StatusNone EventStatus = iota
	StatusPending
	StatusRunning
	StatusSuccess
	StatusError
	StatusDenied
	StatusInfo
)

// TimelineEvent is the structured replacement for "[tool] ..." string prefixes.
type TimelineEvent struct {
	Kind   TimelineKind
	Status EventStatus
	Title  string
	Detail string
	// Details are already laid-out continuation lines. Leading indentation is
	// preserved while each line receives the muted semantic role.
	Details []string
	Source  string
	Time    time.Time
	// Tag overrides the visible bracketed prefix while Kind remains the semantic
	// role owner. This covers runtime labels such as [team summary] without
	// sending them back through a keyword parser.
	Tag string
	// Marker is an optional leading bullet ("• " / "* ") preserved in Document
	// plain layout so legacy transcript lines stay byte-identical.
	Marker string
	// SuppressKindPrefix omits the [kind] tag. Used for bare bullet notices
	// such as "• Edited file.go" that must not become "[notice] Edited file.go".
	SuppressKindPrefix bool
}

// RoleForKind maps timeline kinds to semantic style roles.
func RoleForKind(kind TimelineKind) style.Role {
	switch kind {
	case TimelineTool:
		return style.RoleTool
	case TimelineReasoning, TimelineThinking, TimelinePlanning:
		return style.RoleReasoning
	case TimelineApproval:
		return style.RoleApproval
	case TimelineQuestion:
		return style.RoleInfo
	case TimelineTeam, TimelineTask, TimelineProgress, TimelineNotice, TimelineTip, TimelineInput:
		return style.RoleTimeline
	default:
		return style.RoleTimeline
	}
}

// PrefixForKind returns the stable visible tag for a kind (including brackets).
func PrefixForKind(kind TimelineKind) string {
	switch kind {
	case TimelineTool:
		return "[tool]"
	case TimelineReasoning:
		return "[reasoning]"
	case TimelinePlanning:
		return "[planning]"
	case TimelineApproval:
		return "[approval]"
	case TimelineQuestion:
		return "[question]"
	case TimelineTeam:
		return "[team]"
	case TimelineTask:
		return "[task]"
	case TimelineProgress:
		return "[progress]"
	case TimelineThinking:
		return "[thinking]"
	case TimelineTip:
		return "[tip]"
	case TimelineInput:
		return "[input]"
	case TimelineNotice:
		return "[notice]"
	default:
		return "[event]"
	}
}

// Document converts a timeline event into a single-line (or multi-line detail) document.
func (e TimelineEvent) Document() render.Document {
	prefix := PrefixForKind(e.Kind)
	if e.Kind == TimelineTool {
		switch e.Status {
		case StatusDenied:
			prefix = "[tool denied]"
		case StatusSuccess:
			prefix = "[tool done]"
		default:
			prefix = "[tool]"
		}
	}
	if e.Tag != "" {
		prefix = e.Tag
	}
	if e.SuppressKindPrefix {
		prefix = ""
	}

	role := RoleForKind(e.Kind)
	if e.Status == StatusError || e.Status == StatusDenied {
		role = style.RoleError
	}
	var spans []render.Span
	if e.Marker != "" {
		spans = append(spans, render.Span{
			Text:  e.Marker,
			Style: render.Style{Role: string(role), Bold: true},
		})
	}
	if prefix != "" {
		spans = append(spans, render.Span{
			Text:  prefix,
			Style: render.Style{Role: string(role), Bold: true},
		})
	}

	title := e.Title
	if title != "" {
		// Bare bullet notices historically color the whole line with the kind role.
		titleRole := style.RoleTextSecondary
		titleBold := false
		sep := " "
		if e.SuppressKindPrefix {
			// Marker already carries its trailing space ("• "); title follows directly.
			titleRole = role
			titleBold = true
			sep = ""
		} else if prefix == "" && e.Marker == "" {
			sep = ""
		}
		spans = append(spans, render.Span{
			Text:  sep + title,
			Style: render.Style{Role: string(titleRole), Bold: titleBold},
		})
	}
	if e.Detail != "" {
		spans = append(spans, render.Span{
			Text:  " " + e.Detail,
			Style: render.Style{Role: string(style.RoleTextMuted), Dim: true},
		})
	}

	lines := []render.Line{{Spans: spans}}
	for _, detailLine := range e.Details {
		lines = append(lines, render.Line{Spans: []render.Span{{
			Text:  detailLine,
			Style: render.Style{Role: string(style.RoleTextMuted), Dim: true},
		}}})
	}
	return render.Document{Blocks: []render.Block{{
		Kind:  render.BlockParagraph,
		Lines: lines,
	}}}
}

// FormatANSI encodes the event with the given theme context.
func (e TimelineEvent) FormatANSI(theme style.ThemeContext) string {
	return style.RenderDocument(e.Document(), theme)
}

// FormatPlain returns visible text only.
func (e TimelineEvent) FormatPlain() string {
	return render.PlainBackend{}.Render(e.Document())
}
