package skill

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Environment freeze keys mirror sessionmeta constants without importing that
// package (sessionmeta -> chat -> agent -> skill would create an import cycle).
const (
	skillEnvironmentContextBlock       = "environment_context_block"
	skillEnvironmentCapabilityGuidance = "environment_capability_guidance"
	skillEnvironmentProbedAt           = "environment_probed_at"
	skillEnvironmentValues             = "environment_values"
)

var workflowTemplatePattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.-]+)\s*\}\}`)

var systemRoleExpectedRolePattern = regexp.MustCompile(`messages?(?:\[\d+\]|\.\d+)?\.role.*\b(expected|must be|should be)\b.*\b(user|assistant|developer)\b`)

// Executor 技能执行器
type Executor struct {
	registry   *Registry
	mcpManager MCPManager
	llmRuntime *llm.LLMRuntime
	// defaultModel 是技能桥调用 LLM 时显式使用的模型；为空表示沿用运行时默认
	// 模型。会话宿主（例如 aicli 复用 runtime host 的共享 bootstrap）里运行时
	// 默认模型未必等于会话模型，留空会把请求发到无关模型/供应商上。
	defaultModel string
	// implicitIndex 是隐式调用判定索引（SK-3）；nil 表示不判定。
	implicitIndex *ImplicitInvocationIndex
}

// NewExecutor 创建执行器
func NewExecutor(registry *Registry, mcpManager MCPManager, llmRuntime *llm.LLMRuntime) *Executor {
	return &Executor{
		registry:   registry,
		mcpManager: mcpManager,
		llmRuntime: llmRuntime,
	}
}

// SetDefaultModel 设置技能桥请求使用的模型（调用方通常传入会话当前模型）。
func (e *Executor) SetDefaultModel(model string) {
	if e == nil {
		return
	}
	e.defaultModel = strings.TrimSpace(model)
}

// DefaultModel 返回当前配置的技能桥模型（空表示沿用运行时默认模型）。
func (e *Executor) DefaultModel() string {
	if e == nil {
		return ""
	}
	return e.defaultModel
}

// SetImplicitInvocationIndex 设置隐式调用判定索引（SK-3）。
// 传入 nil 表示停用隐式调用判定。索引由调用方随 registry 失效重建。
func (e *Executor) SetImplicitInvocationIndex(index *ImplicitInvocationIndex) {
	if e == nil {
		return
	}
	e.implicitIndex = index
}

// RefreshImplicitInvocationIndex 从当前 registry 重建隐式调用判定索引（SK-3）。
// 技能面（registry 摘要）变化后由宿主调用；registry 为空时索引自然为空。
func (e *Executor) RefreshImplicitInvocationIndex() {
	if e == nil || e.registry == nil {
		return
	}
	e.SetImplicitInvocationIndex(BuildImplicitInvocationIndex(e.registry.ListSummaries()))
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
	// ImplicitInvocations 记录本回合工具调用中命中的隐式技能调用（SK-3）。
	// 由 executeDefault 的工具循环填充，调用方负责发布 skills.invoked 事件。
	ImplicitInvocations []ImplicitInvocation `json:"implicit_invocations,omitempty"`
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

	// 执行模式：auto（默认）保持 Handler → Workflow → executeDefault 的既有优先级；
	// model 跳过直执行，把 skill 说明与程序清单交给模型，由模型选择要调用哪些程序。
	executionMode := resolveSkillExecutionMode(req)

	// 1. 如果有自定义处理器，直接执行
	if executionMode != SkillExecutionModeModel && skill.Handler != nil {
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
	if executionMode != SkillExecutionModeModel && skill.HasWorkflow() {
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
	// SK-7：显式声明 execution_mode: document 的技能不走 executeDefault 子调用；
	// 正文由 injection path 注入上下文，模型用既有工具完成任务。
	// （auto 识别在注册处处理，此处仅作显式声明的兜底。）
	if skill.ExecutionMode == ExecutionModeDocument {
		result.Success = true
		result.Output = fmt.Sprintf("skill %q is in document mode (execution_mode: document); its instructions have been injected into context. Use available tools directly.", skill.Name)
		return result, nil
	}
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

// BuildMainLoopPrompt 渲染「注入主循环」的技能指令文本。
//
// 说明型技能（无 handler / workflow）不再另起一次技能 LLM 交互（技能桥），
// 而是把技能正文与用户请求作为上下文交给宿主的主对话循环：由主循环的模型用
// 常规工具面（shell / 文件 / 网络等）决定如何执行，避免双重 LLM 循环与技能桥
// 的步数上限。
//
// 与 executeDefault 的区别：不注入宿主已经具备的 environment / shell / file
// editing 指引与历史消息，也不发起任何 LLM 调用，只做纯文本渲染。
func (e *Executor) BuildMainLoopPrompt(skill *Skill, req *types.Request) (string, error) {
	if skill == nil {
		return "", fmt.Errorf("skill is required")
	}
	resolved, err := e.resolveExecutableSkill(skill)
	if err != nil {
		return "", err
	}
	if resolved != nil {
		skill = resolved
	}
	systemPrompt, userPrompt, err := resolveSkillPrompts(skill)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(userPrompt) == "" && req != nil {
		userPrompt = strings.TrimSpace(req.Prompt)
	}

	name := strings.TrimSpace(skill.Name)
	var builder strings.Builder
	fmt.Fprintf(&builder, "已加载技能「%s」的指令；请使用当前对话已有的常规工具完成下面的技能指令与用户请求。\n", name)
	if description := strings.TrimSpace(skill.Description); description != "" {
		builder.WriteString("技能说明: " + description + "\n")
	}
	if body := strings.TrimSpace(systemPrompt); body != "" {
		builder.WriteString("\n## 技能指令\n")
		builder.WriteString(body)
		builder.WriteString("\n")
	}
	if request := strings.TrimSpace(userPrompt); request != "" {
		builder.WriteString("\n## 用户请求\n")
		builder.WriteString(request)
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String()), nil
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
	// 模型驱动模式：显式说明文档（systemPrompt / Codex Body）之外，再投影一份
	// skill 说明与程序清单，让模型先读文档、再决定调用哪些程序。
	if resolveSkillExecutionMode(req) == SkillExecutionModeModel {
		if guide := buildSkillProgramGuide(skill); guide != "" {
			messages = append(messages, *types.NewSystemMessage(guide))
		}
	}
	if environmentContext := buildEnvironmentContextMessage(req); environmentContext != "" {
		messages = append(messages, *types.NewSystemMessage("Environment context:\n" + environmentContext))
	}
	if shellGuidance := buildShellGuidanceMessage(req); shellGuidance != "" {
		messages = append(messages, *types.NewSystemMessage(shellGuidance))
	}
	if fileEditingGuidance := strings.TrimSpace(runtimeprompt.RenderFileEditingGuidance()); fileEditingGuidance != "" {
		messages = append(messages, *types.NewSystemMessage(fileEditingGuidance))
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

	// 构建工具定义：显式声明的工具（含 workflow 步骤引用的程序）优先；未声明工具的
	// 说明型技能（例如 Codex SKILL.md 技能）在工具循环开启时暴露运行时工具面，让
	// /skill 真正执行技能要求的动作，而不是只回显一段文本。模型驱动模式下工具循环
	// 默认开启（显式 tool_loop=false 仍可关闭）。
	var tools []types.ToolDefinition
	if programTools := skillProgramTools(skill); len(programTools) > 0 {
		tools = e.buildToolDefinitions(ctx, programTools)
	} else if skillToolLoopRequested(req) {
		tools = e.buildToolDefinitions(ctx, e.runtimeToolNames())
	}

	// 调用 LLM
	llmRequest := &llm.LLMRequest{
		Model:           e.defaultModel,
		Messages:        messages,
		Tools:           tools,
		MaxTokens:       4096,
		Temperature:     0.7,
		ReasoningEffort: types.NormalizeReasoningEffort(req.ReasoningEffort),
		Thinking:        types.CloneThinkingConfig(req.Thinking),
	}

	maxSteps := resolveSkillToolLoopMaxSteps(req, len(tools) > 0)
	observations := make([]types.Observation, 0, 4)
	lastContent := ""
	implicit := make([]ImplicitInvocation, 0, 4)
	for step := 0; step < maxSteps; step++ {
		response, callErr := e.callSkillLLM(ctx, llmRequest)
		if callErr != nil {
			return e.attachImplicitInvocations(&ExecuteResult{
				SkillName:    skill.Name,
				Success:      false,
				Output:       "",
				Error:        callErr.Error(),
				Observations: observations,
			}, implicit), callErr
		}
		if response == nil {
			break
		}
		lastContent = response.Content

		calls := normalizeSkillToolCalls(*response)
		if len(calls) == 0 {
			return e.attachImplicitInvocations(&ExecuteResult{
				SkillName:    skill.Name,
				Success:      true,
				Output:       response.Content,
				Usage:        response.Usage,
				Observations: observations,
			}, implicit), nil
		}
		if len(llmRequest.Tools) == 0 {
			// 模型发起了工具调用，但当前执行路径没有工具面：不要回显原始标记
			// 文本（DSML/JSON 片段），返回可解释的错误让调用方决策。
			return e.attachImplicitInvocations(&ExecuteResult{
				SkillName:    skill.Name,
				Success:      false,
				Output:       "",
				Error:        skillToolCallsWithoutToolsError(calls),
				Observations: observations,
			}, implicit), nil
		}

		messages = append(messages, newAssistantToolCallMessage(response.Content, calls))
		stepObservations, toolMessages := e.executeSkillToolCalls(ctx, calls)
		observations = append(observations, stepObservations...)
		messages = append(messages, toolMessages...)
		e.collectImplicitInvocations(calls, &implicit)
		llmRequest.Messages = messages
	}

	capNote := fmt.Sprintf("[已达工具调用步数上限（%d），技能可能未完成]", maxSteps)
	if strings.TrimSpace(lastContent) == "" {
		lastContent = capNote
	} else {
		lastContent = strings.TrimSpace(lastContent) + "\n\n" + capNote
	}
	return e.attachImplicitInvocations(&ExecuteResult{
		SkillName:    skill.Name,
		Success:      true,
		Output:       lastContent,
		Observations: observations,
	}, implicit), nil
}

// attachImplicitInvocations 把收集到的隐式调用附加到结果（SK-3）。
func (e *Executor) attachImplicitInvocations(result *ExecuteResult, implicit []ImplicitInvocation) *ExecuteResult {
	if result == nil {
		return result
	}
	if len(implicit) == 0 {
		return result
	}
	result.ImplicitInvocations = append(result.ImplicitInvocations, DedupeInvocations(implicit)...)
	return result
}

// collectImplicitInvocations 扫描本回合工具调用，把命中的隐式技能调用追加到结果（SK-3）。
// 仅当 SetImplicitInvocationIndex 设置过索引时生效；结果由调用方在回合结束后发布。
func (e *Executor) collectImplicitInvocations(calls []types.ToolCall, implicit *[]ImplicitInvocation) {
	if e == nil || e.implicitIndex == nil || len(calls) == 0 || implicit == nil {
		return
	}
	for _, call := range calls {
		matches := DetectImplicitInvocations(e.implicitIndex, call.Name, call.Args)
		if len(matches) > 0 {
			*implicit = append(*implicit, matches...)
		}
	}
}

// callSkillLLM 调用技能桥 LLM，并保留既有的 system-role 回退行为。
func (e *Executor) callSkillLLM(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	response, err := e.llmRuntime.Call(ctx, req)
	if err != nil {
		if fallbackMessages, ok := buildSystemRoleFallbackMessages(req.Messages, err); ok {
			req.Messages = fallbackMessages
			response, err = e.llmRuntime.Call(ctx, req)
		}
	}
	return response, err
}

// newAssistantToolCallMessage 构造携带工具调用的 assistant 消息，供下一轮
// 请求重放（与主 agent 循环的消息形态保持一致）。
func newAssistantToolCallMessage(content string, calls []types.ToolCall) types.Message {
	message := *types.NewAssistantMessage(assistantContentForToolCalls(content))
	message.ToolCalls = calls
	return message
}

// normalizeSkillToolCalls 归一化模型返回的工具调用：
//   - 优先使用协议解析出的 ToolCalls；
//   - 协议未解析（例如 DeepSeek 原生 DSML 标记落到 content）时回退解析文本。
func normalizeSkillToolCalls(response llm.LLMResponse) []types.ToolCall {
	calls := normalizeParsedToolCalls(response.ToolCalls)
	if len(calls) > 0 {
		return calls
	}
	return parseDSMLToolCalls(response.Content)
}

func normalizeParsedToolCalls(raw []types.ToolCall) []types.ToolCall {
	if len(raw) == 0 {
		return nil
	}
	calls := make([]types.ToolCall, 0, len(raw))
	for index, call := range raw {
		name := strings.TrimSpace(call.Name)
		if name == "" {
			continue
		}
		normalized := types.ToolCall{
			ID:       strings.TrimSpace(call.ID),
			Type:     strings.TrimSpace(call.Type),
			Name:     name,
			Args:     call.Args,
			RawInput: call.RawInput,
		}
		if normalized.ID == "" {
			normalized.ID = fmt.Sprintf("skill_call_%d", index+1)
		}
		if normalized.Type == "" {
			normalized.Type = "function"
		}
		if normalized.Args == nil {
			normalized.Args = map[string]interface{}{}
		}
		calls = append(calls, normalized)
	}
	return calls
}

func skillToolCallsWithoutToolsError(calls []types.ToolCall) string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		if name := strings.TrimSpace(call.Name); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		names = append(names, "unknown")
	}
	return "模型发起了工具调用（" + strings.Join(names, ", ") + "），但当前技能执行路径没有可用工具面；" +
		"请在启用工具循环的执行路径（如 /skill）中重试，或为该技能声明 tools。"
}

const (
	// SkillExecutionModeAuto 保持既有优先级：Handler → Workflow → executeDefault。
	SkillExecutionModeAuto = "auto"
	// SkillExecutionModeModel 跳过直执行，由模型读取说明并自行选择要调用的程序。
	SkillExecutionModeModel     = "model"
	skillExecutionModeOptionKey = "execution_mode"

	skillToolLoopOptionKey         = "tool_loop"
	skillToolLoopMaxStepsOptionKey = "tool_loop_max_steps"
	skillToolLoopDefaultMaxSteps   = 8
	skillToolLoopMaxStepsLimit     = 32
	skillToolResultMaxRunes        = 16000
	dsmlMarker                     = "｜｜DSML｜｜"
	dsmlMarkerASCII                = "||DSML||"
)

// skillToolLoopEnabled 读取调用方显式开启的工具循环选项。默认关闭，保持旧
// 调用方的单次调用语义；aicli 的 /skill 路径会为说明型技能开启它。
func skillToolLoopEnabled(req *types.Request) bool {
	if req == nil || len(req.Options) == 0 {
		return false
	}
	value, ok := req.Options[skillToolLoopOptionKey]
	if !ok {
		return false
	}
	return skillOptionTruthy(value)
}

// skillToolLoopRequested 在显式选项缺省时按执行模式回落：模型驱动模式默认开启
// 工具循环；调用方显式给 `tool_loop=false` 时仍尊重显式关闭。
func skillToolLoopRequested(req *types.Request) bool {
	if req != nil && len(req.Options) > 0 {
		if value, ok := req.Options[skillToolLoopOptionKey]; ok {
			return skillOptionTruthy(value)
		}
	}
	return resolveSkillExecutionMode(req) == SkillExecutionModeModel
}

func skillOptionTruthy(value interface{}) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "on", "true", "auto", "enabled", "yes", "1":
			return true
		}
	}
	return false
}

// resolveSkillToolLoopMaxSteps 解析工具循环步数上限。没有工具面时退化为单次
// 调用（仍会识别“模型发起工具调用但无工具可执行”并给出可解释错误）。
func resolveSkillToolLoopMaxSteps(req *types.Request, hasTools bool) int {
	if !hasTools {
		return 1
	}
	steps := skillToolLoopDefaultMaxSteps
	if req != nil && len(req.Options) > 0 {
		switch typed := req.Options[skillToolLoopMaxStepsOptionKey].(type) {
		case float64:
			steps = int(typed)
		case int:
			steps = typed
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
				steps = parsed
			}
		}
	}
	if steps <= 0 {
		steps = skillToolLoopDefaultMaxSteps
	}
	if steps > skillToolLoopMaxStepsLimit {
		steps = skillToolLoopMaxStepsLimit
	}
	return steps
}

// resolveSkillExecutionMode 解析执行模式；缺省或未知值回落 auto，保持既有调用方语义。
func resolveSkillExecutionMode(req *types.Request) string {
	if req == nil || len(req.Options) == 0 {
		return SkillExecutionModeAuto
	}
	raw, ok := req.Options[skillExecutionModeOptionKey]
	if !ok {
		return SkillExecutionModeAuto
	}
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(raw))) {
	case SkillExecutionModeModel, "llm", "agent":
		return SkillExecutionModeModel
	default:
		return SkillExecutionModeAuto
	}
}

// skillProgramTools 返回模型在 model 模式下可选择的程序清单：
// 声明 tools ∪ workflow 步骤引用的 tools（去重保序）。
func skillProgramTools(skill *Skill) []string {
	if skill == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(skill.Tools))
	tools := make([]string, 0, len(skill.Tools))
	appendTool := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		tools = append(tools, name)
	}
	for _, name := range skill.Tools {
		appendTool(name)
	}
	if skill.Workflow != nil {
		for _, step := range skill.Workflow.Steps {
			appendTool(step.Tool)
		}
	}
	return tools
}

// buildSkillProgramGuide 把 skill 说明与程序清单投影为系统消息：模型先读文档，
// 再自行选择调用哪些程序（而不是由运行时替它决定）。
func buildSkillProgramGuide(skill *Skill) string {
	if skill == nil || strings.TrimSpace(skill.Name) == "" {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("Skill program guide:\n")
	fmt.Fprintf(&builder, "- skill: %s\n", skill.Name)
	if description := strings.TrimSpace(skill.Description); description != "" {
		fmt.Fprintf(&builder, "- description: %s\n", description)
	}
	if programs := skillProgramTools(skill); len(programs) > 0 {
		builder.WriteString("- available programs (call the tool that matches the task):\n")
		for _, name := range programs {
			fmt.Fprintf(&builder, "  - %s\n", name)
		}
	}
	if skill.Workflow != nil && len(skill.Workflow.Steps) > 0 {
		builder.WriteString("- workflow steps (reference only; choose the programs you actually need):\n")
		for _, step := range skill.Workflow.Steps {
			line := fmt.Sprintf("  - %s", strings.TrimSpace(step.ID))
			if name := strings.TrimSpace(step.Name); name != "" {
				line += fmt.Sprintf(" (%s)", name)
			}
			if tool := strings.TrimSpace(step.Tool); tool != "" {
				line += fmt.Sprintf(" → tool %s", tool)
			}
			if len(step.Args) > 0 {
				if raw, err := json.Marshal(step.Args); err == nil {
					line += fmt.Sprintf("; args=%s", string(raw))
				}
			}
			builder.WriteString(line + "\n")
		}
	}
	builder.WriteString("- note: a user message may start with a `/skill <name>` call marker; the marker is the invocation itself, not part of the request — use the text after it as the concrete arguments.\n")
	builder.WriteString("Read this guide, then call the appropriate program(s) with concrete arguments instead of only describing them.\n\n")
	builder.WriteString(skillUsageDisciplineFooter())
	return builder.String()
}

// skillUsageDisciplineFooter 把 SK-2 的"How to use skills"纪律精要附加到单技能
// ProgramGuide（Phase 1 临时接入；完整版见 catalog_render.go 的 RenderSkillCatalog）。
func skillUsageDisciplineFooter() string {
	return "## How to use this skill\n" +
		"- Trigger: you must use this skill when the user names it or the task clearly matches its description; do not carry it across turns unless re-mentioned.\n" +
		"- Read this guide fully before acting; do not delegate reading/summarizing it to a subagent.\n" +
		"- Resolve references/scripts/assets relative to the skill directory; prefer running scripts over retyping code.\n" +
		"- If you skip an obvious skill, say why; if a skill can't be applied cleanly, state the issue and fall back.\n"
}

// ProgramGuide 返回 skill 的模型可读说明与程序清单投影。宿主（如 skills API 的
// 显式 expose_skills 回合）与 skill.Executor 的 execution_mode=model 共用同一实现，
// 避免"模型读到的文档"出现两套口径。
func ProgramGuide(skill *Skill) string {
	return buildSkillProgramGuide(skill)
}

// ProgramTools 返回 skill 声明的程序清单（tools ∪ workflow steps 引用的 tools，去重保序）。
func ProgramTools(skill *Skill) []string {
	return skillProgramTools(skill)
}

// runtimeToolNames 返回运行时工具面（工具管理器 + MCP）中可暴露给技能桥的工具名。
func (e *Executor) runtimeToolNames() []string {
	if e == nil || e.mcpManager == nil {
		return nil
	}
	infos := e.mcpManager.ListTools()
	names := make([]string, 0, len(infos))
	seen := make(map[string]bool, len(infos))
	for _, info := range infos {
		name := strings.TrimSpace(info.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// executeSkillToolCalls 依次执行模型发起的工具调用，返回观察记录与可供下一轮
// 请求重放的 tool 消息。工具失败（找不到工具/执行报错）不中断循环，而是把
// 错误作为工具结果反馈给模型，让它可以换一种做法。
//
// 权限：嵌套工具调用由上层（例如 aicli 的 /skill 直接调用）完成授权；该路径
// 已按技能级 CapExternalSideEffect 走审批，工具级策略的逐调用复核仍待补充。
func (e *Executor) executeSkillToolCalls(ctx context.Context, calls []types.ToolCall) ([]types.Observation, []types.Message) {
	observations := make([]types.Observation, 0, len(calls))
	messages := make([]types.Message, 0, len(calls))
	for index, call := range calls {
		observation := types.NewObservation(fmt.Sprintf("%d", index+1), call.Name)
		observation.WithInput(call.Args)
		message := *types.NewToolMessage(call.ID, "")

		if e == nil || e.mcpManager == nil {
			errText := "skill tool runtime is not configured"
			observation.MarkFailure(errText)
			message.Content = "tool execution failed: " + errText
			observations = append(observations, *observation)
			messages = append(messages, message)
			continue
		}

		info, findErr := e.mcpManager.FindTool(call.Name)
		if findErr != nil {
			observation.MarkFailure(findErr.Error())
			message.Content = "tool execution failed: " + findErr.Error()
			observations = append(observations, *observation)
			messages = append(messages, message)
			continue
		}

		output, callErr := e.mcpManager.CallTool(ctx, strings.TrimSpace(info.MCPName), call.Name, call.Args)
		text := formatSkillToolOutput(output)
		if callErr != nil {
			observation.MarkFailure(callErr.Error())
			if strings.TrimSpace(text) == "" {
				text = callErr.Error()
			}
		} else {
			observation.MarkSuccess()
		}
		text = truncateSkillToolResult(text)
		observation.WithOutput(text)
		message.Content = text
		observations = append(observations, *observation)
		messages = append(messages, message)
	}
	return observations, messages
}

func formatSkillToolOutput(output interface{}) string {
	switch typed := output.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(raw)
	}
}

func truncateSkillToolResult(text string) string {
	runes := []rune(text)
	if len(runes) <= skillToolResultMaxRunes {
		return text
	}
	return string(runes[:skillToolResultMaxRunes]) + "\n...[工具输出已截断]"
}

func containsDSMLMarkup(content string) bool {
	return strings.Contains(content, dsmlMarker) || strings.Contains(content, dsmlMarkerASCII)
}

// parseDSMLToolCalls 解析落到 content 里的 DeepSeek 原生 DSML 工具调用标记
// （协议适配缺失时会出现）。解析失败返回 nil，由调用方决定降级行为。
func parseDSMLToolCalls(content string) []types.ToolCall {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || !containsDSMLMarkup(trimmed) {
		return nil
	}
	var calls []types.ToolCall
	rest := trimmed
	for {
		idx := strings.Index(rest, "invoke name=\"")
		if idx < 0 {
			break
		}
		rest = rest[idx+len("invoke name=\""):]
		nameEnd := strings.Index(rest, "\"")
		if nameEnd < 0 {
			break
		}
		name := strings.TrimSpace(rest[:nameEnd])
		rest = rest[nameEnd+1:]

		invokeEnd := len(rest)
		for _, boundary := range []string{"invoke>", "invoke name=\""} {
			if boundaryIdx := strings.Index(rest, boundary); boundaryIdx >= 0 && boundaryIdx < invokeEnd {
				invokeEnd = boundaryIdx
			}
		}
		body := rest[:invokeEnd]
		rest = rest[invokeEnd:]

		if name == "" {
			continue
		}
		calls = append(calls, types.ToolCall{
			ID:   fmt.Sprintf("dsml_call_%d", len(calls)+1),
			Type: "function",
			Name: name,
			Args: parseDSMLInvokeParameters(body),
		})
	}
	return calls
}

func parseDSMLInvokeParameters(body string) map[string]interface{} {
	args := make(map[string]interface{})
	rest := body
	for {
		idx := strings.Index(rest, "parameter name=\"")
		if idx < 0 {
			break
		}
		rest = rest[idx+len("parameter name=\""):]
		nameEnd := strings.Index(rest, "\"")
		if nameEnd < 0 {
			break
		}
		name := strings.TrimSpace(rest[:nameEnd])
		rest = rest[nameEnd+1:]

		stringFlag := "false"
		if flagIdx := strings.Index(rest, "string=\""); flagIdx >= 0 {
			flagRest := rest[flagIdx+len("string=\""):]
			if flagEnd := strings.Index(flagRest, "\""); flagEnd >= 0 {
				stringFlag = strings.TrimSpace(flagRest[:flagEnd])
			}
		}

		valueStart := strings.Index(rest, ">")
		if valueStart < 0 {
			break
		}
		valueText := rest[valueStart+1:]
		valueEnd := len(valueText)
		for _, terminator := range []string{"</" + dsmlMarker + " parameter>", "</" + dsmlMarkerASCII + " parameter>", "parameter>"} {
			if termIdx := strings.Index(valueText, terminator); termIdx >= 0 && termIdx < valueEnd {
				valueEnd = termIdx
			}
		}
		value := strings.TrimSpace(valueText[:valueEnd])
		rest = valueText[valueEnd:]

		if name == "" {
			continue
		}
		args[name] = decodeDSMLArgValue(value, stringFlag)
	}
	return args
}

func decodeDSMLArgValue(value, stringFlag string) interface{} {
	if strings.EqualFold(strings.TrimSpace(stringFlag), "true") {
		return value
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	var decoded interface{}
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		return decoded
	}
	return value
}

// assistantContentForToolCalls 在重放工具调用时清理 DSML 回退解析出来的原生
// 标记，避免把标记文本再次送回模型。
func assistantContentForToolCalls(content string) string {
	if !containsDSMLMarkup(content) {
		return content
	}
	for _, marker := range []string{dsmlMarker + " calls", dsmlMarkerASCII + " calls", dsmlMarker + " invoke", dsmlMarkerASCII + " invoke"} {
		if idx := strings.Index(content, marker); idx >= 0 {
			return strings.TrimSpace(content[:idx])
		}
	}
	return ""
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
		// Frozen environment prompt fragments / measured host facts are assembled
		// into dedicated system messages; keep them out of the runtime summary.
		if key == skillEnvironmentContextBlock ||
			key == skillEnvironmentCapabilityGuidance ||
			key == skillEnvironmentProbedAt ||
			key == skillEnvironmentValues ||
			isEnvironmentFactKey(key) {
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

func buildEnvironmentContextMessage(req *types.Request) string {
	workspacePath := ""
	if req != nil && len(req.Context) > 0 {
		// Prefer the session-frozen environment block injected by the caller
		// (HTTP agent path). Avoid re-probing the host on every skill invoke.
		if value, ok := req.Context[skillEnvironmentContextBlock].(string); ok {
			if block := strings.TrimSpace(value); block != "" {
				return block
			}
		}
		if value, ok := req.Context["workspace_path"].(string); ok {
			workspacePath = strings.TrimSpace(value)
		}
	}
	// Standalone skill invocations without a frozen session snapshot still get
	// a one-shot measured block for planning. Callers that own multi-turn
	// history should freeze and pass environment_context_block instead.
	return strings.TrimSpace(runtimeprompt.RenderEnvironmentContextBlock(workspacePath))
}

func buildShellGuidanceMessage(req *types.Request) string {
	capability := ""
	if req != nil && len(req.Context) > 0 {
		if value, ok := req.Context[skillEnvironmentCapabilityGuidance].(string); ok {
			capability = strings.TrimSpace(value)
		}
	}
	// When no frozen capability text is present, RenderShellExecutionGuidance
	// measures once for this standalone invoke.
	if capability == "" {
		return strings.TrimSpace(runtimeprompt.RenderShellExecutionGuidance())
	}
	return strings.TrimSpace(runtimeprompt.RenderShellExecutionGuidanceWithCapability(capability))
}

func isEnvironmentFactKey(key string) bool {
	switch strings.TrimSpace(key) {
	case "os", "shell", "current_date", "timezone", "available_commands", "unavailable_commands":
		return true
	default:
		return false
	}
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
		if resolver, ok := e.mcpManager.(interface{ ResolveToolSource(string) string }); ok {
			if source := toolresult.NormalizeSource(resolver.ResolveToolSource(toolInfo.Name)); source != "" {
				tool.Metadata = map[string]interface{}{
					toolresult.SourceKey: source,
				}
			}
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
