package chataloganalytics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInferSessionProjectUsesExplicitProjectPath(t *testing.T) {
	project := filepath.Join(t.TempDir(), "explicit-project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	file := &chatSessionFile{
		ProjectPath:      project,
		WorkingDirectory: filepath.Join(t.TempDir(), "ignored"),
	}
	if got := inferSessionProject(file); got != filepath.Clean(project) {
		t.Fatalf("expected explicit project %q, got %q", project, got)
	}
}

func TestRollupUsesLoggedTitleAndInitialMessageFallback(t *testing.T) {
	dir := SessionDir{SessionID: "chat-session", Directory: "2026/07/27"}
	rollup := rollupFromChatFile(dir, "", &chatSessionFile{
		SessionID:        "chat-session",
		RuntimeSessionID: "runtime-session",
		Title:            "  Analyze\nusage diagnostics  ",
		InitialMessage:   "ignored fallback",
	})
	if rollup.Title != "Analyze usage diagnostics" || rollup.TitleSource != "chat_log" || rollup.RuntimeSessionID != "runtime-session" {
		t.Fatalf("unexpected logged title projection: %+v", rollup)
	}

	fallback := rollupFromChatFile(dir, "", &chatSessionFile{InitialMessage: "  First user prompt  "})
	if fallback.Title != "First user prompt" || fallback.TitleSource != "initial_message" {
		t.Fatalf("unexpected initial message title fallback: %+v", fallback)
	}
}

func TestInferSessionProjectGroupsNestedWorkdirsAtGitRoot(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	backend := filepath.Join(project, "backend")
	frontend := filepath.Join(project, "frontend")
	for _, path := range []string{filepath.Join(project, ".git"), backend, frontend} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	toolCall := func(path string) chatSessionMessage {
		content, err := json.Marshal(map[string]interface{}{
			"function": "shell_command",
			"args":     map[string]interface{}{"workdir": path},
		})
		if err != nil {
			t.Fatal(err)
		}
		return chatSessionMessage{MessageType: "tool_call", Content: content}
	}
	file := &chatSessionFile{Messages: []chatSessionMessage{
		toolCall(backend), toolCall(other), toolCall(frontend),
	}}
	if got := inferSessionProject(file); got != filepath.Clean(project) {
		t.Fatalf("expected most frequent git root %q, got %q", project, got)
	}
}

func TestProjectFilterGroupingAndDimensions(t *testing.T) {
	project := filepath.Clean(filepath.Join(t.TempDir(), "project"))
	rollups := []SessionRollup{
		{SessionID: "one", Project: project, Provider: "openai", Directory: "2026/07/27"},
		{SessionID: "two", Project: project, Provider: "openai", Directory: "2026/07/28"},
		{SessionID: "three", Provider: "anthropic", Directory: "2026/07/28"},
	}
	if !matchRollup(rollups[0], Query{Project: project}) || matchRollup(rollups[2], Query{Project: project}) {
		t.Fatal("project filter did not distinguish known and unknown projects")
	}
	groups := groupRollups(rollups, normalizeGroupBy("project"))
	if len(groups) != 2 || groups[0].Key != project || groups[0].Sessions != 2 {
		t.Fatalf("unexpected project groups: %+v", groups)
	}
}

func TestParseRequestFinishedLine(t *testing.T) {
	line := `[2026-07-27 07:34:12.393] [llm-debug] request_finished trace_id=abc step=1 success=true usage_prompt_tokens=15053 usage_completion_tokens=939 usage_total_tokens=15992 usage_cached_tokens=128 usage_source=provider`
	step, ok := parseRequestFinishedLine(line)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if step.TraceID != "abc" || step.Step != 1 || !step.Success {
		t.Fatalf("unexpected step meta: %+v", step)
	}
	if step.PromptTokens != 15053 || step.CompletionTokens != 939 || step.TotalTokens != 15992 || step.CachedTokens != 128 {
		t.Fatalf("unexpected tokens: %+v", step)
	}
	if step.Timestamp.IsZero() {
		t.Fatal("expected timestamp")
	}
}

func TestParseRequestFinishedLineClassifiesQuotedError(t *testing.T) {
	line := `[2026-07-27 07:34:12.393] [llm-debug] request_finished trace_id=abc step=2 success=false error="streaming aggregate call failed after retries: HTTP 502: upstream failed"`
	step, ok := parseRequestFinishedLine(line)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if step.Success || step.ErrorCategory != "upstream_unavailable" {
		t.Fatalf("unexpected error classification: %+v", step)
	}
}

