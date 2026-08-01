package commands

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// This file is the explicit output adapter allowlist for structured commands.
// Owned interactive output goes only through RenderCommandDocument. os.Stdout is
// used solely by Plain/JSON/noninteractive compatibility projections.

func renderChatCommandResult(session *ChatSession, result CommandResult, noInteractive bool) error {
	doc := result.Document()
	if strings.TrimSpace(ui.RenderDocumentPlain(doc)) == "" {
		return nil
	}

	ownedInteractive := !noInteractive && session != nil && !session.NoInteractive && !session.JSONOutput
	if ownedInteractive {
		if session.Interaction == nil {
			return fmt.Errorf("structured command requires an interaction coordinator")
		}
		if !session.Interaction.RenderCommandDocument(doc) {
			return fmt.Errorf("structured command document was not committed")
		}
		return nil
	}

	return writeChatCommandResultPlain(os.Stdout, result)
}

func writeChatCommandResultPlain(writer io.Writer, result CommandResult) error {
	if writer == nil {
		return fmt.Errorf("structured command plain writer is nil")
	}
	text := ui.RenderDocumentPlain(result.Document())
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	_, err := io.WriteString(writer, text)
	return err
}

// writeLegacyChatDebugDisplay keeps direct handleCommand callers compatible
// while sharing the structured document as the single source of truth. Normal
// interactive dispatch never reaches this adapter.
func writeLegacyChatDebugDisplay(session *ChatSession) error {
	result := CommandResult{
		Blocks: []RenderBlock{{Document: buildChatDebugDisplayDocument(session)}},
		Action: CommandContinue,
	}
	text := ui.RenderDocumentANSI(result.Document())
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	_, err := io.WriteString(os.Stdout, text)
	return err
}
