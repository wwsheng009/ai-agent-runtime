package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ai-gateway/ai-agent-runtime/internal/errors"
	"github.com/ai-gateway/ai-agent-runtime/internal/llm"
	"github.com/ai-gateway/ai-agent-runtime/internal/skill"
	"github.com/ai-gateway/ai-agent-runtime/internal/types"
)

// Planner 计划器
type Planner struct {
	mcpManager      skill.MCPManager
	llmRuntime      *llm.LLMRuntime
	dagBuilder      *skill.DAGBuilder
	provider        string
	model           string
	reasoningEffort string
	thinking        *types.ThinkingConfig
}

// NewPlanner 创建计划器
func NewPlanner(mcpManager skill.MCPManager) *Planner {
	return &Planner{
		mcpManager: mcpManager,
		dagBuilder: skill.NewDAGBuilder(),
		provider:   "",
		model:      "",
	}
}

// NewPlannerWithLLM 创建带 LLM 的计划器
func NewPlannerWithLLM(mcpManager skill.MCPManager, llmRuntime *llm.LLMRuntime) *Planner {
	return &Planner{
		mcpManager: mcpManager,
		llmRuntime: llmRuntime,
		dagBuilder: skill.NewDAGBuilder(),
		provider:   "",
		model:      "",
	}
}

// Plan 执行计划
type Plan struct {
	Goal  string     `json:"goal" yaml:"goal"`
	Steps []PlanStep `json:"steps" yaml:"steps"`
}

// PlanStep 计划步骤
type PlanStep struct {
	ID          string                 `json:"id" yaml:"id"`
	Description string                 `json:"description" yaml:"description"`
	Tool        string                 `json:"tool" yaml:"tool"`
	Args        map[string]interface{} `json:"args" yaml:"args"`
	DependsOn   []string               `json:"dependsOn" yaml:"dependsOn"`
	Priority    int                    `json:"priority" yaml:"priority"`
}

// CreatePlanWithLLM 使用 LLM 创建计划
func (p *Planner) CreatePlanWithLLM(ctx context.Context, goal string, availableTools []skill.ToolInfo) (*Plan, error) {
	if goal == "" {
		return nil, errors.New(errors.ErrValidationFailed, "goal is required")
	}

	if p.llmRuntime == nil {
		return nil, errors.New(errors.ErrValidationFailed, "LLM runtime not configured")
	}

	// 构建 tool 描述
	toolDescriptions := p.buildToolDescriptions(availableTools)

	// 构建 Prompt
	systemPrompt := p.buildPlanningPrompt(goal, toolDescriptions)

	// 创建消息
	messages := []types.Message{
		{
			Role:     "system",
			Content:  systemPrompt,
			Metadata: types.NewMetadata(),
		},
		{
			Role:     "user",
			Content:  goal,
			Metadata: types.NewMetadata(),
		},
	}

	// 调用 LLM 生成计划
	llmRequest := &llm.LLMRequest{
		Provider:        p.provider,
		Model:           p.model,
		Messages:        messages,
		MaxTokens:       4096,
		Temperature:     0.3, // 较低的温度以获得更确定性的输出
		ReasoningEffort: p.reasoningEffort,
		Thinking:        types.CloneThinkingConfig(p.thinking),
	}

	response, err := p.llmRuntime.Call(ctx, llmRequest)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	// 解析 LLM 响应为 JSON
	return p.parseLLMResponseToPlan(response, availableTools)
}

// CreatePlan 创建计划（保持向后兼容，不使用 LLM）
func (p *Planner) CreatePlan(ctx context.Context, goal string, availableTools []skill.ToolInfo) (*Plan, error) {
	if goal == "" {
		return nil, errors.New(errors.ErrValidationFailed, "goal is required")
	}

	if len(availableTools) == 0 {
		return nil, errors.New(errors.ErrValidationFailed, "no tools available for planning")
	}

	if p.llmRuntime != nil {
		if llmPlan, err := p.CreatePlanWithLLM(ctx, goal, availableTools); err == nil {
			return llmPlan, nil
		}
	}

	return p.createHeuristicPlan(goal, availableTools), nil
}

