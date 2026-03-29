package output

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-gateway/ai-agent-runtime/internal/artifact"
	"github.com/ai-gateway/ai-agent-runtime/internal/toolbroker"
)

func TestGateway_StoresRawOutputAndReturnsReducedEnvelope(t *testing.T) {
	store, err := artifact.NewStore(nil)
	if err != nil {
		t.Fatalf("create artifact store: %v", err)
	}
	defer func() { _ = store.Close() }()

	gateway := NewGateway(store, NewTextReducer(80, 3))
	rawOutput := strings.Join([]string{
		"line 1: preparing",
		"line 2: unique-needle",
		"line 3: details",
		"line 4: more details",
		"line 5: tail",
	}, "\n")

	envelope, err := gateway.Process(context.Background(), RawToolResult{
		SessionID:  "session-1",
		ToolName:   "run_command_readonly",
		ToolCallID: "call-1",
		Content:    rawOutput,
		Metadata: map[string]interface{}{
			"source": "test",
		},
	})
	if err != nil {
		t.Fatalf("process tool output: %v", err)
	}
	if envelope == nil {
		t.Fatal("expected envelope, got nil")
	}
	if envelope.Metadata["reducer"] != "text_truncation" {
		t.Fatalf("expected text_truncation reducer, got %v", envelope.Metadata["reducer"])
	}
	if len(envelope.ArtifactIDs) != 1 {
		t.Fatalf("expected 1 artifact id, got %d", len(envelope.ArtifactIDs))
	}
	if strings.Contains(envelope.Summary, "line 5: tail") {
		t.Fatal("expected summary to be truncated before the last line")
	}

	record, err := store.Get(context.Background(), envelope.ArtifactIDs[0])
	if err != nil {
		t.Fatalf("load artifact: %v", err)
	}
	if record == nil {
		t.Fatal("expected stored artifact record")
	}
	if record.Content != rawOutput {
		t.Fatalf("expected raw output to be stored intact, got %q", record.Content)
	}

	hits, err := store.Search(context.Background(), "session-1", "unique-needle", 5)
	if err != nil {
		t.Fatalf("search artifacts: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected artifact search to find stored record")
	}
	if hits[0].ID != envelope.ArtifactIDs[0] {
		t.Fatalf("expected hit id %s, got %s", envelope.ArtifactIDs[0], hits[0].ID)
	}
}

func TestGateway_DefaultReducers_HandleCommonFormats(t *testing.T) {
	store, err := artifact.NewStore(nil)
	if err != nil {
		t.Fatalf("create artifact store: %v", err)
	}
	defer func() { _ = store.Close() }()

	gateway := NewGateway(store)
	testCases := []struct {
		name            string
		content         string
		expectedReducer string
	}{
		{
			name: "json",
			content: `{
  "status": "ok",
  "items": [{"id":"a"},{"id":"b"}]
}`,
			expectedReducer: "json_summary",
		},
		{
			name: "table",
			content: strings.Join([]string{
				"NAME\tSTATUS",
				"job-a\tpassed",
				"job-b\tfailed",
			}, "\n"),
			expectedReducer: "table_summary",
		},
		{
			name: "log",
			content: strings.Join([]string{
				"2026-03-14 10:00:01 INFO starting worker",
				"2026-03-14 10:00:02 ERROR failed to fetch artifact",
			}, "\n"),
			expectedReducer: "log_summary",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := gateway.Process(context.Background(), RawToolResult{
				SessionID:  "session-1",
				ToolName:   "test_tool",
				ToolCallID: "call-" + tc.name,
				Content:    tc.content,
			})
			if err != nil {
				t.Fatalf("process %s output: %v", tc.name, err)
			}
			if envelope == nil {
				t.Fatalf("expected envelope for %s", tc.name)
			}
			if envelope.Metadata["reducer"] != tc.expectedReducer {
				t.Fatalf("expected reducer %s, got %v", tc.expectedReducer, envelope.Metadata["reducer"])
			}
			if len(envelope.ArtifactIDs) != 1 {
				t.Fatalf("expected artifact refs for %s, got %v", tc.name, envelope.ArtifactIDs)
			}
			if strings.TrimSpace(envelope.Summary) == "" {
				t.Fatalf("expected non-empty summary for %s", tc.name)
			}
		})
	}
}

func TestGateway_AskUserQuestionResultPreservesAnswerInJSONSummary(t *testing.T) {
	gateway := NewGateway(nil)

	envelope, err := gateway.Process(context.Background(), RawToolResult{
		SessionID:  "session-question",
		ToolName:   toolbroker.ToolAskUserQuestion,
		ToolCallID: "call-question",
		Content: toolbroker.AskUserQuestionResult{
			QuestionID: "question-1",
			Answer:     "provided answer 42",
		},
	})
	if err != nil {
		t.Fatalf("process ask_user_question result: %v", err)
	}
	if envelope == nil {
		t.Fatal("expected envelope")
	}
	if envelope.Metadata["reducer"] != "json_summary" {
		t.Fatalf("expected json_summary reducer, got %v", envelope.Metadata["reducer"])
	}
	if !strings.Contains(envelope.Summary, "answer=provided answer 42") {
		t.Fatalf("expected reducer summary to preserve answer, got %q", envelope.Summary)
	}
	if strings.TrimSpace(envelope.Render()) == "" {
		t.Fatalf("expected rendered envelope to stay non-empty, got %+v", envelope)
	}
}