func TestParseDebugRequestFinishedDerivesContextUtilization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")
	contents := `[2026-07-27 07:34:10.000] [llm-debug] request_started trace_id=abc step=1 context_window_tokens=100000 prompt_budget=90000
[2026-07-27 07:34:12.000] [llm-debug] request_finished trace_id=abc step=1 success=true usage_prompt_tokens=1000 usage_completion_tokens=100 usage_total_tokens=70100
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	steps, err := ParseDebugRequestFinished(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected one step, got %d", len(steps))
	}
	if got := steps[0].ContextUtilization; got != 0.7 {
		t.Fatalf("expected context utilization 0.7, got %v", got)
	}
	if steps[0].DurationMs != 2000 {
		t.Fatalf("expected duration 2000ms, got %d", steps[0].DurationMs)
	}
}

func TestDiscoverSessionDirsFlatAndPartitioned(t *testing.T) {
	root := t.TempDir()

	flatID := "20260726_101010.000_flat1"
	flatDir := filepath.Join(root, flatID)
	if err := os.MkdirAll(flatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flatDir, "debug.log"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	partID := "20260727_073349.024_dbc55eea"
	partDir := filepath.Join(root, "2026", "07", "27", partID)
	if err := os.MkdirAll(partDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partDir, "chat_test.json"), []byte(`{"session_id":"`+partID+`","status":"completed","provider":"openai","model":"gpt-5","summary":{"total_requests":2,"total_responses":2,"total_tool_calls":1,"total_tokens":100,"total_duration_ms":1200}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	dirs, err := DiscoverSessionDirs(root, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 2 {
		t.Fatalf("expected 2 dirs, got %d", len(dirs))
	}

	var sawPart bool
	for _, dir := range dirs {
		if dir.SessionID == partID {
			sawPart = true
			if dir.Directory != "2026/07/27" {
				t.Fatalf("expected date directory, got %q", dir.Directory)
			}
		}
	}
	if !sawPart {
		t.Fatal("missing partitioned session")
	}
}

func TestListAndSummarizeAggregation(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.Local)

	writeSession := func(id, provider, model, status string, tokens int, day time.Time) {
		dir := filepath.Join(root, day.Format("2006"), day.Format("01"), day.Format("02"), id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{
  "session_id": "` + id + `",
  "start_time": "` + day.Format(time.RFC3339) + `",
  "status": "` + status + `",
  "provider": "` + provider + `",
  "model": "` + model + `",
  "summary": {
    "total_requests": 3,
    "total_responses": 3,
    "total_tool_calls": 2,
    "total_tokens": ` + itoa(tokens) + `,
    "average_response_time_ms": 100,
    "total_duration_ms": 500
  }
}`
		if err := os.WriteFile(filepath.Join(dir, "chat_"+id+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		debug := `[` + day.Format("2006-01-02 15:04:05.000") + `] [llm-debug] request_finished trace_id=t1 step=1 success=true usage_prompt_tokens=10 usage_completion_tokens=5 usage_total_tokens=15
[` + day.Format("2006-01-02 15:04:05.000") + `] [llm-debug] request_finished trace_id=t1 step=2 success=false error=boom usage_prompt_tokens=1 usage_completion_tokens=0 usage_total_tokens=1
`
		if err := os.WriteFile(filepath.Join(dir, "debug.log"), []byte(debug), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeSession("20260727_080000.000_aaaa", "openai", "gpt-5", "completed", 200, now)
	writeSession("20260726_090000.000_bbbb", "anthropic", "claude", "failed", 50, now.Add(-24*time.Hour))
	_ = now

	list, err := ListSessions(root, Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 || list.Count != 2 {
		t.Fatalf("unexpected list counts: %+v", list)
	}
	if list.Totals.TotalTokens != 250 {
		t.Fatalf("expected total tokens 250, got %d", list.Totals.TotalTokens)
	}

	summary, err := Summarize(root, Query{GroupBy: "provider"})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Matched != 2 || summary.Totals.Sessions != 2 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if len(summary.Groups) != 2 {
		t.Fatalf("expected 2 provider groups, got %d", len(summary.Groups))
	}

	filtered, err := ListSessions(root, Query{Provider: "openai", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 || filtered.Sessions[0].Provider != "openai" {
		t.Fatalf("provider filter failed: %+v", filtered)
	}

	dirFiltered, err := Summarize(root, Query{Directory: "2026/07/27", GroupBy: "directory"})
	if err != nil {
		t.Fatal(err)
	}
	if dirFiltered.Matched != 1 {
		t.Fatalf("directory filter failed: %+v", dirFiltered)
	}

	detail, err := SessionUsage(root, "20260727_080000.000_aaaa")
	if err != nil {
		t.Fatal(err)
	}
	if detail.StepCount != 2 {
		t.Fatalf("expected 2 steps, got %d", detail.StepCount)
	}
	if detail.Session.LLMErrors != 1 || detail.Session.TotalTokens != 200 {
		t.Fatalf("debug enrich unexpected: %+v", detail.Session)
	}
	if detail.Session.ReconciliationStatus != "partial" || !detail.Partial {
		t.Fatalf("expected partial reconciliation metadata: %+v", detail.Session)
	}
	if len(detail.Turns) != 1 || detail.Turns[0].Outcome != "failed" {
		t.Fatalf("unexpected turn rollup: %+v", detail.Turns)
	}
}

func TestResolveSessionDir(t *testing.T) {
	root := t.TempDir()
	id := "20260727_073349.024_dbc55eea"
	dir := filepath.Join(root, "2026", "07", "27", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "debug.log"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok, err := ResolveSessionDir(root, id)
	if err != nil || !ok {
		t.Fatalf("resolve failed: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(filepath.ToSlash(got.Path), "/2026/07/27/") {
		t.Fatalf("unexpected path %q", got.Path)
	}
}

func TestResolveSessionDirRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, ok, err := ResolveSessionDir(root, `..\20260727_073349.024_dbc55eea`); err != nil || ok {
		t.Fatalf("expected traversal-shaped session id to be rejected: ok=%v err=%v", ok, err)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [32]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
