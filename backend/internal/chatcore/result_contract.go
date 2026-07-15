package chatcore

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentresult"
)

func buildChatResultContract(result *ChatResult) *agentresult.Result {
	if result == nil {
		return nil
	}
	if result.ResultContract != nil {
		return result.ResultContract
	}
	failed := 0
	for _, execution := range result.ToolExecutions {
		if !execution.Success {
			failed++
		}
	}
	success := strings.TrimSpace(result.Error) == "" && failed == 0
	usage := agentresult.Usage{ToolCalls: len(result.ToolExecutions), DurationMS: result.Duration.GetDuration().Milliseconds()}
	if result.Usage != nil {
		usage.InputTokens = result.Usage.PromptTokens
		usage.OutputTokens = result.Usage.CompletionTokens
		usage.TotalTokens = result.Usage.TotalTokens
	}
	contract := agentresult.FromLegacy(success, result.Output, "", result.Error, usage)
	contract.TraceID = strings.TrimSpace(result.TraceID)
	if strings.TrimSpace(result.Output) != "" {
		contract.Findings = append(contract.Findings, agentresult.Finding{ID: "answer", Summary: strings.TrimSpace(result.Output)})
	}
	for _, execution := range result.ToolExecutions {
		refs := resultMetadataRefs(execution.Metadata)
		agentresult.MergeEvidence(contract, refs...)
		if execution.Success {
			continue
		}
		message := strings.TrimSpace(execution.Error)
		if message == "" {
			message = fmt.Sprintf("tool %s failed", execution.ToolName)
		}
		contract.Errors = append(contract.Errors, agentresult.Error{Code: metadataString(execution.Metadata, "error_code"), Message: message, Retryable: metadataBool(execution.Metadata, "retryable"), EvidenceRefs: refs})
	}
	if failed > 0 && strings.TrimSpace(result.Output) != "" {
		contract.Status = agentresult.StatusPartiallyCompleted
	}
	result.ResultContract = contract
	return contract
}

func resultMetadataRefs(metadata map[string]interface{}) []string {
	refs := make([]string, 0, 4)
	for _, key := range []string{"evidence_refs", "artifact_refs", "source_refs", "event_id"} {
		switch values := metadata[key].(type) {
		case string:
			refs = append(refs, values)
		case []string:
			refs = append(refs, values...)
		case []interface{}:
			for _, value := range values {
				if text, ok := value.(string); ok {
					refs = append(refs, text)
				}
			}
		}
	}
	return refs
}

func metadataString(metadata map[string]interface{}, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func metadataBool(metadata map[string]interface{}, key string) bool {
	value, _ := metadata[key].(bool)
	return value
}