// CreatePlanFromWorkflow 从 Workflow 创建计划
func (p *Planner) CreatePlanFromWorkflow(workflow *skill.Workflow) (*Plan, error) {
	if workflow == nil || len(workflow.Steps) == 0 {
		return nil, errors.New(errors.ErrValidationFailed, "workflow is empty")
	}

	steps := make([]PlanStep, len(workflow.Steps))
	for i, ws := range workflow.Steps {
		steps[i] = PlanStep{
			ID:          ws.ID,
			Description: ws.Name,
			Tool:        ws.Tool,
			Args:        ws.Args,
			DependsOn:   ws.DependsOn,
			Priority:    i + 1,
		}
	}

	return &Plan{
		Goal:  "Execute workflow",
		Steps: steps,
	}, nil
}

// ValidatePlan 验证计划
func (p *Plan) ValidatePlan(availableTools []string) error {
	if len(p.Steps) == 0 {
		return errors.New(errors.ErrValidationFailed, "plan has no steps")
	}

	allowedTools := make(map[string]bool, len(availableTools))
	for _, tool := range availableTools {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			continue
		}
		allowedTools[tool] = true
	}
	enforceToolCheck := len(allowedTools) > 0

	// 检查步骤依赖的合法性
	stepMap := make(map[string]bool)
	for _, step := range p.Steps {
		stepID := strings.TrimSpace(step.ID)
		if stepID == "" {
			return errors.New(errors.ErrValidationFailed, "step id is required")
		}
		if stepMap[stepID] {
			return errors.New(errors.ErrValidationFailed, "duplicate step id")
		}
		if strings.TrimSpace(step.Tool) == "" {
			return errors.New(errors.ErrValidationFailed, "step tool is required")
		}
		if enforceToolCheck && !allowedTools[step.Tool] {
			return errors.New(errors.ErrValidationFailed, "step uses unavailable tool")
		}
		stepMap[stepID] = true
	}

	for _, step := range p.Steps {
		for _, dep := range step.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == strings.TrimSpace(step.ID) {
				return errors.New(errors.ErrValidationFailed, "step cannot depend on itself")
			}
			if !stepMap[dep] {
				return errors.New(errors.ErrValidationFailed,
					"invalid dependency: step not found")
			}
		}
	}

	// 检查是否有循环依赖
	if hasCycle(p.Steps) {
		return errors.New(errors.ErrValidationFailed, "plan has cyclic dependencies")
	}

	return nil
}

// ToDAG 将计划转换为 DAG
func (p *Plan) ToDAG() (*skill.DAG, error) {
	dag := &skill.DAG{
		Nodes: make(map[string]*skill.Node),
	}

	// 创建节点
	for _, step := range p.Steps {
		dag.Nodes[step.ID] = &skill.Node{
			ID:   step.ID,
			Data: step,
			Deps: step.DependsOn,
		}
	}

	// 建立反向依赖
	for _, step := range p.Steps {
		if node, ok := dag.Nodes[step.ID]; ok {
			node.Dependents = make([]string, 0)
		}

		for _, dep := range step.DependsOn {
			if depNode, ok := dag.Nodes[dep]; ok {
				depNode.Dependents = append(depNode.Dependents, step.ID)
			}
		}
	}

	// 验证 DAG
	if _, err := dag.TopologicalSort(); err != nil {
		return nil, errors.Wrap(errors.ErrWorkflowCycle, "workflow has cycle", err)
	}

	return dag, nil
}

// GetExecutableSteps 获取可执行的步骤（无依赖）
func (p *Plan) GetExecutableSteps(completedSteps map[string]bool) []PlanStep {
	var executable []PlanStep

	for _, step := range p.Steps {
		if completedSteps[step.ID] {
			continue
		}

		// 检查所有依赖是否完成
		allDepsCompleted := true
		for _, dep := range step.DependsOn {
			if !completedSteps[dep] {
				allDepsCompleted = false
				break
			}
		}

		if allDepsCompleted {
			executable = append(executable, step)
		}
	}

	return executable
}

// buildPlanningPrompt 构建计划提示词
func (p *Planner) buildPlanningPrompt(goal string, tools []skill.ToolInfo) string {
	toolDescriptions := make([]string, len(tools))
	for i, t := range tools {
		toolDescriptions[i] = fmt.Sprintf("- %s: %s", t.Name, t.Description)
	}

	return fmt.Sprintf(`
You are an AI planning assistant. Given a user request and available tools,
create an execution plan in JSON format.

Goal: %s

Available tools:
%s

Respond with a JSON plan:
{
  "goal": "description of the goal",
  "steps": [
    {
      "id": "step1",
      "description": "what this step does",
      "tool": "tool_name",
      "args": {"arg1": "value1"},
      "dependsOn": [],
      "priority": 1
    }
  ]
}

Rules:
1. Each step should be atomic and achievable with one tool call
2. Use dependsOn to specify dependencies between steps
3. Steps with no dependencies can be executed in parallel
4. Set appropriate priority (higher = more important)
`, goal, strings.Join(toolDescriptions, "\n"))
}

