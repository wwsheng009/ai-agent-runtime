package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
)

// errACPQuestionUnavailable marks "no question panel on this host". Callers use
// it to fall back to the fail-closed non-interactive message.
var errACPQuestionUnavailable = fmt.Errorf("acp question panel unavailable: %w", acp.ErrClientQuestionUnsupported)

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

// acpElicitationRequesterAware is implemented by the ACP session host so the
// server can hand it the standard-protocol elicitation requester after
// initialize. Hosts that do not implement it simply keep the legacy path.
type acpElicitationRequesterAware interface {
	SetElicitationRequester(requester acp.ElicitationRequester)
}

// SetElicitationRequester implements acpElicitationRequesterAware.
func (h *acpSessionHost) SetElicitationRequester(requester acp.ElicitationRequester) {
	if h == nil {
		return
	}
	h.elicitationRequester = requester
}

// AskQuestion implements the chatRuntimeEventBridge.askQuestion hook for
// headless ACP sessions: instead of failing the turn it forwards the prompt to
// the client panel, preferring the standard ACP v1 elicitation/create method
// and falling back to the legacy session/request_question extension.
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
	elicitation := b.elicitation
	requester := b.question
	sessionID := strings.TrimSpace(b.sessionID)
	ctx := b.promptCtx
	b.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}

	if elicitation != nil {
		params := acp.NewFormElicitationParams(sessionID, prompt, suggestions, required)
		result, err := elicitation.CreateElicitation(ctx, params)
		if err == nil {
			return result.Answer(), nil
		}
		// A conformant client advertises only what it implements, but some still
		// lack the handler; degrade to the legacy extension on method-not-found
		// instead of failing the turn. Other errors are real transport or user
		// failures and must surface unchanged.
		if requester == nil || !acp.IsMethodNotFound(err) {
			return "", err
		}
	}
	if requester == nil {
		return "", errACPQuestionUnavailable
	}

	params := acp.RequestQuestionParams{
		SessionID:   sessionID,
		QuestionID:  "question_" + generateItemID(),
		Prompt:      prompt,
		Suggestions: append([]string(nil), suggestions...),
		Required:    required,
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

// SetElicitationRequester binds the standard-protocol requester used by
// AskQuestion. It takes precedence over SetQuestionRequester when both are set.
func (b *acpEventBridge) SetElicitationRequester(requester acp.ElicitationRequester) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.elicitation = requester
}
