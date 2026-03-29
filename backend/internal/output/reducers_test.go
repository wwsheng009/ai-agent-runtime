package output

import (
	"context"
	"strings"
	"testing"
)

func TestGoTestJSONReducer_Reduce(t *testing.T) {
	reducer := &GoTestJSONReducer{}
	text := strings.Join([]string{
		`{"Time":"2026-03-12T12:00:00Z","Action":"run","Package":"github.com/demo/pkg","Test":"TestFoo"}`,
		`{"Time":"2026-03-12T12:00:01Z","Action":"output","Package":"github.com/demo/pkg","Test":"TestFoo","Output":"panic: nil pointer dereference"}`,
		`{"Time":"2026-03-12T12:00:02Z","Action":"fail","Package":"github.com/demo/pkg","Test":"TestFoo","Elapsed":0.12}`,
	}, "\n")

	env, ok, err := reducer.Reduce(context.Background(), ReducedInput{
		Raw: RawToolResult{
			ToolName:   "run_command_readonly",
			ToolCallID: "call-1",
		},
		Text: text,
	})
	if err != nil {
		t.Fatalf("reduce go test output: %v", err)
	}
	if !ok {
		t.Fatal("expected reducer to match go test json")
	}
	if !strings.Contains(env.Summary, "Failed targets: github.com/demo/pkg (TestFoo)") {
		t.Fatalf("unexpected summary: %q", env.Summary)
	}
}

func TestPlaywrightSnapshotReducer_Reduce(t *testing.T) {
	reducer := &PlaywrightSnapshotReducer{}
	text := strings.Join([]string{
		"Playwright trace start",
		"URL: https://example.test/dashboard",
		"Console Error: TypeError: failed to fetch",
		"Failed request: GET https://example.test/api/data 500",
	}, "\n")

	env, ok, err := reducer.Reduce(context.Background(), ReducedInput{
		Raw: RawToolResult{
			ToolName:   "playwright_debug",
			ToolCallID: "call-2",
		},
		Text: text,
	})
	if err != nil {
		t.Fatalf("reduce playwright output: %v", err)
	}
	if !ok {
		t.Fatal("expected reducer to match playwright output")
	}
	if !strings.Contains(env.Summary, "1 console errors, 1 failed requests") {
		t.Fatalf("unexpected summary: %q", env.Summary)
	}
}

func TestGitLogReducer_Reduce(t *testing.T) {
	reducer := &GitLogReducer{}
	text := strings.Join([]string{
		"commit a1b2c3d4",
		"Author: Alice <alice@example.com>",
		"",
		"    Fix auth fallback",
		"internal/runtime/agent/loop.go | 12 ++++++",
		"internal/runtime/output/gateway.go | 8 ++++",
		"",
		"commit deadbee1",
		"Author: Bob <bob@example.com>",
		"",
		"    Revert \"Temporary bypass\"",
		"internal/runtime/contextmgr/manager.go | 6 +++",
	}, "\n")

	env, ok, err := reducer.Reduce(context.Background(), ReducedInput{
		Raw: RawToolResult{
			ToolName: "git_log",
		},
		Text: text,
	})
	if err != nil {
		t.Fatalf("reduce git log output: %v", err)
	}
	if !ok {
		t.Fatal("expected reducer to match git log output")
	}
	if !strings.Contains(env.Summary, "2 commits") {
		t.Fatalf("unexpected summary: %q", env.Summary)
	}
	if env.Metadata["commit_count"] != 2 {
		t.Fatalf("expected commit_count=2, got %v", env.Metadata["commit_count"])
	}
}

func TestJSONReducer_Reduce(t *testing.T) {
	reducer := &JSONReducer{}
	text := `{
  "status": "ok",
  "count": 3,
  "items": [
    {"id": "a1", "name": "alpha"},
    {"id": "b2", "name": "beta"}
  ],
  "warnings": ["slow query"]
}`

	env, ok, err := reducer.Reduce(context.Background(), ReducedInput{
		Raw: RawToolResult{
			ToolName:   "fetch_status",
			ToolCallID: "call-4",
		},
		Text: text,
	})
	if err != nil {
		t.Fatalf("reduce json output: %v", err)
	}
	if !ok {
		t.Fatal("expected reducer to match json output")
	}
	if !strings.Contains(env.Summary, "Parsed JSON object") {
		t.Fatalf("unexpected summary: %q", env.Summary)
	}
	if env.Metadata["json_type"] != "object" {
		t.Fatalf("expected json_type=object, got %v", env.Metadata["json_type"])
	}
	if env.Metadata["collection_count"] != 2 {
		t.Fatalf("expected collection_count=2, got %v", env.Metadata["collection_count"])
	}
}

