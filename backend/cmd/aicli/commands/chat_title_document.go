package commands

import "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"

// buildChatTitleUpdatedDocument renders the successful /title or /rename
// confirmation without writing the terminal. The mutation itself is performed
// by the structured command dispatcher before this retained command cell is
// committed.
func buildChatTitleUpdatedDocument() render.Document {
	return render.SingleLineDoc(render.TextSpan("会话标题已更新"))
}
