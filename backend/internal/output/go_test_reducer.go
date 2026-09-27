package output

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// GoTestJSONReducer 压缩 `go test -json` 输出。
type GoTestJSONReducer struct{}

// Name 返回 reducer 名称。
func (r *GoTestJSONReducer) Name() string {
	return "go_test_json"
}

// Reduce 解析 line-delimited JSON，提取失败包和首批高价值输出。
func (r *GoTestJSONReducer) Reduce(_ context.Context, input ReducedInput) (*Envelope, bool, error) {
	if !looksLikeGoTestJSON(input.Raw.ToolName, input.Text) {
		return nil, false, nil
	}

	type event struct {
		Action  string `json:"Action"`
		Package string `json:"Package"`
		Test    string `json:"Test"`
		Output  string `json:"Output"`
	}

	failures := make([]string, 0, 4)
	outputHints := make([]string, 0, 4)

	for _, line := range strings.Split(strings.ReplaceAll(input.Text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}

		if e.Action == "fail" {
			label := e.Package
			if e.Test != "" {
				label = fmt.Sprintf("%s (%s)", e.Package, e.Test)
			}
			if strings.TrimSpace(label) != "" {
				failures = appendUniqueLimited(failures, label, 4)
			}
		}

		if e.Action == "output" {
			hint := summarizeLine(e.Output, 160)
			if hint != "" && !strings.HasPrefix(strings.ToLower(hint), "=== run") {
				outputHints = appendUniqueLimited(outputHints, hint, 4)
			}
		}
	}

	summaryParts := []string{"Parsed go test -json output."}
	if len(failures) > 0 {
		summaryParts = append(summaryParts, "Failed targets: "+strings.Join(failures, "; "))
	}
	if len(outputHints) > 0 {
		summaryParts = append(summaryParts, "First hints: "+strings.Join(outputHints, " | "))
	}
	if len(failures) == 0 && len(outputHints) == 0 {
		summaryParts = append(summaryParts, summarizeLine(input.Text, 220))
	}

	return &Envelope{
		ToolName:   input.Raw.ToolName,
		ToolCallID: input.Raw.ToolCallID,
		Summary:    strings.Join(summaryParts, "\n"),
		Error:      strings.TrimSpace(input.Raw.Error),
		Metadata: map[string]interface{}{
			"failed_targets": failures,
			"output_hints":   outputHints,
		},
	}, true, nil
}

func looksLikeGoTestJSON(toolName, text string) bool {
	lowerTool := strings.ToLower(toolName)
	lowerText := strings.ToLower(text)
	return (strings.Contains(lowerTool, "go") || strings.Contains(lowerTool, "run_command")) &&
		strings.Contains(lowerText, `"action":"`) &&
		strings.Contains(lowerText, `"package":"`)
}

// GoTestTextReducer extracts actionable failures from ordinary go test output.
type GoTestTextReducer struct{}

func (r *GoTestTextReducer) Name() string {
	return "go_test_text"
}

func (r *GoTestTextReducer) Reduce(_ context.Context, input ReducedInput) (*Envelope, bool, error) {
	if !looksLikeGoTestInput(input) || looksLikeGoTestJSON(input.Raw.ToolName, input.Text) {
		return nil, false, nil
	}

	lines := normalizedNonEmptyLines(input.Text)
	failedTests := make([]string, 0, 8)
	failedTargets := make([]string, 0, 6)
	passedTargets := make([]string, 0, 6)
	signals := make([]string, 0, 8)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(trimmed, "--- FAIL:"):
			failedTests = appendUniqueLimited(failedTests, summarizeLine(strings.TrimPrefix(trimmed, "--- FAIL:"), 180), 8)
			signals = appendUniqueLimited(signals, summarizeLine(trimmed, 220), 8)
		case strings.HasPrefix(trimmed, "FAIL\t") || strings.HasPrefix(trimmed, "FAIL "):
			failedTargets = appendUniqueLimited(failedTargets, summarizeLine(strings.TrimSpace(strings.TrimPrefix(trimmed, "FAIL")), 180), 6)
		case strings.HasPrefix(trimmed, "ok\t") || strings.HasPrefix(trimmed, "ok "):
			passedTargets = appendUniqueLimited(passedTargets, summarizeLine(strings.TrimSpace(strings.TrimPrefix(trimmed, "ok")), 180), 6)
		case isHighSignalGoTestLine(lower):
			signals = appendUniqueLimited(signals, summarizeLine(trimmed, 220), 8)
		}
	}

	status := "passed"
	if strings.TrimSpace(input.Raw.Error) != "" || len(failedTests) > 0 || len(failedTargets) > 0 {
		status = "failed"
	}
	if strings.Contains(strings.ToLower(input.Raw.Error), "timeout") || strings.Contains(strings.ToLower(input.Raw.Error), "deadline exceeded") {
		status = "timed out"
	}

	parts := []string{fmt.Sprintf("Parsed go test output: %s.", status)}
	if status == "timed out" {
		if budget := goTestTimeoutBudget(input.Raw.Metadata); budget != "" {
			parts = append(parts, budget)
		}
	}
	if len(failedTests) > 0 {
		parts = append(parts, "Failed tests: "+strings.Join(failedTests, "; "))
	}
	if len(failedTargets) > 0 {
		parts = append(parts, "Failed targets: "+strings.Join(failedTargets, "; "))
	}
	if len(signals) > 0 {
		parts = append(parts, "Failure signals: "+strings.Join(signals, " | "))
	} else if status != "passed" && len(lines) > 0 {
		parts = append(parts, "Recent output: "+strings.Join(lastStrings(lines, 3), " | "))
	}
	if status == "timed out" && len(lines) == 0 {
		parts = append(parts, goTestTimeoutWithoutOutputHint)
	}
	if status == "passed" && len(passedTargets) > 0 {
		parts = append(parts, "Passed targets: "+strings.Join(passedTargets, "; "))
	}

	return &Envelope{
		ToolName: input.Raw.ToolName, ToolCallID: input.Raw.ToolCallID,
		Summary: strings.Join(parts, "\n"), Error: strings.TrimSpace(input.Raw.Error),
		Metadata: map[string]interface{}{
			"go_test_status": status, "failed_tests": failedTests,
			"failed_targets": failedTargets, "passed_targets": passedTargets,
			"failure_signals": signals,
		},
	}, true, nil
}

