package skill

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

var workflowTemplatePattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.-]+)\s*\}\}`)

var systemRoleExpectedRolePattern = regexp.MustCompile(`messages?(?:\[\d+\]|\.\d+)?\.role.*\b(expected|must be|should be)\b.*\b(user|assistant|developer)\b`)

// Executor 技能执行器
type Executor struct {
	registry   *Registry
	mcpManager MCPManager
	llmRuntime *llm.LLMRuntime
}

// NewExecutor 创建执行器
func NewExecutor(registry *Registry, mcpManager MCPManager, llmRuntime *llm.LLMRuntime) *Executor {
	return &Executor{
		registry:   registry,
		mcpManager: mcpManager,
		llmRuntime: llmRuntime,
	}
}

// SetLLMRuntime 设置 LLM Runtime
func (e *Executor) SetLLMRuntime(llmRuntime *llm.LLMRuntime) {
	e.llmRuntime = llmRuntime
}

// ExecuteResult 执行结果
type ExecuteResult struct {
	SkillName    string                 `json:"skillName"`
	Success      bool                   `json:"success"`
	Output       string                 `json:"output"`
	Skill        string                 `json:"skill,omitempty"`
	Observations []types.Observation    `json:"observations,omitempty"`
	Error        string                 `json:"error,omitempty"`
	ErrorCode    string                 `json:"error_code,omitempty"`
	ErrorContext map[string]interface{} `json:"error_context,omitempty"`
	Usage        *types.TokenUsage      `json:"usage,omitempty"`
}

// Execute 执行 Skill
func (e *Executor) Execute(ctx context.Context, skill *Skill, req *types.Request) (*ExecuteResult, error) {
	if skill == nil {
		return nil, fmt.Errorf("skill is required")
	}
	if req == nil {
		req = types.NewRequest("")
	}

	result := &ExecuteResult{
		SkillName: skill.Name,
		Skill:     skill.Name,
	}
	resolvedSkill, err := e.resolveExecutableSkill(skill)
	if err != nil {
		result.setError(err)
		return result, nil
	}
	if resolvedSkill != nil {
		skill = resolvedSkill
		result.SkillName = skill.Name
		result.Skill = skill.Name
	}
	if err := e.checkPermissions(skill, req); err != nil {
		result.setError(err)
		return result, nil
	}

	// 1. 如果有自定义处理器，直接执行
	if skill.Handler != nil {
		typedResult, err := skill.Handler.Execute(ctx, req)
		if err != nil {
			result.setError(err)
			return result, nil
		}
		result.Success = typedResult.Success
		result.Output = typedResult.Output
		result.Observations = typedResult.Observations
		result.Usage = typedResult.Usage
		return result, nil
	}

	// 2. 如果有工作流，执行工作流
	if skill.HasWorkflow() {
		obs, output, err := e.executeWorkflow(ctx, skill, req)
		result.Observations = obs
		if err != nil {
			result.setError(err)
			return result, nil
		}
		result.Success = true
		result.Output = output
		return result, nil
	}

	// 3. 默认: 直接调用工具
	return e.executeDefault(ctx, skill, req)
}

func (e *Executor) resolveExecutableSkill(skill *Skill) (*Skill, error) {
	if e != nil && e.registry != nil {
		return e.registry.Hydrate(skill)
	}
	return HydrateSkill(skill)
}

func (r *ExecuteResult) setError(err error) {
	if r == nil || err == nil {
		return
	}
	r.Error = err.Error()

	var runtimeErr *runtimeerrors.RuntimeError
	if !stderrors.As(err, &runtimeErr) {
		return
	}
	r.ErrorCode = string(runtimeErr.Code)
	if ctx := runtimeErr.GetContext(); len(ctx) > 0 {
		r.ErrorContext = ctx
	}
}

// executeWorkflow 执行工作流
func (e *Executor) executeWorkflow(ctx context.Context, skill *Skill, req *types.Request) ([]types.Observation, string, error) {
	parallelDAG, err := e.buildExecutionDAG(skill.Workflow)
	if err != nil {
		return nil, "", fmt.Errorf("invalid workflow: %w", err)
	}

	parallelExecutor := runtimeexecutor.NewParallelExecutor(e.workflowConcurrency(skill.Workflow),
		func(execCtx context.Context, nodeID, tool string, args map[string]interface{}, nodeCtx *runtimeexecutor.NodeExecutorContext) (*types.Observation, error) {
			step := e.findStep(skill.Workflow, nodeID)
			if step == nil {
				return nil, fmt.Errorf("workflow step not found: %s", nodeID)
			}

			results := e.buildResultsFromObservations(nodeCtx)
			preparedArgs := e.prepareArgs(step.Args, results, req)

			observation := types.NewObservation(step.ID, step.Tool)
			observation.WithInput(preparedArgs)

			toolInfo, findErr := e.mcpManager.FindTool(step.Tool)
			if findErr != nil {
				observation.MarkFailure(fmt.Sprintf("tool not found: %s", step.Tool))
				return observation, findErr
			}
			observation.WithMetric("mcp_name", toolInfo.MCPName)
			if toolInfo.MCPTrustLevel != "" {
				observation.WithMetric("mcp_trust_level", toolInfo.MCPTrustLevel)
			}
			if toolInfo.ExecutionMode != "" {
				observation.WithMetric("execution_mode", toolInfo.ExecutionMode)
			}

			output, callErr := e.mcpManager.CallTool(execCtx, toolInfo.MCPName, step.Tool, preparedArgs)
			observation.WithOutput(output)
			if callErr != nil {
				observation.MarkFailure(callErr.Error())
				return observation, callErr
			}

			observation.MarkSuccess()
			return observation, nil
		},
	)

	parallelObservations, execErr := parallelExecutor.ExecuteParallel(ctx, parallelDAG)
	observations := make([]types.Observation, 0, len(parallelObservations))
	results := make(map[string]interface{}, len(parallelObservations))
	for _, observation := range parallelObservations {
		if observation == nil {
			continue
		}
		observations = append(observations, *observation)
		results[observation.Step] = observation.Output
	}

	output := e.formatOutput(results)
	if execErr != nil {
		return observations, output, execErr
	}
	return observations, output, nil
}

func (e *Executor) buildExecutionDAG(workflow *Workflow) (*runtimeexecutor.DAG, error) {
	if workflow == nil {
		return nil, fmt.Errorf("workflow cannot be nil")
	}

	dag := &runtimeexecutor.DAG{Nodes: make(map[string]*runtimeexecutor.DAGNode, len(workflow.Steps))}
	for _, step := range workflow.Steps {
		dag.Nodes[step.ID] = &runtimeexecutor.DAGNode{
			ID:     step.ID,
			Tool:   step.Tool,
			Args:   step.Args,
			Deps:   append([]string(nil), step.DependsOn...),
			Status: runtimeexecutor.StatusPending,
		}
	}

	for _, step := range workflow.Steps {
		for _, dep := range step.DependsOn {
			if _, exists := dag.Nodes[dep]; !exists {
				return nil, fmt.Errorf("workflow step %s depends on non-existent step %s", step.ID, dep)
			}
		}
	}

	return dag, nil
}

func (e *Executor) buildResultsFromObservations(execCtx *runtimeexecutor.NodeExecutorContext) map[string]interface{} {
	results := make(map[string]interface{})
	if execCtx == nil {
		return results
	}

	for nodeID, observation := range execCtx.NodeObservations {
		if observation != nil {
			results[nodeID] = observation.Output
		}
	}
	return results
}

func (e *Executor) workflowConcurrency(workflow *Workflow) int {
	if workflow == nil || len(workflow.Steps) <= 1 {
		return 1
	}
	if len(workflow.Steps) < 4 {
		return len(workflow.Steps)
	}
	return 4
}

// executeDefault 默认执行模式（使用 LLM）
func (e *Executor) executeDefault(ctx context.Context, skill *Skill, req *types.Request) (*ExecuteResult, error) {
	// 如果没有 LLM Runtime，返回提示信息
	if e.llmRuntime == nil {
		return &ExecuteResult{
			SkillName: skill.Name,
			Success:   false,
			Output:    "LLM Runtime not configured",
		}, nil
	}

	systemPrompt, userPrompt, err := resolveSkillPrompts(skill)
	if err != nil {
		return &ExecuteResult{
			SkillName: skill.Name,
			Skill:     skill.Name,
			Success:   false,
			Error:     err.Error(),
		}, nil
	}

	// 构建消息列表
	messages := []types.Message{}

	// 添加系统提示词
	if systemPrompt != "" {
		messages = append(messages, *types.NewSystemMessage(systemPrompt))
	}
	// 附加精简版上下文摘要（避免把完整 context pack 直接塞进 prompt）
	if ctxSummary := buildContextSummary(req); ctxSummary != "" {
		messages = append(messages, *types.NewSystemMessage("Runtime context summary:\n" + ctxSummary))
	}

	// 添加历史消息
	if len(req.History) > 0 {
		messages = append(messages, req.History...)
	}

	// 添加用户消息
	if userPrompt == "" {
		userPrompt = req.Prompt
	}
	messages = append(messages, *types.NewUserMessage(userPrompt))

	// 构建工具定义
	var tools []types.ToolDefinition
	if len(skill.Tools) > 0 {
		tools = e.buildToolDefinitions(ctx, skill.Tools)
	}

	// 调用 LLM
	llmRequest := &llm.LLMRequest{
		Model:           "",
		Messages:        messages,
		Tools:           tools,
		MaxTokens:       4096,
		Temperature:     0.7,
		ReasoningEffort: req.ReasoningEffort,
		Thinking:        types.CloneThinkingConfig(req.Thinking),
	}

	response, err := e.llmRuntime.Call(ctx, llmRequest)
	if err != nil {
		if fallbackMessages, ok := buildSystemRoleFallbackMessages(messages, err); ok {
			llmRequest.Messages = fallbackMessages
			response, err = e.llmRuntime.Call(ctx, llmRequest)
		}
	}
	if err != nil {
		return &ExecuteResult{
			SkillName: skill.Name,
			Success:   false,
			Output:    "",
			Error:     err.Error(),
		}, err
	}

	return &ExecuteResult{
		SkillName:    skill.Name,
		Success:      true,
		Output:       response.Content,
		Usage:        response.Usage,
		Observations: []types.Observation{},
	}, nil
}

const contextSummaryMaxBytes = 4096

func buildContextSummary(req *types.Request) string {
	if req == nil || len(req.Context) == 0 {
		return ""
	}

	summary := map[string]interface{}{}

	if workspacePath, ok := req.Context["workspace_path"].(string); ok && workspacePath != "" {
		summary["workspace_path"] = workspacePath
	}

	profileLayer := false
	if pack, ok := req.Context["context_pack"].(map[string]interface{}); ok {
		if reduced := shrinkContextPack(pack); len(reduced) > 0 {
			summary["context_pack"] = reduced
		}
		_, profileLayer = pack["profile"].(map[string]interface{})
	}

	// 仅保留简单标量，避免把大对象塞进 prompt
	for key, value := range req.Context {
		if key == "context_pack" || key == "workspace_path" {
			continue
		}
		if profileLayer && strings.HasPrefix(key, "profile_") {
			continue
		}
		if isScalarValue(value) {
			summary[key] = value
		}
	}

	if len(summary) == 0 {
		return ""
	}

	raw, err := json.Marshal(summary)
	if err != nil {
		return ""
	}
	if len(raw) > contextSummaryMaxBytes {
		raw = append(raw[:contextSummaryMaxBytes], []byte("...")...)
	}
	return string(raw)
}

func shrinkContextPack(pack map[string]interface{}) map[string]interface{} {
	if len(pack) == 0 {
		return nil
	}

	reduced := map[string]interface{}{}

	if profile, ok := pack["profile"].(map[string]interface{}); ok {
		profileSummary := map[string]interface{}{}
		copyString(profileSummary, "reference", profile["reference"])
		copyString(profileSummary, "name", profile["name"])
		copyString(profileSummary, "agent", profile["agent"])
		copyString(profileSummary, "root", profile["root"])
		copyString(profileSummary, "memory_path", profile["memory_path"])
		copyString(profileSummary, "notes_path", profile["notes_path"])
		if resources, ok := profile["resources"].(map[string]interface{}); ok {
			if reducedResources := shrinkProfileResources(resources); len(reducedResources) > 0 {
				profileSummary["resources"] = reducedResources
			}
		}
		if len(profileSummary) > 0 {
			reduced["profile"] = profileSummary
		}
	}

	if workspace, ok := pack["workspace"].(map[string]interface{}); ok {
		wsSummary := map[string]interface{}{}
		if v, ok := workspace["summary"].(string); ok && strings.TrimSpace(v) != "" {
			wsSummary["summary"] = v
		}
		if v, ok := workspace["query"].(string); ok && strings.TrimSpace(v) != "" {
			wsSummary["query"] = v
		}
		if files := toStringSlice(workspace["files"]); len(files) > 0 {
			wsSummary["files"] = limitStringSlice(files, 5)
		}
		if len(wsSummary) > 0 {
			reduced["workspace"] = wsSummary
		}
	}

	if session, ok := pack["session"].(map[string]interface{}); ok {
		summary := map[string]interface{}{}
		copyString(summary, "id", session["id"])
		copyString(summary, "user_id", session["user_id"])
		copyString(summary, "state", session["state"])
		copyString(summary, "last_agent", session["last_agent"])
		copyString(summary, "last_skill", session["last_skill"])
		copyString(summary, "last_model", session["last_model"])
		if tags := toStringSlice(session["tags"]); len(tags) > 0 {
			summary["tags"] = limitStringSlice(tags, 5)
		}
		if totalTurns, ok := toInt(session["total_turns"]); ok {
			summary["total_turns"] = totalTurns
		}
		if len(summary) > 0 {
			reduced["session"] = summary
		}
	}

	if team, ok := pack["team"].(map[string]interface{}); ok {
		summary := map[string]interface{}{}
		copyString(summary, "team_id", team["team_id"])
		copyString(summary, "task_id", team["task_id"])
		if value, ok := team["summary"].(string); ok && strings.TrimSpace(value) != "" {
			summary["summary"] = summarizeContextString(value, 300)
		}
		if taskCount, ok := toInt(team["task_count"]); ok {
			summary["task_count"] = taskCount
		}
		if mailCount, ok := toInt(team["mail_count"]); ok {
			summary["mail_count"] = mailCount
		}
		if len(summary) > 0 {
			reduced["team"] = summary
		}
	}

	if warnings, ok := pack["_warnings"]; ok {
		reduced["warnings"] = warnings
	}

	return reduced
}

func shrinkProfileResources(resources map[string]interface{}) map[string]interface{} {
	if len(resources) == 0 {
		return nil
	}

	reduced := map[string]interface{}{}
	for key, raw := range resources {
		item, ok := raw.(map[string]interface{})
		if !ok || len(item) == 0 {
			continue
		}
		summary := map[string]interface{}{}
		copyString(summary, "path", item["path"])
		copyString(summary, "format", item["format"])
		if content, ok := item["content"].(string); ok && strings.TrimSpace(content) != "" {
			summary["content"] = summarizeContextString(content, 300)
		}
		if truncated, ok := item["truncated"].(bool); ok && truncated {
			summary["truncated"] = true
		}
		if len(summary) > 0 {
			reduced[key] = summary
		}
	}

	if len(reduced) == 0 {
		return nil
	}
	return reduced
}

func summarizeContextString(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" || limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func isScalarValue(value interface{}) bool {
	switch value.(type) {
	case string, bool, int, int32, int64, float32, float64, uint, uint32, uint64:
		return true
	default:
		return false
	}
}

func toStringSlice(value interface{}) []string {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return typed
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func limitStringSlice(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	return values[:limit]
}

func copyString(target map[string]interface{}, key string, value interface{}) {
	if target == nil {
		return
	}
	if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
		target[key] = s
	}
}

func toInt(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

// buildToolDefinitions 构建工具定义
func (e *Executor) buildToolDefinitions(ctx context.Context, toolNames []string) []types.ToolDefinition {
	tools := make([]types.ToolDefinition, 0, len(toolNames))

	for _, name := range toolNames {
		if e.mcpManager == nil {
			continue
		}

		toolInfo, err := e.mcpManager.FindTool(name)
		if err != nil {
			continue
		}

		tool := types.ToolDefinition{
			Name:        toolInfo.Name,
			Description: toolInfo.Description,
			Parameters:  normalizeToolParameters(toolInfo.InputSchema),
		}

		tools = append(tools, tool)
	}

	return tools
}

func normalizeToolParameters(schema map[string]interface{}) map[string]interface{} {
	if len(schema) == 0 {
		return map[string]interface{}{
			"type": "object",
		}
	}

	normalized := make(map[string]interface{}, len(schema)+1)
	for key, value := range schema {
		normalized[key] = value
	}
	if _, ok := normalized["type"]; !ok {
		normalized["type"] = "object"
	}
	return normalized
}

func buildSystemRoleFallbackMessages(messages []types.Message, err error) ([]types.Message, bool) {
	if !shouldRetryWithoutSystemRole(err) {
		return nil, false
	}

	var systemParts []string
	filtered := make([]types.Message, 0, len(messages))

	for _, msg := range messages {
		if msg.Role == "system" {
			if content := strings.TrimSpace(msg.Content); content != "" {
				systemParts = append(systemParts, content)
			}
			continue
		}

		clone := msg
		filtered = append(filtered, clone)
	}

	if len(systemParts) == 0 {
		return nil, false
	}

	if len(filtered) == 0 {
		return []types.Message{*types.NewUserMessage(mergeSystemIntoUserPrompt(systemParts, ""))}, true
	}

	if filtered[0].Role == "user" {
		filtered[0].Content = mergeSystemIntoUserPrompt(systemParts, filtered[0].Content)
		return filtered, true
	}

	filtered = append([]types.Message{*types.NewUserMessage(mergeSystemIntoUserPrompt(systemParts, ""))}, filtered...)
	return filtered, true
}

func shouldRetryWithoutSystemRole(err error) bool {
	if err == nil {
		return false
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unknown variant  system") ||
		strings.Contains(message, "unknown variant system") ||
		strings.Contains(message, "invalid value: 'system'") ||
		strings.Contains(message, "invalid role: system") ||
		strings.Contains(message, "unsupported value: \"system\"") ||
		strings.Contains(message, "unsupported value: 'system'") ||
		strings.Contains(message, "unsupported role: system") ||
		(strings.Contains(message, "system role") && containsUnsupportedMarker(message)) ||
		(strings.Contains(message, "system message") && containsUnsupportedMarker(message)) ||
		systemRoleExpectedRolePattern.MatchString(message)
}

func containsUnsupportedMarker(message string) bool {
	return strings.Contains(message, "unsupported") ||
		strings.Contains(message, "not supported") ||
		strings.Contains(message, "not allowed") ||
		strings.Contains(message, "invalid")
}

func mergeSystemIntoUserPrompt(systemParts []string, userContent string) string {
	systemText := strings.TrimSpace(strings.Join(systemParts, "\n\n"))
	userText := strings.TrimSpace(userContent)

	switch {
	case systemText == "":
		return userText
	case userText == "":
		return "System instructions:\n" + systemText
	default:
		return fmt.Sprintf("System instructions:\n%s\n\nUser request:\n%s", systemText, userText)
	}
}

// buildDAG 构建依赖图
func (e *Executor) buildDAG(workflow *Workflow) *DAG {
	dag := &DAG{
		Nodes: make(map[string]*Node),
	}

	for _, step := range workflow.Steps {
		dag.Nodes[step.ID] = &Node{
			ID:         step.ID,
			Data:       step,
			Deps:       step.DependsOn,
			Dependents: []string{},
		}
	}

	// 建立反向依赖
	for _, step := range workflow.Steps {
		for _, dep := range step.DependsOn {
			if node, ok := dag.Nodes[dep]; ok {
				node.Dependents = append(node.Dependents, step.ID)
			}
		}
	}

	return dag
}

// findStep 查找步骤
func (e *Executor) findStep(workflow *Workflow, stepID string) *WorkflowStep {
	for _, step := range workflow.Steps {
		if step.ID == stepID {
			return &step
		}
	}
	return nil
}

// checkDependencies 检查依赖是否满足
func (e *Executor) checkDependencies(step *WorkflowStep, results map[string]interface{}) bool {
	if len(step.DependsOn) == 0 {
		return true
	}

	for _, dep := range step.DependsOn {
		if _, ok := results[dep]; !ok {
			return false
		}
	}

	return true
}

// prepareArgs 准备参数
func (e *Executor) prepareArgs(args map[string]interface{}, results map[string]interface{}, req *types.Request) map[string]interface{} {
	prepared := make(map[string]interface{}, len(args))
	contextData := buildWorkflowTemplateContext(results, req)

	for key, value := range args {
		prepared[key] = renderWorkflowTemplateValue(value, contextData)
	}

	// 添加请求上下文
	if req != nil {
		prepared["prompt"] = req.Prompt
		if req.Context != nil {
			for k, v := range req.Context {
				// 避免覆盖已有参数
				if _, exists := prepared[k]; !exists {
					prepared[k] = v
				}
			}
		}
	}

	return prepared
}

func buildWorkflowTemplateContext(results map[string]interface{}, req *types.Request) map[string]interface{} {
	contextData := map[string]interface{}{
		"results": results,
	}

	if req == nil {
		return contextData
	}

	contextData["prompt"] = req.Prompt
	contextData["context"] = req.Context
	contextData["options"] = req.Options
	contextData["metadata"] = req.Metadata

	return contextData
}

func renderWorkflowTemplateValue(value interface{}, contextData map[string]interface{}) interface{} {
	switch typed := value.(type) {
	case string:
		return renderWorkflowTemplateString(typed, contextData)
	case map[string]interface{}:
		rendered := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			rendered[key] = renderWorkflowTemplateValue(item, contextData)
		}
		return rendered
	case []interface{}:
		rendered := make([]interface{}, len(typed))
		for i, item := range typed {
			rendered[i] = renderWorkflowTemplateValue(item, contextData)
		}
		return rendered
	default:
		return value
	}
}

func renderWorkflowTemplateString(template string, contextData map[string]interface{}) interface{} {
	matches := workflowTemplatePattern.FindAllStringSubmatch(template, -1)
	if len(matches) == 0 {
		return template
	}

	trimmed := strings.TrimSpace(template)
	if len(matches) == 1 && matches[0][0] == trimmed {
		if resolved, ok := resolveWorkflowTemplatePath(matches[0][1], contextData); ok {
			return resolved
		}
		return template
	}

	rendered := workflowTemplatePattern.ReplaceAllStringFunc(template, func(match string) string {
		submatches := workflowTemplatePattern.FindStringSubmatch(match)
		if len(submatches) != 2 {
			return match
		}
		if resolved, ok := resolveWorkflowTemplatePath(submatches[1], contextData); ok {
			return fmt.Sprint(resolved)
		}
		return match
	})

	return rendered
}

func resolveWorkflowTemplatePath(path string, contextData map[string]interface{}) (interface{}, bool) {
	if path == "" {
		return nil, false
	}

	parts := strings.Split(path, ".")
	var current interface{} = contextData

	for _, part := range parts {
		switch typed := current.(type) {
		case map[string]interface{}:
			next, ok := typed[part]
			if !ok {
				return nil, false
			}
			current = next
		case types.Metadata:
			next, ok := typed.Get(part)
			if !ok {
				return nil, false
			}
			current = next
		case []interface{}:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}

	return current, true
}

// formatOutput 格式化输出
func (e *Executor) formatOutput(results map[string]interface{}) string {
	if len(results) == 0 {
		return "No results"
	}
	if len(results) == 1 {
		for _, result := range results {
			return fmt.Sprint(result)
		}
	}

	var output string
	for id, result := range results {
		if output != "" {
			output += "\n"
		}
		output += fmt.Sprintf("[%s]: %v", id, result)
	}

	return output
}

func (e *Executor) checkPermissions(skill *Skill, req *types.Request) error {
	if skill == nil || len(skill.Permissions) == 0 {
		return nil
	}

	granted := collectGrantedPermissions(req)
	if _, ok := granted["*"]; ok {
		return nil
	}

	missing := make([]string, 0, len(skill.Permissions))
	for _, permission := range skill.Permissions {
		permission = normalizePermission(permission)
		if permission == "" {
			continue
		}
		if _, ok := granted[permission]; ok {
			continue
		}
		missing = append(missing, permission)
	}

	if len(missing) == 0 {
		return nil
	}

	return runtimeerrors.WrapWithContext(
		runtimeerrors.ErrAgentPermission,
		fmt.Sprintf("skill %q requires permissions: %s", skill.Name, strings.Join(missing, ", ")),
		nil,
		map[string]interface{}{
			"skill":                skill.Name,
			"required_permissions": append([]string(nil), skill.Permissions...),
			"missing_permissions":  missing,
		},
	)
}

func collectGrantedPermissions(req *types.Request) map[string]struct{} {
	granted := make(map[string]struct{})
	if req == nil {
		return granted
	}

	appendPermissions(granted, req.Context["permissions"])
	appendPermissions(granted, req.Context["granted_permissions"])
	appendPermissions(granted, req.Options["permissions"])
	appendPermissions(granted, req.Options["granted_permissions"])
	if req.Metadata != nil {
		if value, ok := req.Metadata.Get("permissions"); ok {
			appendPermissions(granted, value)
		}
		if value, ok := req.Metadata.Get("granted_permissions"); ok {
			appendPermissions(granted, value)
		}
	}

	return granted
}

func appendPermissions(target map[string]struct{}, value interface{}) {
	switch typed := value.(type) {
	case nil:
		return
	case string:
		for _, item := range splitPermissionString(typed) {
			target[item] = struct{}{}
		}
	case []string:
		for _, item := range typed {
			if normalized := normalizePermission(item); normalized != "" {
				target[normalized] = struct{}{}
			}
		}
	case []interface{}:
		for _, item := range typed {
			appendPermissions(target, item)
		}
	case map[string]interface{}:
		for key, raw := range typed {
			allowed, ok := raw.(bool)
			if ok && allowed {
				if normalized := normalizePermission(key); normalized != "" {
					target[normalized] = struct{}{}
				}
			}
		}
	case map[string]bool:
		for key, allowed := range typed {
			if allowed {
				if normalized := normalizePermission(key); normalized != "" {
					target[normalized] = struct{}{}
				}
			}
		}
	}
}

func splitPermissionString(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\t'
	})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if normalized := normalizePermission(part); normalized != "" {
			result = append(result, normalized)
		}
	}
	return result
}

func normalizePermission(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
