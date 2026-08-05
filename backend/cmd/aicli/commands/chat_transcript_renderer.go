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
	// replay marks a pure history-replay renderer: user echo must not restore the
	// live composer (that would grow the bottom reserve and bill surface scroll
	// compensation into the replayed transcript). Live adapters leave this false.
	replay bool
}

func newAICLITranscriptRenderer(session *ChatSession) *aicliTranscriptRenderer {
	return &aicliTranscriptRenderer{session: session}
}

// newAICLIReplayTranscriptRenderer builds a renderer for replaying already-final
// history. It routes user echo through the side-effect-free replay path so
// replay never touches live prompt/compensation state.
func newAICLIReplayTranscriptRenderer(session *ChatSession) *aicliTranscriptRenderer {
	return &aicliTranscriptRenderer{session: session, replay: true}
}

func (r *aicliTranscriptRenderer) RenderUser(content string) bool {
	if r == nil || !shouldRenderInteractiveOutput(r.session) || strings.TrimSpace(content) == "" {
		return false
	}
	if r.session.Interaction != nil {
		if r.replay {
			r.session.Interaction.RenderReplayedUserInput(content)
		} else {
			r.session.Interaction.RenderSubmittedUserInput(content)
		}
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
	// Formatter may emit SGR; DisplayAssistantMessage preserves it.
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
		if r.replay {
			r.session.Interaction.RenderAsyncLine(content)
		} else {
			r.session.Interaction.RenderLocalSupplement(content)
		}
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
	if r.session.Interaction != nil {
		if r.replay {
			return r.session.Interaction.RenderReplayedToolChainEvent(event)
		}
		return r.session.Interaction.RenderToolChainEvent(event)
	}
	rendered := renderSharedChatToolEvent(event)
	if strings.TrimSpace(rendered) == "" {
		return false
	}
	return r.RenderSupplement(rendered)
}
