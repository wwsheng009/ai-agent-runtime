package agent

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func subagentDecodeArgs(agents ...map[string]interface{}) map[string]interface{} {
	items := make([]interface{}, 0, len(agents))
	for _, agent := range agents {
		items = append(items, agent)
	}
	return map[string]interface{}{"agents": items}
}

func decodeSubagentTasksError(t *testing.T, args map[string]interface{}) string {
	t.Helper()
	tasks, err := decodeSubagentTasks(args)
	if err == nil {
		t.Fatalf("expected decode to fail, got %d tasks: %+v", len(tasks), tasks)
	}
	return err.Error()
}

func TestDecodeSubagentTasksRejectsInvalidTopLevelOptions(t *testing.T) {
	agent := map[string]interface{}{"id": "writer", "goal": "apply the patch"}

	cases := []struct {
		name string
		args map[string]interface{}
		want string
	}{
		{
			name: "misspelled execution mode",
			args: map[string]interface{}{"agents": []interface{}{agent}, "execution_mode": "backgroud"},
			want: `execution_mode "backgroud" is not supported`,
		},
		{
			name: "numeric execution mode",
			args: map[string]interface{}{"agents": []interface{}{agent}, "execution_mode": 1},
			want: `field "execution_mode" must be a JSON string`,
		},
		{
			name: "string wait timeout",
			args: map[string]interface{}{"agents": []interface{}{agent}, "wait_timeout_sec": "30"},
			want: `field "wait_timeout_sec" must be a JSON number`,
		},
		{
			name: "numeric idempotency key",
			args: map[string]interface{}{"agents": []interface{}{agent}, "batch_idempotency_key": 7},
			want: `field "batch_idempotency_key" must be a JSON string`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			message := decodeSubagentTasksError(t, testCase.args)
			if !strings.Contains(message, testCase.want) {
				t.Fatalf("expected %q in error, got %q", testCase.want, message)
			}
		})
	}

	// Accepted spellings must keep working, including the documented synonyms.
	tasks, err := decodeSubagentTasks(map[string]interface{}{
		"agents":                []interface{}{agent},
		"execution_mode":        "BACKGROUND",
		"wait_timeout_sec":      30,
		"batch_idempotency_key": "parent-turn-1",
	})
	if err != nil {
		t.Fatalf("expected valid top-level options to decode, got %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected one task, got %d", len(tasks))
	}
}

func TestDecodeSubagentTasksRejectsUnknownDependency(t *testing.T) {
	message := decodeSubagentTasksError(t, subagentDecodeArgs(
		map[string]interface{}{"id": "writer", "goal": "apply the patch"},
		map[string]interface{}{
			"id":         "verifier",
			"goal":       "check the patch",
			"depends_on": []interface{}{"writter"},
		},
	))

	for _, want := range []string{"depends on unknown agent", `"writter"`, `"verifier"`, "writer"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q does not mention %q", message, want)
		}
	}
}

func TestDecodeSubagentTasksRejectsSelfDependency(t *testing.T) {
	message := decodeSubagentTasksError(t, subagentDecodeArgs(
		map[string]interface{}{
			"id":         "writer",
			"goal":       "apply the patch",
			"depends_on": []interface{}{"writer"},
		},
	))
	if !strings.Contains(message, "cannot depend on itself") {
		t.Fatalf("error %q does not report self dependency", message)
	}
}

func TestDecodeSubagentTasksRejectsCyclicDependency(t *testing.T) {
	message := decodeSubagentTasksError(t, subagentDecodeArgs(
		map[string]interface{}{
			"id":         "writer",
			"goal":       "apply the patch",
			"depends_on": []interface{}{"verifier"},
		},
		map[string]interface{}{
			"id":         "verifier",
			"goal":       "check the patch",
			"depends_on": []interface{}{"writer"},
		},
	))
	if !strings.Contains(message, "cyclic dependencies") {
		t.Fatalf("error %q does not report a cycle", message)
	}
}

func TestDecodeSubagentTasksRejectsDuplicateAndBlankIDs(t *testing.T) {
	duplicate := decodeSubagentTasksError(t, subagentDecodeArgs(
		map[string]interface{}{"id": "worker", "goal": "first"},
		map[string]interface{}{"id": "worker", "goal": "second"},
	))
	if !strings.Contains(duplicate, "duplicated") {
		t.Fatalf("error %q does not report a duplicate id", duplicate)
	}

	blank := decodeSubagentTasksError(t, subagentDecodeArgs(
		map[string]interface{}{"id": "   ", "goal": "first"},
	))
	if !strings.Contains(blank, "missing id") {
		t.Fatalf("error %q does not report a blank id", blank)
	}
}

