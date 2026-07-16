package commands

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// aicliTranscriptRenderer is the shared renderer for complete chat transcript
// blocks. Live event adapters and persisted-history replay both feed this
// renderer so reasoning, assistant Markdown, tools, and prompt/surface
// coordination use the same presentation path.
//
// Streaming deltas intentionally remain in aicliEventRenderer: they are an
// input transport concern. Once a complete block exists, it belongs here.
type aicliTranscriptRenderer struct {
	session *ChatSession
}

func newAICLITranscriptRenderer(session *ChatSession) *aicliTranscriptRenderer {
	return &aicliTranscriptRenderer{session: session}
}

func (r *aicliTranscriptRenderer) RenderUser(content string) bool {
	if r == nil || !shouldRenderInteractiveOutput(r.session) || strings.TrimSpace(content) == "" {
		return false
	}
	if r.session.Interaction != nil {
		r.session.Interaction.RenderSubmittedUserInput(content)
		return true
	}
	ui.DisplayUserMessage(content)
	return true
}

func (r *aicliTranscriptRenderer) RenderAssistant(content string) bool {
	if r == nil || !shouldRenderInteractiveOutput(r.session) || strings.TrimSpace(content) == "" {
		return false
	}
	if r.session.Interaction != nil {
		r.session.Interaction.RenderAssistant(content)
		return true
	}
	formatted := content
	if r.session.Formatter != nil {
		formatted = r.session.Formatter.Format(content)
	}
	ui.DisplayAssistantMessage(formatted)
	return true
}

func (r *aicliTranscriptRenderer) RenderSystem(content string) bool {
	if r == nil || !shouldRenderInteractiveOutput(r.session) || strings.TrimSpace(content) == "" {
		return false
	}
	if r.session.Interaction != nil {
		r.session.Interaction.RenderAsyncLine(ui.FormatSystemMessage(content))
		return true
	}
	ui.DisplaySystemMessage(content)
	return true
}

func (r *aicliTranscriptRenderer) RenderSupplement(content string) bool {
	if r == nil || !shouldRenderInteractiveOutput(r.session) || strings.TrimSpace(content) == "" {
		return false
	}
	if r.session.Interaction != nil {
		r.session.Interaction.RenderAsyncLine(content)
		return true
	}
	fmt.Println(ui.FormatAssistantSupplementBlock(content))
	return true
}

func (r *aicliTranscriptRenderer) RenderReasoning(block *runtimetypes.ReasoningBlock) bool {
	if r == nil || !shouldRenderChatReasoning(r.session) || block == nil {
		return false
	}
	lines := chatReasoningLines(block)
	if len(lines) == 0 {
		return false
	}
	return r.RenderSupplement(strings.Join(lines, "\n"))
}

func (r *aicliTranscriptRenderer) RenderToolEvent(event runtimechatcore.ChatEvent) bool {
	if r == nil || !shouldRenderInteractiveOutput(r.session) {
		return false
	}
	rendered := renderSharedChatToolEvent(event)
	if strings.TrimSpace(rendered) == "" {
		return false
	}
	return r.RenderSupplement(rendered)
}