func looksLikeGoTestInput(input ReducedInput) bool {
	name := strings.ToLower(strings.TrimSpace(input.Raw.ToolName))
	if strings.Contains(name, "go_test") {
		return true
	}
	command := strings.ToLower(strings.TrimSpace(metadataString(input.Raw.Metadata, "command")))
	fields := strings.Fields(command)
	for index := 0; index+1 < len(fields); index++ {
		if (fields[index] == "go" || fields[index] == "go.exe") && fields[index+1] == "test" {
			return true
		}
	}
	return false
}

func isHighSignalGoTestLine(lower string) bool {
	for _, marker := range []string{
		"panic:", "fatal error:", "[build failed]", "undefined:",
		"error trace:", "expected", "actual", "want:", "got:",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func lastStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) == 0 {
		return nil
	}
	if len(values) <= limit {
		return append([]string(nil), values...)
	}
	return append([]string(nil), values[len(values)-limit:]...)
}

// goTestTimeoutWithoutOutputHint is appended when a timed-out go test run has
// no captured output at all. In that shape the kill almost always happened
// before the test binary printed anything (build/compile), or a output-slicing
// pipeline swallowed it; misreading it as a hung test sends the next attempt
// down the wrong path (chasing a stack dump that never existed).
const goTestTimeoutWithoutOutputHint = "No output was captured before the timeout: the process was most likely killed during build/compile, or a `| tail`/`| head` pipeline swallowed the output. This is not a Go test hang (a hung test prints a panic/stack dump); re-run without the pipe and pass an explicit timeout if the build needs longer."

// goTestTimeoutBudget attributes a timed-out run to the timeout layer that
// actually owned the budget. Shell results record the resolved budget
// (timeout_source/timeout_effective_ms/timeout_requested_ms) in metadata, so a
// timeout can be explained instead of only reported.
func goTestTimeoutBudget(metadata map[string]interface{}) string {
	source := strings.TrimSpace(metadataString(metadata, "timeout_source"))
	effectiveMs := goTestMetadataInt(metadata, "timeout_effective_ms")
	requestedMs := goTestMetadataInt(metadata, "timeout_requested_ms")
	if source == "" && effectiveMs <= 0 {
		return ""
	}
	fields := make([]string, 0, 3)
	if source != "" {
		fields = append(fields, "source="+source)
	}
	if effectiveMs > 0 {
		fields = append(fields, "effective="+(time.Duration(effectiveMs)*time.Millisecond).String())
	}
	if requestedMs > 0 && requestedMs != effectiveMs {
		fields = append(fields, "requested="+(time.Duration(requestedMs)*time.Millisecond).String())
	}
	return "Timeout budget: " + strings.Join(fields, " ")
}

// goTestMetadataInt is metadataInt plus the nested "tool_metadata" fallback
// that metadataString already applies for shell emitters.
func goTestMetadataInt(metadata map[string]interface{}, key string) int {
	if len(metadata) == 0 {
		return 0
	}
	if value := metadataInt(metadata, key); value != 0 {
		return value
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		return metadataInt(nested, key)
	}
	return 0
}