func TestDecodeSubagentTasksNormalizesDifficultyAndRejectsUnknown(t *testing.T) {
	tasks, err := decodeSubagentTasks(subagentDecodeArgs(
		map[string]interface{}{"id": "writer", "goal": "apply the patch", "difficulty": " MEDIUM "},
	))
	if err != nil {
		t.Fatalf("difficulty synonym should be accepted: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Difficulty != "normal" {
		t.Fatalf("difficulty synonym was not canonicalized: %+v", tasks)
	}

	message := decodeSubagentTasksError(t, subagentDecodeArgs(
		map[string]interface{}{"id": "writer", "goal": "apply the patch", "difficulty": "spicy"},
	))
	for _, want := range []string{"invalid difficulty", `"spicy"`, "easy|normal|hard|expert"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q does not mention %q", message, want)
		}
	}
}

func TestDecodeSubagentTasksAcceptsValidDependencyGraph(t *testing.T) {
	tasks, err := decodeSubagentTasks(subagentDecodeArgs(
		map[string]interface{}{"id": "writer", "goal": "apply the patch", "difficulty": "hard"},
		map[string]interface{}{
			"id":         "verifier",
			"goal":       "check the patch",
			"depends_on": []interface{}{" writer "},
		},
	))
	if err != nil {
		t.Fatalf("valid dependency graph should decode: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].ID != "writer" || tasks[0].Difficulty != "hard" {
		t.Fatalf("first task not preserved: %+v", tasks[0])
	}
	if tasks[1].ID != "verifier" || len(tasks[1].DependsOn) != 1 {
		t.Fatalf("second task not preserved: %+v", tasks[1])
	}
}

func TestDecodeSubagentTasksNormalizesCompletionRequirementAndRejectsUnknown(t *testing.T) {
	tasks, err := decodeSubagentTasks(subagentDecodeArgs(
		map[string]interface{}{"id": "worker", "goal": "finish the task", "completion_requirement": " COMPLETE-TASK "},
	))
	if err != nil {
		t.Fatalf("completion requirement alias should be accepted: %v", err)
	}
	if len(tasks) != 1 || tasks[0].CompletionRequirement != CompletionRequirementCompleteTask {
		t.Fatalf("completion requirement alias was not canonicalized: %+v", tasks)
	}

	tasks, err = decodeSubagentTasks(subagentDecodeArgs(
		map[string]interface{}{"id": "worker", "goal": "finish the task", "completionRequirement": "none"},
	))
	if err != nil {
		t.Fatalf("camelCase completion requirement should be accepted: %v", err)
	}
	if len(tasks) != 1 || tasks[0].CompletionRequirement != CompletionRequirementNone {
		t.Fatalf("camelCase completion requirement lost: %+v", tasks)
	}

	message := decodeSubagentTasksError(t, subagentDecodeArgs(
		map[string]interface{}{"id": "worker", "goal": "finish the task", "completion_requirement": "must_complete"},
	))
	for _, want := range []string{"invalid completion_requirement", `"must_complete"`, "none|complete_task"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q does not mention %q", message, want)
		}
	}
}

func TestDecodeSubagentTasksRejectsMistypedFields(t *testing.T) {
	cases := []struct {
		name  string
		agent map[string]interface{}
		want  string
	}{
		{
			name:  "dependency list sent as a string",
			agent: map[string]interface{}{"id": "verifier", "goal": "check", "depends_on": "writer"},
			want:  `field "depends_on" must be a JSON array`,
		},
		{
			name:  "non-string dependency item",
			agent: map[string]interface{}{"id": "verifier", "goal": "check", "depends_on": []interface{}{123}},
			want:  `field "depends_on" item 0 must be a non-empty string`,
		},
		{
			name:  "budget sent as text",
			agent: map[string]interface{}{"id": "writer", "goal": "patch", "budget_tokens": "many"},
			want:  `field "budget_tokens" must be a JSON number`,
		},
		{
			name:  "read_only sent as text",
			agent: map[string]interface{}{"id": "writer", "goal": "patch", "read_only": "true"},
			want:  `field "read_only" must be a JSON boolean`,
		},
		{
			name:  "patch item not an object",
			agent: map[string]interface{}{"id": "writer", "goal": "patch", "patches": []interface{}{"--- a/x"}},
			want:  `field "patches" item 0 must be an object`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			message := decodeSubagentTasksError(t, subagentDecodeArgs(testCase.agent))
			if !strings.Contains(message, testCase.want) {
				t.Fatalf("error %q does not mention %q", message, testCase.want)
			}
		})
	}
}

