package output

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
