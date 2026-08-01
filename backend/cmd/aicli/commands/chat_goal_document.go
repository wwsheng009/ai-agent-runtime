package commands

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

// This file owns the renderer-neutral documents for the structured /goal
// path. It never touches the terminal: owned interactive dispatch renders the
// documents through the interaction coordinator, while plain/JSON projections
// use their own renderer. Side effects (persistence, status transitions,
// objective send) live in the structured dispatch branch, not here.

// buildChatGoalStatusDocument renders the /goal status view (no argument or
// "status"). A nil goal renders the same "当前会话未设置 goal" message as the
// legacy path; goal errors are handled by the dispatch branch (legacy error
// path stays visible in every mode).
func buildChatGoalStatusDocument(goal *runtimegoal.SessionGoal) render.Document {
	if goal == nil {
		return render.SingleLineDoc(render.TextSpan("当前会话未设置 goal"))
	}
	return goalSummaryDocument(*goal)
}

// buildChatGoalClearDocument renders the /goal clear confirmation.
func buildChatGoalClearDocument() render.Document {
	return render.SingleLineDoc(render.TextSpan("Goal 已清除"))
}

// buildChatGoalNoneDocument renders the soft "no goal" message used by
// pause/resume/complete when the session has no goal yet.
func buildChatGoalNoneDocument() render.Document {
	return render.SingleLineDoc(render.TextSpan("当前会话未设置 goal"))
}

// buildChatGoalMutationDocument renders a pause/resume/complete confirmation
// followed by the updated goal summary, matching the legacy two-step output.
func buildChatGoalMutationDocument(message string, goal runtimegoal.SessionGoal) render.Document {
	return goalSummaryDocument(goal, message)
}

// buildChatGoalSetDocument renders the /goal <objective> confirmation: the
// "Goal 已设置" line (plus the replaced-goal note when one existed) followed
// by the new goal summary. The objective chat send is NOT part of this
// document: dispatch triggers it after committing the cell
// (CommandResult.SendObjective).
func buildChatGoalSetDocument(replaced string, goal *runtimegoal.SessionGoal) render.Document {
	if goal == nil {
		return render.SingleLineDoc(render.TextSpan("Goal 已设置" + replaced))
	}
	return goalSummaryDocument(*goal, "Goal 已设置"+replaced)
}

func goalSummaryDocument(goal runtimegoal.SessionGoal, prefix ...string) render.Document {
	var lines []string
	lines = append(lines, prefix...)
	lines = append(lines, strings.Split(formatGoalSummary(goal), "\n")...)
	return textLinesDocument(lines)
}

func textLinesDocument(lines []string) render.Document {
	docLines := make([]render.Line, 0, len(lines))
	for _, line := range lines {
		docLines = append(docLines, render.Line{Spans: []render.Span{render.TextSpan(line)}})
	}
	return render.LinesDoc(docLines...)
}
