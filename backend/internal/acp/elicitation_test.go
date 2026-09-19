package acp

import (
	"context"
	"testing"
)

// fakeElicitBackend is a SessionBackend that also implements the optional
// ElicitationRequester extension, recording every elicitation the ACP layer
// forwards to it.
type fakeElicitBackend struct {
	elicited  int
	gotParams ElicitationRequestParams
	result    ElicitationResult
	err       error
}

func (b *fakeElicitBackend) NewSession(ctx context.Context, req NewSessionRequest) (NewSessionResponse, error) {
	return NewSessionResponse{SessionID: "sess-1"}, nil
}

func (b *fakeElicitBackend) Prompt(ctx context.Context, req PromptRequest, emit Emitter) (PromptResponse, error) {
	return PromptResponse{}, nil
}

func (b *fakeElicitBackend) Cancel(ctx context.Context, sessionID string) error { return nil }

func (b *fakeElicitBackend) CreateElicitation(ctx context.Context, params ElicitationRequestParams) (ElicitationResult, error) {
	b.elicited++
	b.gotParams = params
	return b.result, b.err
}

var _ SessionBackend = (*fakeElicitBackend)(nil)

// TestElicitationRequesterDetected asserts the server-side type assertion the
// ACP layer uses to decide whether a backend can service elicitation/create is
// satisfied by an elicitation-capable backend.
func TestElicitationRequesterDetected(t *testing.T) {
	t.Parallel()

	var backend SessionBackend = &fakeElicitBackend{}
	if _, ok := backend.(ElicitationRequester); !ok {
		t.Fatal("elicitation-capable backend must satisfy ElicitationRequester")
	}

	plain := &fakeBackend{}
	if _, ok := SessionBackend(plain).(ElicitationRequester); ok {
		t.Fatal("backend without Elicit must not satisfy ElicitationRequester")
	}
}

// TestElicitationResultAccepted covers the small result helpers used by the
// aicli bridge to translate a client panel answer back into a tool argument.
func TestElicitationResultAccepted(t *testing.T) {
	t.Parallel()

	accepted := ElicitationResult{
		Action:  ElicitationActionAccept,
		Content: map[string]interface{}{"answer": "use postgres"},
	}
	if !accepted.Accepted() {
		t.Fatalf("Accepted() = false for action %q", accepted.Action)
	}
	if got := accepted.Answer(); got != "use postgres" {
		t.Fatalf("Answer() = %q, want %q", got, "use postgres")
	}

	for _, action := range []string{ElicitationActionDecline, ElicitationActionCancel} {
		res := ElicitationResult{Action: action, Content: map[string]interface{}{"answer": "ignored"}}
		if res.Accepted() {
			t.Fatalf("Accepted() = true for action %q", action)
		}
		if got := res.Answer(); got != "" {
			t.Fatalf("Answer() = %q for action %q, want empty", got, action)
		}
	}

	// Accept with an empty/absent answer is "no answer", not an error.
	if got := (ElicitationResult{Action: ElicitationActionAccept}).Answer(); got != "" {
		t.Fatalf("Answer() = %q for empty content, want empty", got)
	}
}

// TestElicitationRequesterUnsupportedErrors pins the sentinel errors the ACP
// layer returns when the connected client cannot render an interactive panel.
func TestElicitationRequesterUnsupportedErrors(t *testing.T) {
	t.Parallel()

	if ErrClientElicitationUnsupported == nil {
		t.Fatal("ErrClientElicitationUnsupported must be non-nil")
	}
	if ErrClientQuestionUnsupported == nil {
		t.Fatal("ErrClientQuestionUnsupported must be non-nil")
	}
	if ErrClientElicitationUnsupported == ErrClientQuestionUnsupported {
		t.Fatal("the two unsupported-client sentinels must be distinct")
	}
}
