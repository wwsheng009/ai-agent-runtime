package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
)

// errACPQuestionUnavailable marks "no question panel on this host". Callers use
// it to fall back to the fail-closed non-interactive message.
var errACPQuestionUnavailable = fmt.Errorf("acp question panel unavailable: %w", acp.ErrClientQuestionsUnsupported)

// acpQuestionRequesterAware is implemented by the ACP session host so the
// server can hand it the client-bound question requester after initialize.
// It mirrors acp.SessionEmitterAware: hosts that do not implement it simply
// keep the non-interactive default answer.
type acpQuestionRequesterAware interface {
	SetQuestionRequester(requester acp.QuestionRequester)
}

// SetQuestionRequester implements acpQuestionRequesterAware.
func (h *acpSessionHost) SetQuestionRequester(requester acp.QuestionRequester) {
	if h == nil {
		return
	}
	h.questionRequester = requester
}

// AskQuestion implements the chatRuntimeEventBridge.askQuestion hook for
// headless ACP sessions: instead of failing the turn it forwards the prompt to
// the client panel through the session/request_question extension.
//
// Returning an empty answer with a nil error is intentional. A dismissed panel
// is a valid outcome (the user chose not to answer), and the runtime continues
// with its own best judgment; only a transport failure becomes an error so the
// caller can fall back to its fail-closed path.
func (b *acpEventBridge) AskQuestion(prompt string, suggestions []string, required bool) (string, error) {
	if b == nil {
		return "", errACPQuestionUnavailable
	}
	b.mu.Lock()
	requester := b.question
	sessionID := b.sessionID
	b.mu.Unlock()
	if requester == nil {
		return "", errACPQuestionUnavailable
	}

	params := acp.RequestQuestionParams{
		SessionID:   strings.TrimSpace(sessionID),
		QuestionID:  "question_" + generateItemID(),
		Prompt:      prompt,
		Suggestions: append([]string(nil), suggestions...),
		Required:    required,
	}
	ctx := context.Background()
	if b.promptCtx != nil {
		ctx = b.promptCtx
	}
	result, err := requester.RequestQuestion(ctx, params)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Answer), nil
}

// SetQuestionRequester binds the client-bound requester used by AskQuestion.
func (b *acpEventBridge) SetQuestionRequester(requester acp.QuestionRequester) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.question = requester
}
