package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestInjectLiveUserInputAddsDurableUserMessagesOnce(t *testing.T) {
	consumed := false
	loop := &ReActLoop{config: &LoopReActConfig{
		LiveUserInputSource: func() LiveUserInputBatch {
			if consumed {
				return LiveUserInputBatch{}
			}
			consumed = true
			return LiveUserInputBatch{Inputs: []string{"  steer one  ", "steer two"}}
		},
	}}
	history := []types.Message{*types.NewUserMessage("original")}
	identity := NewBuilderIdentity("turn_live")
	builder := NewMessageBuilderWithIdentity(history, identity)
	promptBuilder := NewMessageBuilder(history)

	var persisted []types.Message
	persist := func(messages []types.Message) error {
		persisted = messages
		return nil
	}
	if err := loop.injectLiveUserInput(context.Background(), identity, builder, promptBuilder, persist); err != nil {
		t.Fatalf("injectLiveUserInput returned error: %v", err)
	}
	if err := loop.injectLiveUserInput(context.Background(), identity, builder, promptBuilder, persist); err != nil {
		t.Fatalf("second injectLiveUserInput returned error: %v", err)
	}

	for name, messages := range map[string][]types.Message{
		"durable history": builder.Messages(),
		"prompt history":  promptBuilder.Messages(),
		"persisted":       persisted,
	} {
		if len(messages) != 3 {
			t.Errorf("%s has %d messages, want 3: %+v", name, len(messages), messages)
			continue
		}
		if messages[1].Role != "user" || messages[1].Content != "steer one" ||
			messages[2].Role != "user" || messages[2].Content != "steer two" {
			t.Errorf("%s does not preserve injected user message order: %+v", name, messages)
		}
		if got := types.TurnID(messages[1]); got != "turn_live" {
			t.Errorf("%s injected user message turn_id = %q, want turn_live", name, got)
		}
		if types.MessageID(messages[1]) == "" {
			t.Errorf("%s injected user message is missing message_id", name)
		}
	}
}

func TestInjectLiveUserInputRestoresBatchWhenPersistenceFails(t *testing.T) {
	wantErr := errors.New("persist failed")
	restored := false
	loop := &ReActLoop{config: &LoopReActConfig{
		LiveUserInputSource: func() LiveUserInputBatch {
			return LiveUserInputBatch{
				Inputs: []string{"steer"},
				Restore: func() {
					restored = true
				},
			}
		},
	}}
	identity := NewBuilderIdentity("turn_live")
	builder := NewMessageBuilderWithIdentity(nil, identity)
	promptBuilder := NewMessageBuilder(nil)

	err := loop.injectLiveUserInput(context.Background(), identity, builder, promptBuilder, func([]types.Message) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("injectLiveUserInput error = %v, want %v", err, wantErr)
	}
	if !restored {
		t.Fatal("failed persistence must restore the claimed live input batch")
	}
}