// ToJSON 将计划序列化为 JSON
func (p *Plan) ToJSON() ([]byte, error) {
	return json.MarshalIndent(p, "", "  ")
}

// FromJSON 从 JSON 反序列化计划
func (p *Plan) FromJSON(data []byte) error {
	return json.Unmarshal(data, p)
}

// Clone 克隆计划
func (p *Plan) Clone() *Plan {
	if p == nil {
		return nil
	}

	clone := &Plan{
		Goal:  p.Goal,
		Steps: make([]PlanStep, len(p.Steps)),
	}

	for i, step := range p.Steps {
		clone.Steps[i] = PlanStep{
			ID:          step.ID,
			Description: step.Description,
			Tool:        step.Tool,
			Args:        copyMap(step.Args),
			DependsOn:   append([]string{}, step.DependsOn...),
			Priority:    step.Priority,
		}
	}

	return clone
}

// copyMap 辅助函数：复制 map
func copyMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}

	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// hasCycle 检查是否有循环依赖
func hasCycle(steps []PlanStep) bool {
	visited := make(map[string]bool)
	visiting := make(map[string]bool)

	var hasCycleDFS func(stepID string) bool
	hasCycleDFS = func(stepID string) bool {
		visiting[stepID] = true

		for _, step := range steps {
			if step.ID != stepID {
				continue
			}

			for _, dep := range step.DependsOn {
				if visiting[dep] {
					return true // 发现环
				}
				if !visited[dep] {
					if hasCycleDFS(dep) {
						return true
					}
				}
			}
		}

		visiting[stepID] = false
		visited[stepID] = true
		return false
	}

	for _, step := range steps {
		if !visited[step.ID] {
			if hasCycleDFS(step.ID) {
				return true
			}
		}
	}

	return false
}

func (p *Planner) createHeuristicPlan(goal string, availableTools []skill.ToolInfo) *Plan {
	selected := selectHeuristicTools(goal, availableTools, 3)
	steps := make([]PlanStep, 0, len(selected))
	for idx, tool := range selected {
		stepID := fmt.Sprintf("step_%d", idx+1)
		step := PlanStep{
			ID:          stepID,
			Description: heuristicStepDescription(idx, tool.Name, goal),
			Tool:        tool.Name,
			Args:        heuristicStepArgs(goal, tool),
			Priority:    len(selected) - idx,
		}
		if idx > 0 {
			step.DependsOn = []string{steps[idx-1].ID}
		}
		steps = append(steps, step)
	}
	return &Plan{
		Goal:  goal,
		Steps: steps,
	}
}

type scoredTool struct {
	tool  skill.ToolInfo
	score int
	index int
}

func selectHeuristicTools(goal string, availableTools []skill.ToolInfo, maxSteps int) []skill.ToolInfo {
	if len(availableTools) == 0 {
		return nil
	}
	if maxSteps <= 0 {
		maxSteps = 1
	}
	if maxSteps > len(availableTools) {
		maxSteps = len(availableTools)
	}

	tokens := tokenizePlanningGoal(goal)
	scored := make([]scoredTool, 0, len(availableTools))
	for idx, tool := range availableTools {
		scored = append(scored, scoredTool{
			tool:  tool,
			score: heuristicToolScore(tool, tokens),
			index: idx,
		})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].index < scored[j].index
		}
		return scored[i].score > scored[j].score
	})

	selected := make([]skill.ToolInfo, 0, maxSteps)
	for _, item := range scored {
		if len(selected) >= maxSteps {
			break
		}
		if item.score <= 0 && len(selected) > 0 {
			continue
		}
		selected = append(selected, item.tool)
	}
	if len(selected) == 0 {
		selected = append(selected, availableTools[0])
	}
	return selected
}

func tokenizePlanningGoal(goal string) []string {
	raw := strings.FieldsFunc(strings.ToLower(goal), func(r rune) bool {
		switch r {
		case ' ', '\n', '\t', ',', '.', ':', ';', '/', '\\', '-', '_', '(', ')', '[', ']':
			return true
		default:
			return false
		}
	})
	tokens := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, token := range raw {
		if len(token) < 2 || seen[token] {
			continue
		}
		seen[token] = true
		tokens = append(tokens, token)
	}
	return tokens
}