func TestJSONReducer_Reduce_EmitsRuntimeScopeFacts(t *testing.T) {
	reducer := &JSONReducer{}
	text := `{
  "team_id": "team-1",
  "task_id": "task-7",
  "message_count": 1,
  "from_agent": "lead",
  "to_agent": "planner"
}`

	env, ok, err := reducer.Reduce(context.Background(), ReducedInput{
		Raw: RawToolResult{
			ToolName:   "read_mailbox_digest",
			ToolCallID: "call-7",
		},
		Text: text,
	})
	if err != nil {
		t.Fatalf("reduce json output: %v", err)
	}
	if !ok {
		t.Fatal("expected reducer to match json output")
	}
	for _, want := range []string{
		"team_id=team-1",
		"task_id=task-7",
		"message_count=1",
		"from_agent=lead",
		"to_agent=planner",
	} {
		if !strings.Contains(env.Summary, want) {
			t.Fatalf("expected summary to contain %q, got %q", want, env.Summary)
		}
	}
}

func TestJSONReducer_Reduce_EmitsQuestionAnswerFacts(t *testing.T) {
	reducer := &JSONReducer{}
	text := `{
  "question_id": "q-1",
  "answer": "在当前目录",
  "required": true
}`

	env, ok, err := reducer.Reduce(context.Background(), ReducedInput{
		Raw: RawToolResult{
			ToolName:   "ask_user_question",
			ToolCallID: "call-9",
		},
		Text: text,
	})
	if err != nil {
		t.Fatalf("reduce json output: %v", err)
	}
	if !ok {
		t.Fatal("expected reducer to match json output")
	}
	for _, want := range []string{
		"question_id=q-1",
		"answer=在当前目录",
	} {
		if !strings.Contains(env.Summary, want) {
			t.Fatalf("expected summary to contain %q, got %q", want, env.Summary)
		}
	}
}

func TestTableReducer_Reduce(t *testing.T) {
	reducer := &TableReducer{}
	text := strings.Join([]string{
		"NAME,STATUS,DURATION",
		"job-a,passed,12s",
		"job-b,failed,45s",
		"job-c,running,5s",
	}, "\n")

	env, ok, err := reducer.Reduce(context.Background(), ReducedInput{
		Raw: RawToolResult{
			ToolName:   "list_jobs",
			ToolCallID: "call-5",
		},
		Text: text,
	})
	if err != nil {
		t.Fatalf("reduce table output: %v", err)
	}
	if !ok {
		t.Fatal("expected reducer to match table output")
	}
	if !strings.Contains(env.Summary, "Parsed csv table: 3 rows x 3 columns") {
		t.Fatalf("unexpected summary: %q", env.Summary)
	}
	if env.Metadata["row_count"] != 3 {
		t.Fatalf("expected row_count=3, got %v", env.Metadata["row_count"])
	}
}

func TestLogReducer_Reduce(t *testing.T) {
	reducer := &LogReducer{}
	text := strings.Join([]string{
		"2026-03-14 10:00:01 INFO starting worker",
		"2026-03-14 10:00:02 WARN retrying request",
		"2026-03-14 10:00:03 ERROR failed to fetch artifact",
		"2026-03-14 10:00:04 INFO worker stopped",
	}, "\n")

	env, ok, err := reducer.Reduce(context.Background(), ReducedInput{
		Raw: RawToolResult{
			ToolName:   "service_output",
			ToolCallID: "call-6",
		},
		Text: text,
	})
	if err != nil {
		t.Fatalf("reduce log output: %v", err)
	}
	if !ok {
		t.Fatal("expected reducer to match log output")
	}
	if !strings.Contains(env.Summary, "1 error-like entries, 1 warnings") {
		t.Fatalf("unexpected summary: %q", env.Summary)
	}
	if env.Metadata["line_count"] != 4 {
		t.Fatalf("expected line_count=4, got %v", env.Metadata["line_count"])
	}
}
