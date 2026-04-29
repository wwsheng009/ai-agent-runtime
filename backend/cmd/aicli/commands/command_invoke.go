package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
)

type directFunctionInvokeReport struct {
	RequestedName string                 `json:"requested_name"`
	FunctionName  string                 `json:"function_name"`
	Output        string                 `json:"output,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

func handleDirectFunctionCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}

	payload, jsonOutput := extractCommandArgumentOptions(command)
	jsonOutput = jsonOutput || shouldUseSessionJSONCommandOutput(session)
	requestedName, rawArgs := splitCommandNameAndRemainder(payload)
	if requestedName == "" {
		fmt.Println(formatCommandError("错误: 需要指定 function 名称\n用法: /call <name> [args-json] 或 /tool <name> [args-json]", jsonOutput))
		return false
	}

	resolvedName, isSkill, err := resolveDirectCallableFunctionName(session, requestedName, false)
	if err != nil {
		fmt.Println(formatCommandError("错误: "+err.Error(), jsonOutput))
		return false
	}
	args, err := parseDirectFunctionArgs(rawArgs, isSkill)
	if err != nil {
		fmt.Println(formatCommandError("错误: "+err.Error(), jsonOutput))
		return false
	}

	report, err := executeDirectFunction(session, requestedName, resolvedName, args)
	if err != nil {
		fmt.Println(formatCommandError("错误: "+err.Error(), jsonOutput))
		return false
	}
	fmt.Println(formatDirectFunctionInvokeReport(report, jsonOutput))
	return false
}

func handleDirectSkillCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}

	payload, jsonOutput := extractCommandArgumentOptions(command)
	jsonOutput = jsonOutput || shouldUseSessionJSONCommandOutput(session)
	requestedName, rawPrompt := splitCommandNameAndRemainder(payload)
	if requestedName == "" {
		fmt.Println(formatCommandError("错误: 需要指定 skill 名称\n用法: /skill <name> <prompt> 或 /skill <name> {\"prompt\":\"...\"}", jsonOutput))
		return false
	}

	resolvedName, _, err := resolveDirectCallableFunctionName(session, requestedName, true)
	if err != nil {
		fmt.Println(formatCommandError("错误: "+err.Error(), jsonOutput))
		return false
	}
	args, err := parseDirectFunctionArgs(rawPrompt, true)
	if err != nil {
		fmt.Println(formatCommandError("错误: "+err.Error(), jsonOutput))
		return false
	}

	report, err := executeDirectFunction(session, requestedName, resolvedName, args)
	if err != nil {
		fmt.Println(formatCommandError("错误: "+err.Error(), jsonOutput))
		return false
	}
	fmt.Println(formatDirectFunctionInvokeReport(report, jsonOutput))
	return false
}

func shouldUseSessionJSONCommandOutput(session *ChatSession) bool {
	return session != nil && session.NoInteractive && session.JSONOutput
}

func splitCommandNameAndRemainder(payload string) (string, string) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return "", ""
	}
	if idx := strings.IndexAny(payload, " \t"); idx >= 0 {
		return strings.TrimSpace(payload[:idx]), strings.TrimSpace(payload[idx+1:])
	}
	return payload, ""
}

func resolveDirectCallableFunctionName(session *ChatSession, requestedName string, preferSkill bool) (string, bool, error) {
	catalog := ensureFunctionCatalog(session)
	if catalog == nil || catalog.Registry() == nil {
		return "", false, fmt.Errorf("function catalog 未初始化")
	}

	normalized := strings.TrimSpace(requestedName)
	if normalized == "" {
		return "", false, fmt.Errorf("function 名称不能为空")
	}
	normalized = toolnames.CanonicalOpenAIImageGenerateToolName(normalized)

	candidates := make([]string, 0, 3)
	candidates = append(candidates, normalized)
	if preferSkill || !strings.HasPrefix(normalized, skillFunctionPrefix) {
		if skillName := buildSkillFunctionName(normalized); skillName != normalized {
			candidates = append(candidates, skillName)
		}
	}

	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		if _, ok := catalog.Registry().Get(candidate); ok {
			if descriptor := catalog.Descriptor(candidate); descriptor != nil {
				return candidate, descriptor.Kind == "skill" || strings.HasPrefix(candidate, skillFunctionPrefix), nil
			}
			return candidate, strings.HasPrefix(candidate, skillFunctionPrefix), nil
		}
	}

	if preferSkill {
		return "", false, fmt.Errorf("未找到 skill: %s", requestedName)
	}
	return "", false, fmt.Errorf("未找到 function: %s", requestedName)
}

func parseDirectFunctionArgs(raw string, isSkill bool) (map[string]interface{}, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if isSkill {
			return nil, fmt.Errorf("skill 调用需要提供 prompt 或 JSON 参数")
		}
		return map[string]interface{}{}, nil
	}
	if strings.HasPrefix(raw, "{") {
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return nil, fmt.Errorf("JSON 参数解析失败: %w", err)
		}
		if args == nil {
			args = map[string]interface{}{}
		}
		return args, nil
	}
	if isSkill {
		return map[string]interface{}{"prompt": raw}, nil
	}
	return nil, fmt.Errorf("非 skill function 需要 JSON object 参数，例如 {\"prompt\":\"...\"}")
}

func executeDirectFunction(session *ChatSession, requestedName, functionName string, args map[string]interface{}) (*directFunctionInvokeReport, error) {
	catalog := ensureFunctionCatalog(session)
	if catalog == nil || catalog.Registry() == nil {
		return nil, fmt.Errorf("function registry 未初始化")
	}

	ctx := context.Background()
	if session != nil {
		if session.cancelCtx != nil {
			ctx = session.cancelCtx
		}
		if session.RequestTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, session.RequestTimeout)
			defer cancel()
		}
	}
	ctx = generatedImageToolContext(ctx, session)

	output, metadata, err := catalog.Registry().ExecuteFunctionWithMeta(ctx, functionName, args)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(functionName, skillFunctionPrefix) {
		normalizedOutput, normalizedMeta, normalizeErr := normalizeDirectSkillCommandResult(output, metadata)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		output = normalizedOutput
		metadata = normalizedMeta
	}
	return &directFunctionInvokeReport{
		RequestedName: requestedName,
		FunctionName:  functionName,
		Output:        output,
		Metadata:      metadata,
	}, nil
}

type directSkillCommandEnvelope struct {
	Success      bool                   `json:"success"`
	Output       string                 `json:"output"`
	Error        string                 `json:"error"`
	Observations []interface{}          `json:"observations"`
	Usage        map[string]interface{} `json:"usage"`
	Skill        string                 `json:"skill"`
}

func normalizeDirectSkillCommandResult(output string, metadata map[string]interface{}) (string, map[string]interface{}, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || (!strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[")) {
		return output, metadata, nil
	}

	var envelope directSkillCommandEnvelope
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return output, metadata, nil
	}

	if envelope.Error == "" && envelope.Output == "" && envelope.Skill == "" && len(envelope.Observations) == 0 && len(envelope.Usage) == 0 && !envelope.Success {
		return output, metadata, nil
	}

	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	if len(envelope.Observations) > 0 {
		metadata["skill_observations"] = envelope.Observations
	}
	if len(envelope.Usage) > 0 {
		metadata["skill_usage"] = envelope.Usage
	}
	if strings.TrimSpace(envelope.Skill) != "" {
		metadata["skill"] = envelope.Skill
	}
	if strings.TrimSpace(envelope.Error) != "" || !envelope.Success {
		errMsg := strings.TrimSpace(envelope.Error)
		if errMsg == "" {
			errMsg = "skill execution failed"
		}
		return envelope.Output, metadata, fmt.Errorf("%s", errMsg)
	}
	return envelope.Output, metadata, nil
}

func formatDirectFunctionInvokeReport(report *directFunctionInvokeReport, jsonOutput bool) string {
	if report == nil {
		return formatCommandError("direct function result is empty", jsonOutput)
	}
	if jsonOutput {
		return marshalIndentedJSON(report)
	}
	if strings.TrimSpace(report.Output) != "" {
		return report.Output
	}
	if len(report.Metadata) > 0 {
		return marshalIndentedJSON(report.Metadata)
	}
	return fmt.Sprintf("Function %s 执行完成", report.FunctionName)
}