func heuristicToolScore(tool skill.ToolInfo, goalTokens []string) int {
	name := strings.ToLower(strings.TrimSpace(tool.Name))
	desc := strings.ToLower(strings.TrimSpace(tool.Description))
	score := 0
	for _, token := range goalTokens {
		if strings.Contains(name, token) {
			score += 6
		}
		if strings.Contains(desc, token) {
			score += 3
		}
	}

	switch {
	case strings.Contains(name, "read"),
		strings.Contains(name, "search"),
		strings.Contains(name, "grep"),
		strings.Contains(name, "fetch"),
		strings.Contains(name, "list"):
		score += 4
	case strings.Contains(name, "analy"),
		strings.Contains(name, "summary"),
		strings.Contains(name, "inspect"),
		strings.Contains(name, "query"):
		score += 3
	case strings.Contains(name, "write"),
		strings.Contains(name, "edit"),
		strings.Contains(name, "patch"),
		strings.Contains(name, "apply"):
		score += 2
	case strings.Contains(name, "test"),
		strings.Contains(name, "verify"),
		strings.Contains(name, "lint"),
		strings.Contains(name, "build"):
		score += 2
	}

	if tool.Enabled {
		score++
	}
	return score
}

func heuristicStepDescription(stepIndex int, toolName, goal string) string {
	if stepIndex == 0 {
		return fmt.Sprintf("Use %s to gather key facts for goal: %s", toolName, strings.TrimSpace(goal))
	}
	return fmt.Sprintf("Use %s to advance the goal: %s", toolName, strings.TrimSpace(goal))
}

func heuristicStepArgs(goal string, tool skill.ToolInfo) map[string]interface{} {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		goal = "complete the requested task"
	}
	if key := preferredPlanningArgKey(tool.InputSchema); key != "" {
		return map[string]interface{}{
			key: goal,
		}
	}
	return map[string]interface{}{
		"goal": goal,
	}
}

func preferredPlanningArgKey(schema map[string]interface{}) string {
	if len(schema) == 0 {
		return ""
	}
	props, ok := schema["properties"].(map[string]interface{})
	if !ok || len(props) == 0 {
		return ""
	}
	for _, key := range []string{"query", "prompt", "goal", "task", "input", "text", "message", "keyword"} {
		if _, ok := props[key]; ok {
			return key
		}
	}
	return ""
}

// buildToolDescriptions 构建工具描述
func (p *Planner) buildToolDescriptions(tools []skill.ToolInfo) []skill.ToolInfo {
	return tools
}

// parseLLMResponseToPlan 解析 LLM 响应为计划
func (p *Planner) parseLLMResponseToPlan(response *llm.LLMResponse, availableTools []skill.ToolInfo) (*Plan, error) {
	if response == nil || response.Content == "" {
		return nil, errors.New(errors.ErrValidationFailed, "empty LLM response")
	}

	// 尝试提取 JSON 部分
	jsonContent := p.extractJSONFromResponse(response.Content)

	var plan Plan
	if err := json.Unmarshal([]byte(jsonContent), &plan); err != nil {
		return nil, errors.Wrap(errors.ErrValidationFailed, "failed to parse LLM response as JSON", err)
	}

	// 验证计划
	toolNames := make(map[string]bool)
	for _, tool := range availableTools {
		toolNames[tool.Name] = true
	}

	availableToolNames := make([]string, 0, len(toolNames))
	for name := range toolNames {
		availableToolNames = append(availableToolNames, name)
	}

	if err := plan.ValidatePlan(availableToolNames); err != nil {
		return nil, errors.Wrap(errors.ErrValidationFailed, "generated plan is invalid", err)
	}

	return &plan, nil
}

// extractJSONFromResponse 从响应中提取 JSON
func (p *Planner) extractJSONFromResponse(content string) string {
	// 查找第一个 { 和最后一个 }
	startIdx := strings.Index(content, "{")
	if startIdx == -1 {
		return content
	}

	endIdx := strings.LastIndex(content, "}")
	if endIdx == -1 || endIdx <= startIdx {
		return content
	}

	return content[startIdx : endIdx+1]
}

// SetLLMRuntime 设置 LLM Runtime
func (p *Planner) SetLLMRuntime(llmRuntime *llm.LLMRuntime) {
	p.llmRuntime = llmRuntime
}

// GetLLMRuntime 获取 LLM Runtime
func (p *Planner) GetLLMRuntime() *llm.LLMRuntime {
	return p.llmRuntime
}
