package toolbroker

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type capturingUserInputHandler struct {
	requests []UserQuestionRequest
	answer   string
}

func (h *capturingUserInputHandler) AskUserQuestion(ctx context.Context, req UserQuestionRequest) (string, error) {
	h.requests = append(h.requests, req)
	return h.answer, nil
}

func TestBrokerExecuteToolCallAskUserQuestionUsesDeterministicQuestionID(t *testing.T) {
	handler := &capturingUserInputHandler{answer: "workspace-root"}
	broker := &Broker{UserInput: handler}

	call := types.ToolCall{
		ID:   "toolcall_ask_1",
		Name: ToolAskUserQuestion,
		Args: map[string]interface{}{
			"prompt":      "Where should I run this command?",
			"suggestions": []interface{}{"workspace-root", "repo-root"},
			"required":    true,
		},
	}

	rawFirst, _, err := broker.ExecuteToolCall(context.Background(), "session-1", call)
	if err != nil {
		t.Fatalf("first ask_user_question call failed: %v", err)
	}
	rawSecond, _, err := broker.ExecuteToolCall(context.Background(), "session-1", call)
	if err != nil {
		t.Fatalf("second ask_user_question call failed: %v", err)
	}

	first, ok := rawFirst.(AskUserQuestionResult)
	if !ok {
		t.Fatalf("expected AskUserQuestionResult, got %T", rawFirst)
	}
	second, ok := rawSecond.(AskUserQuestionResult)
	if !ok {
		t.Fatalf("expected AskUserQuestionResult, got %T", rawSecond)
	}
	if first.QuestionID != second.QuestionID {
		t.Fatalf("expected deterministic question id, got %q and %q", first.QuestionID, second.QuestionID)
	}
	if len(handler.requests) != 2 {
		t.Fatalf("expected 2 captured requests, got %d", len(handler.requests))
	}
	if handler.requests[0].ID != handler.requests[1].ID {
		t.Fatalf("expected user input request ids to match, got %q and %q", handler.requests[0].ID, handler.requests[1].ID)
	}
}