func TestDecodeSubagentTasksAcceptsNumericAndBooleanFields(t *testing.T) {
	tasks, err := decodeSubagentTasks(subagentDecodeArgs(map[string]interface{}{
		"id":              "writer",
		"goal":            "apply the patch",
		"budget_tokens":   float64(1200),
		"timeout":         90,
		"read_only":       true,
		"tools_whitelist": []interface{}{"shell"},
		"patches":         []interface{}{map[string]interface{}{"path": "a.go"}},
	}))
	if err != nil {
		t.Fatalf("well-typed fields should decode: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	task := tasks[0]
	if task.BudgetTokens != 1200 || task.TimeoutSec != 90 || !task.ReadOnly {
		t.Fatalf("numeric/boolean fields were not preserved: %+v", task)
	}
	if len(task.ToolsWhitelist) != 1 || len(task.PatchContext) != 1 {
		t.Fatalf("array fields were not preserved: %+v", task)
	}
}

// TestSanitizeDecodedSubagentOptionRange pins the non-fatal policy from
// docs/plan/task-difficulty-model-routing-plan.md §32.2 ("use the configured
// default or clip to the allowed range") together with the warning that keeps the
// fallback visible. Before this, intValue truncated or overflowed and the
// downstream "not set" checks (resolver.go, child_factory.go) turned the value
// into the routed/host default without saying so.
func TestSanitizeDecodedSubagentOptionRange(t *testing.T) {
	cases := []struct {
		name        string
		value       interface{}
		wantValue   int
		wantWarning string
	}{
		{name: "absent", value: nil, wantValue: 0, wantWarning: ""},
		{name: "decoded JSON number", value: float64(1200), wantValue: 1200, wantWarning: ""},
		{name: "native int", value: 90, wantValue: 90, wantWarning: ""},
		{name: "json.Number", value: json.Number("45"), wantValue: 45, wantWarning: ""},
		{
			name:        "fractional value is truncated and reported",
			value:       float64(1200.5),
			wantValue:   1200,
			wantWarning: "budget_tokens_truncated_to_integer",
		},
		{
			name:        "zero is ignored and reported",
			value:       float64(0),
			wantValue:   0,
			wantWarning: "budget_tokens_ignored_non_positive",
		},
		{
			name:        "negative is ignored and reported",
			value:       -1,
			wantValue:   0,
			wantWarning: "budget_tokens_ignored_non_positive",
		},
		{
			name:        "huge value is clipped instead of overflowing the int conversion",
			value:       float64(1e30),
			wantValue:   subagentNumericOptionMax,
			wantWarning: "budget_tokens_clamped_to_range",
		},
		{
			name:        "value just above the cap is clipped",
			value:       float64(subagentNumericOptionMax) + 1,
			wantValue:   subagentNumericOptionMax,
			wantWarning: "budget_tokens_clamped_to_range",
		},
		{
			name:        "positive infinity is clipped",
			value:       math.Inf(1),
			wantValue:   subagentNumericOptionMax,
			wantWarning: "budget_tokens_clamped_to_range",
		},
		{
			name:        "not a number is ignored",
			value:       math.NaN(),
			wantValue:   0,
			wantWarning: "budget_tokens_ignored_non_numeric",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			value, warning := sanitizeDecodedSubagentOptionRange("budget_tokens", testCase.value)
			if value != testCase.wantValue {
				t.Fatalf("value = %d, want %d", value, testCase.wantValue)
			}
			if warning != testCase.wantWarning {
				t.Fatalf("warning = %q, want %q", warning, testCase.wantWarning)
			}
		})
	}
}

// TestDecodeSubagentTasksReportsSanitizedNumericOptions checks the wiring: the
// sanitized value lands on the task and the reason it changed is attached to
// task.RouteWarnings, which child_factory forwards to the child's route warnings
// instead of dropping the caller's request in silence.
func TestDecodeSubagentTasksReportsSanitizedNumericOptions(t *testing.T) {
	tasks, err := decodeSubagentTasks(subagentDecodeArgs(
		map[string]interface{}{
			"id":            "writer",
			"goal":          "apply the patch",
			"budget_tokens": float64(-500),
			"timeout":       float64(90.25),
		},
		map[string]interface{}{
			"id":            "verifier",
			"goal":          "check the patch",
			"budget_tokens": float64(1e18),
			"timeout":       0,
		},
	))
	if err != nil {
		t.Fatalf("out-of-range numeric options must stay non-fatal: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}

	writer := tasks[0]
	if writer.BudgetTokens != 0 {
		t.Fatalf("a non-positive budget must fall back to the default, got %d", writer.BudgetTokens)
	}
	if writer.TimeoutSec != 90 {
		t.Fatalf("expected the fractional timeout to truncate to 90, got %d", writer.TimeoutSec)
	}
	for _, want := range []string{"budget_tokens_ignored_non_positive", "timeout_truncated_to_integer"} {
		if !containsWarning(writer.RouteWarnings, want) {
			t.Fatalf("writer route warnings %v do not mention %q", writer.RouteWarnings, want)
		}
	}

	verifier := tasks[1]
	if verifier.BudgetTokens != subagentNumericOptionMax {
		t.Fatalf("expected the oversized budget to clip to %d, got %d", subagentNumericOptionMax, verifier.BudgetTokens)
	}
	for _, want := range []string{"budget_tokens_clamped_to_range", "timeout_ignored_non_positive"} {
		if !containsWarning(verifier.RouteWarnings, want) {
			t.Fatalf("verifier route warnings %v do not mention %q", verifier.RouteWarnings, want)
		}
	}
	if verifier.TimeoutSec != 0 {
		t.Fatalf("expected timeout 0 to stay unset so the routed default applies, got %d", verifier.TimeoutSec)
	}
}

func containsWarning(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
