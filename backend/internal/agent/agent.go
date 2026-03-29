package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	mcpcatalog "github.com/ai-gateway/ai-agent-runtime/internal/mcp/catalog"
	"github.com/ai-gateway/ai-agent-runtime/internal/artifact"
	"github.com/ai-gateway/ai-agent-runtime/internal/contextmgr"
	"github.com/ai-gateway/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/ai-gateway/ai-agent-runtime/internal/events"
	"github.com/ai-gateway/ai-agent-runtime/internal/llm"
	"github.com/ai-gateway/ai-agent-runtime/internal/memory"
	"github.com/ai-gateway/ai-agent-runtime/internal/output"
	"github.com/ai-gateway/ai-agent-runtime/internal/skill"
	"github.com/ai-gateway/ai-agent-runtime/internal/types"
	"github.com/ai-gateway/ai-agent-runtime/internal/workspace"
)

// Config Agent 配置
type Config struct {
	Name              string                 `yaml:"name" json:"name"`
	Provider          string                 `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model             string                 `yaml:"model" json:"model"`
	MaxSteps          int                    `yaml:"maxSteps" json:"maxSteps"`
	DefaultMaxTokens  int                    `yaml:"defaultMaxTokens" json:"defaultMaxTokens"`
	Temperature       float64                `yaml:"temperature" json:"temperature"`
	SystemPrompt      string                 `yaml:"systemPrompt" json:"systemPrompt"`
	EnableMemory      bool                   `yaml:"enableMemory" json:"enableMemory"`
	EnablePlanning    bool                   `yaml:"enablePlanning" json:"enablePlanning"`
	EnableSelfReflect bool                   `yaml:"enableSelfReflect" json:"enableSelfReflect"`
	MemoryMaxSize     int                    `yaml:"memoryMaxSize" json:"memoryMaxSize"`
	ArtifactStorePath string                 `yaml:"artifactStorePath" json:"artifactStorePath"`
	Options           map[string]interface{} `yaml:"options" json:"options"`
}

// Agent AI Agent
type Agent struct {
	config        *Config
	skillRouter   *skill.Router
	skillExec     *skill.Executor
	mcpManager    skill.MCPManager
	llmRuntime    *llm.LLMRuntime
	memory        *memory.Memory
	planner       *Planner
	artifacts     *artifact.Store
	contextMgr    *contextmgr.Manager
	outputGate    *output.Gateway
	subagents     *SubagentScheduler
	toolCatalog   *mcpcatalog.Catalog
	eventBus      *runtimeevents.Bus
	promptBuild   *PromptBuilder
	toolPolicy    *ToolExecutionPolicy
	toolHooks     ToolHooks
	permEngine    *PermissionEngine
	toolBroker    *ToolBroker
	hookManager   *HookManager
	checkpointMgr *CheckpointManager

	mu      sync.RWMutex
	running bool
	state   AgentState
}

// AgentState Agent 状态
type AgentState struct {
	CurrentStep int         `json:"currentStep" yaml:"currentStep"`
	Running     bool        `json:"running" yaml:"running"`
	Errors      []string    `json:"errors" yaml:"errors"`
	Context     interface{} `json:"context" yaml:"context"`
}

// Result Agent 执行结果
type Result struct {
	Success      bool                `json:"success"`
	Output       string              `json:"output"`
	Steps        int                 `json:"steps"`
	Observations []types.Observation `json:"observations"`
	Skill        string              `json:"skill,omitempty"`
	TraceID      string              `json:"trace_id,omitempty"`
	State        AgentState          `json:"state"`
	Usage        *types.TokenUsage   `json:"usage,omitempty"`
	Duration     types.Duration      `json:"duration"`
	Error        string              `json:"error,omitempty"`
}

// NewAgent 创建 Agent
func NewAgent(cfg *Config, mcpManager skill.MCPManager) *Agent {
	return NewAgentWithLLM(cfg, mcpManager, nil)
}

// NewAgentWithLLM 创建带 LLM Runtime 的 Agent
func NewAgentWithLLM(cfg *Config, mcpManager skill.MCPManager, llmRuntime *llm.LLMRuntime) *Agent {
	// 设置默认配置
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 10
	}
	if cfg.DefaultMaxTokens <= 0 {
		cfg.DefaultMaxTokens = 4096
	}
	if cfg.MemoryMaxSize <= 0 {
		cfg.MemoryMaxSize = 100
	}

	registry := skill.NewRegistry(mcpManager)
	router := skill.NewRouter(registry)
	executor := skill.NewExecutor(registry, mcpManager, llmRuntime)
	artifactStore := newArtifactStore(cfg)
	planner := NewPlannerWithLLM(mcpManager, llmRuntime)
	planner.provider = cfg.Provider
	planner.model = cfg.Model
	contextMgr := newDefaultContextManager(cfg, artifactStore)
	outputGate := newDefaultOutputGateway(cfg, artifactStore)
	toolCatalog := newDefaultToolCatalog(mcpManager)

	return &Agent{
		config:      cfg,
		skillRouter: router,
		skillExec:   executor,
		mcpManager:  mcpManager,
		llmRuntime:  llmRuntime,
		memory:      memory.NewMemory(cfg.MemoryMaxSize),
		planner:     planner,
		artifacts:   artifactStore,
		contextMgr:  contextMgr,
		outputGate:  outputGate,
		subagents:   NewSubagentScheduler(nil, SubagentSchedulerConfig{}),
		toolCatalog: toolCatalog,
		eventBus:    runtimeevents.NewBus(),
		promptBuild: NewPromptBuilder(),
		running:     false,
		state: AgentState{
			Running: false,
			Errors:  make([]string, 0),
		},
	}
}

// GetConfig 获取配置
func (a *Agent) GetConfig() *Config {
	return a.config
}

// GetState 获取状态
func (a *Agent) GetState() AgentState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state
}

// IsRunning 检查是否运行中
func (a *Agent) IsRunning() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.running
}

// SetRunning 设置运行状态
func (a *Agent) SetRunning(running bool) {
	a.mu.Lock()
	a.running = running
	a.state.Running = running
	a.mu.Unlock()
}

// AddError 添加错误
func (a *Agent) AddError(errStr string) {
	a.mu.Lock()
	a.state.Errors = append(a.state.Errors, errStr)
	a.mu.Unlock()
}

// ClearErrors 清空错误
func (a *Agent) ClearErrors() {
	a.mu.Lock()
	a.state.Errors = make([]string, 0)
	a.mu.Unlock()
}

// GetSkillRouter 获取 Skill Router
func (a *Agent) GetSkillRouter() *skill.Router {
	return a.skillRouter
}

// GetSkillExecutor 获取 Skill Executor
func (a *Agent) GetSkillExecutor() *skill.Executor {
	return a.skillExec
}

// GetMemory 获取 Memory
func (a *Agent) GetMemory() *memory.Memory {
	return a.memory
}

// GetPlanner 获取 Planner
func (a *Agent) GetPlanner() *Planner {
	return a.planner
}

// GetSubagentScheduler 返回子代理调度器。
func (a *Agent) GetSubagentScheduler() *SubagentScheduler {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.subagents == nil {
		a.subagents = NewSubagentScheduler(a, SubagentSchedulerConfig{})
	}
	if a.subagents.parent == nil {
		a.subagents.parent = a
	}
	return a.subagents
}

// SetSubagentScheduler 覆盖默认子代理调度器。
func (a *Agent) SetSubagentScheduler(scheduler *SubagentScheduler) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if scheduler != nil && scheduler.parent == nil {
		scheduler.parent = a
	}
	a.subagents = scheduler
}

// GetToolCatalog 返回 MCP Tool Catalog。
func (a *Agent) GetToolCatalog() *mcpcatalog.Catalog {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.toolCatalog == nil {
		a.toolCatalog = newDefaultToolCatalog(a.mcpManager)
	} else if a.mcpManager != nil {
		a.toolCatalog.Refresh(a.mcpManager.ListTools())
	}
	return a.toolCatalog
}

// SetToolCatalog 覆盖默认 Tool Catalog。
func (a *Agent) SetToolCatalog(catalog *mcpcatalog.Catalog) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.toolCatalog = catalog
}

// GetEventBus 返回 runtime event bus。
func (a *Agent) GetEventBus() *runtimeevents.Bus {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.eventBus == nil {
		a.eventBus = runtimeevents.NewBus()
	}
	if a.contextMgr != nil {
		a.contextMgr.Events = a.eventBus
		a.contextMgr.Agent = a.config.Name
	}
	return a.eventBus
}

// SetEventBus 覆盖默认 runtime event bus。
func (a *Agent) SetEventBus(bus *runtimeevents.Bus) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.eventBus = bus
	if a.contextMgr != nil {
		a.contextMgr.Events = bus
		a.contextMgr.Agent = a.config.Name
	}
}

func (a *Agent) emitRuntimeEvent(eventType, sessionID, toolName string, payload map[string]interface{}) {
	traceID := ""
	if payload != nil {
		if value, ok := payload["trace_id"].(string); ok {
			traceID = value
		}
	}
	if bus := a.GetEventBus(); bus != nil {
		bus.Publish(runtimeevents.Event{
			Type:      eventType,
			TraceID:   traceID,
			AgentName: a.config.Name,
			SessionID: sessionID,
			ToolName:  toolName,
			Payload:   payload,
		})
	}
}

// GetPromptBuilder 返回子代理 prompt builder。
func (a *Agent) GetPromptBuilder() *PromptBuilder {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.promptBuild == nil {
		a.promptBuild = NewPromptBuilder()
	}
	return a.promptBuild
}

// SetPromptBuilder 覆盖默认 prompt builder。
func (a *Agent) SetPromptBuilder(builder *PromptBuilder) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.promptBuild = builder
}

// GetToolExecutionPolicy 返回工具执行策略。
func (a *Agent) GetToolExecutionPolicy() *ToolExecutionPolicy {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.toolPolicy
}

// SetToolExecutionPolicy 覆盖工具执行策略。
func (a *Agent) SetToolExecutionPolicy(policy *ToolExecutionPolicy) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.toolPolicy = policy
}

// GetPermissionEngine returns the permission engine.
func (a *Agent) GetPermissionEngine() *PermissionEngine {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.permEngine
}

// SetPermissionEngine sets the permission engine.
func (a *Agent) SetPermissionEngine(engine *PermissionEngine) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.permEngine = engine
	if a.permEngine != nil && a.permEngine.Policy == nil {
		a.permEngine.Policy = a.toolPolicy
	}
	if a.permEngine != nil && a.permEngine.Hooks == nil && a.hookManager != nil {
		a.permEngine.Hooks = a.hookManager
	}
}

// GetToolBroker returns the runtime tool broker.
func (a *Agent) GetToolBroker() *ToolBroker {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.toolBroker
}

// SetToolBroker sets the runtime tool broker.
func (a *Agent) SetToolBroker(broker *ToolBroker) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.toolBroker = broker
}

// GetHookManager returns the hook manager.
func (a *Agent) GetHookManager() *HookManager {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.hookManager
}

// SetHookManager sets the hook manager.
func (a *Agent) SetHookManager(manager *HookManager) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hookManager = manager
	if a.permEngine != nil && a.permEngine.Hooks == nil && manager != nil {
		a.permEngine.Hooks = manager
	}
}

// GetOutputGateway 获取工具输出治理网关。
func (a *Agent) GetOutputGateway() *output.Gateway {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.outputGate == nil {
		if a.artifacts == nil {
			a.artifacts = newArtifactStore(a.config)
		}
		a.outputGate = newDefaultOutputGateway(a.config, a.artifacts)
	}

	return a.outputGate
}

// SetOutputGateway 覆盖默认的工具输出治理网关。
func (a *Agent) SetOutputGateway(gateway *output.Gateway) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.outputGate = gateway
}

// GetArtifactStore 返回窗口外 artifact store。
func (a *Agent) GetArtifactStore() *artifact.Store {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.artifacts == nil {
		a.artifacts = newArtifactStore(a.config)
	}

	return a.artifacts
}

// GetContextManager 返回上下文治理器。
func (a *Agent) GetContextManager() *contextmgr.Manager {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.contextMgr == nil {
		if a.artifacts == nil {
			a.artifacts = newArtifactStore(a.config)
		}
		a.contextMgr = newDefaultContextManager(a.config, a.artifacts)
	}
	if a.contextMgr != nil {
		a.contextMgr.Events = a.eventBus
		a.contextMgr.Agent = a.config.Name
	}

	return a.contextMgr
}

// SetContextManager 覆盖默认 context manager。
func (a *Agent) SetContextManager(manager *contextmgr.Manager) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.contextMgr = manager
}

// LoadSkills 加载 Skills
func (a *Agent) LoadSkills(dir string) error {
	loader := skill.NewLoader(a.mcpManager)
	skills, err := loader.Load(dir)
	if err != nil {
		return errors.New(errors.ErrSkillLoadFailed,
			fmt.Sprintf("failed to load skills from directory: %s", dir))
	}

	// 注册所有 Skills
	registry := a.skillRouter.Registry()
	var errs []error
	for _, s := range skills {
		if err := registry.Register(s); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to register %d skills: %v", len(errs), errs)
	}

	return nil
}

// RegisterSkill 注册单个 Skill
func (a *Agent) RegisterSkill(s *skill.Skill) error {
	return a.skillRouter.Registry().Register(s)
}

// UnregisterSkill 注销 Skill
func (a *Agent) UnregisterSkill(name string) {
	a.skillRouter.Registry().Unregister(name)
}

// GetSkill 获取 Skill
func (a *Agent) GetSkill(name string) (*skill.Skill, bool) {
	return a.skillRouter.Registry().Get(name)
}

// ListSkills 列出所有 Skills
func (a *Agent) ListSkills() []*skill.Skill {
	registry := a.skillRouter.Registry()
	if registry == nil {
		return nil
	}
	summaries := registry.ListSummaries()
	skills := make([]*skill.Skill, 0, len(summaries))
	for _, summary := range summaries {
		if summary == nil {
			continue
		}
		skills = append(skills, summary.ToSkillStub())
	}
	return skills
}

func (a *Agent) buildRequest(prompt string, history []types.Message, includePromptInHistory bool, contextValues map[string]interface{}, reasoningEffort string, thinking *types.ThinkingConfig) *types.Request {
	req := types.NewRequest(prompt)
	if len(history) > 0 {
		req.History = append(req.History, history...)
	}
	req.ReasoningEffort = reasoningEffort
	req.Thinking = types.CloneThinkingConfig(thinking)
	for key, value := range contextValues {
		req.Context[key] = value
	}
	if includePromptInHistory {
		req.AddToHistory(*types.NewUserMessage(prompt))
	}
	if a.config.SystemPrompt != "" {
		req.History = append([]types.Message{*types.NewSystemMessage(a.config.SystemPrompt)}, req.History...)
	}
	return req
}

func (a *Agent) runWithPreparedRoutes(ctx context.Context, req *types.Request, routes []*skill.RouteResult) (*Result, error) {
	if len(routes) == 0 {
		return a.RunDefaultMode(ctx, req)
	}

	best := routes[0]
	if best.Skill == nil {
		return a.RunDefaultMode(ctx, req)
	}

	return a.executeSkillMode(ctx, req, best.Skill)
}

// Run 执行 Agent（主要入口）
func (a *Agent) Run(ctx context.Context, prompt string) (*Result, error) {
	a.SetRunning(true)
	defer a.SetRunning(false)

	a.ClearErrors()
	a.state.CurrentStep = 0

	req := a.buildRequest(prompt, nil, true, nil, "", nil)

	routes := a.skillRouter.Route(ctx, prompt)
	return a.runWithPreparedRoutes(ctx, req, routes)
}

// RunWithHistory 带历史记录执行
func (a *Agent) RunWithHistory(ctx context.Context, prompt string, history []types.Message) (*Result, error) {
	a.SetRunning(true)
	defer a.SetRunning(false)

	a.ClearErrors()
	a.state.CurrentStep = 0

	req := a.buildRequest(prompt, history, false, nil, "", nil)

	routes := a.skillRouter.Route(ctx, prompt)
	return a.runWithPreparedRoutes(ctx, req, routes)
}

// RunWithHistoryAndContext 带历史与上下文执行
func (a *Agent) RunWithHistoryAndContext(ctx context.Context, prompt string, history []types.Message, contextValues map[string]interface{}) (*Result, error) {
	a.SetRunning(true)
	defer a.SetRunning(false)

	a.ClearErrors()
	a.state.CurrentStep = 0

	req := a.buildRequest(prompt, history, false, contextValues, "", nil)

	routes := a.skillRouter.Route(ctx, prompt)
	return a.runWithPreparedRoutes(ctx, req, routes)
}

// executeSkillMode 执行 Skill 模式
func (a *Agent) executeSkillMode(ctx context.Context, req *types.Request, s *skill.Skill) (*Result, error) {
	skillResult, err := a.skillExec.Execute(ctx, s, req)
	if err != nil {
		return &Result{
			Success:  false,
			Error:    err.Error(),
			Skill:    s.Name,
			Duration: req.Duration,
		}, err
	}

	// 记录到记忆
	if a.config.EnableMemory {
		for _, obs := range skillResult.Observations {
			a.memory.Add(obs)
		}
	}

	// 添加助手消息到历史
	if skillResult.Success {
		assistantMsg := types.NewAssistantMessage(skillResult.Output)
		req.AddToHistory(*assistantMsg)
	}

	return &Result{
		Success:      skillResult.Success,
		Output:       skillResult.Output,
		Observations: skillResult.Observations,
		Skill:        skillResult.SkillName,
		Usage:        skillResult.Usage,
		Duration:     req.Duration,
	}, nil
}

// runDefaultMode 默认执行模式（无 Skill 匹配时）
func (a *Agent) RunDefaultMode(ctx context.Context, req *types.Request) (*Result, error) {
	// 简化实现：返回提示信息
	return &Result{
		Success:  false,
		Output:   "No matching skill found for the request",
		Duration: req.Duration,
	}, nil
}

// Reset 重置 Agent 状态
func (a *Agent) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.running = false
	a.state = AgentState{
		Running: false,
		Errors:  make([]string, 0),
	}

	if a.memory != nil {
		a.memory.Clear()
	}
}

// Validate 验证 Agent 配置
func (a *Agent) Validate() error {
	if a.config.Name == "" {
		return errors.New(errors.ErrValidationFailed, "agent name is required")
	}

	if a.config.Model == "" {
		return errors.New(errors.ErrValidationFailed, "model is required")
	}

	if a.config.MaxSteps < 1 {
		return errors.New(errors.ErrValidationFailed, "maxSteps must be >= 1")
	}

	return nil
}

// RunReAct 使用 ReAct 循环运行 Agent
func (a *Agent) RunReAct(ctx context.Context, llmRuntime *llm.LLMRuntime, prompt string) (*Result, error) {
	// 创建 ReAct 循环配置
	loopConfig := &LoopReActConfig{
		MaxSteps:        a.config.MaxSteps,
		EnableThought:   true,
		EnableToolCalls: true,
		Temperature:     a.config.Temperature,
	}

	// 创建 ReAct 循环
	reactLoop := NewReActLoop(a, llmRuntime, loopConfig)

	// 运行循环
	return reactLoop.Run(ctx, prompt)
}

// RunReActWithConfig 使用自定义配置运行 ReAct Agent
func (a *Agent) RunReActWithConfig(ctx context.Context, llmRuntime *llm.LLMRuntime, prompt string, loopConfig *LoopReActConfig) (*Result, error) {
	if loopConfig == nil {
		loopConfig = &LoopReActConfig{
			MaxSteps:        a.config.MaxSteps,
			EnableThought:   true,
			EnableToolCalls: true,
			Temperature:     a.config.Temperature,
		}
	}

	reactLoop := NewReActLoop(a, llmRuntime, loopConfig)
	return reactLoop.Run(ctx, prompt)
}

// RunReActWithSession 使用现有 session 历史运行 ReAct，并将过程回写到 session。
func (a *Agent) RunReActWithSession(ctx context.Context, llmRuntime *llm.LLMRuntime, prompt string, session HistorySession, loopConfig *LoopReActConfig) (*Result, error) {
	if loopConfig == nil {
		loopConfig = &LoopReActConfig{
			MaxSteps:        a.config.MaxSteps,
			EnableThought:   true,
			EnableToolCalls: true,
			Temperature:     a.config.Temperature,
		}
	}

	reactLoop := NewReActLoop(a, llmRuntime, loopConfig)
	return reactLoop.RunWithSession(ctx, prompt, session)
}

func newArtifactStore(cfg *Config) *artifact.Store {
	storeCfg := &artifact.StoreConfig{}
	if cfg != nil {
		if cfg.ArtifactStorePath != "" {
			storeCfg.Path = cfg.ArtifactStorePath
		}
		if cfg.Options != nil {
			if path, ok := cfg.Options["artifact_store_path"].(string); ok && path != "" {
				storeCfg.Path = path
			}
			if dsn, ok := cfg.Options["artifact_store_dsn"].(string); ok && dsn != "" {
				storeCfg.DSN = dsn
			}
		}
	}

	store, err := artifact.NewStore(storeCfg)
	if err != nil {
		return nil
	}

	return store
}

func newDefaultOutputGateway(cfg *Config, store *artifact.Store) *output.Gateway {
	return output.NewGateway(store)
}

func newDefaultContextManager(cfg *Config, store *artifact.Store) *contextmgr.Manager {
	profile := ""
	overrides := contextmgr.Budget{}
	strategy := contextmgr.Strategy{}
	if cfg != nil && cfg.Options != nil {
		if value, ok := cfg.Options["context_profile"].(string); ok {
			profile = value
		}
		if value, ok := cfg.Options["context_compaction_mode"].(string); ok && value != "" {
			strategy.CompactionMode = value
		}
		if value, ok := cfg.Options["context_recall_mode"].(string); ok && value != "" {
			strategy.RecallMode = value
		}
		if value, ok := cfg.Options["context_observation_mode"].(string); ok && value != "" {
			strategy.ObservationMode = value
		}
		if value, ok := contextOptionInt(cfg.Options, "context_min_compaction_messages"); ok {
			strategy.MinCompactionMessages = value
		}
		if value, ok := contextOptionInt(cfg.Options, "context_min_recall_query_length"); ok {
			strategy.MinRecallQueryLength = value
		}
		if value, ok := contextOptionInt(cfg.Options, "context_ledger_load_limit"); ok {
			strategy.LedgerLoadLimit = value
		}
		if value, ok := contextOptionInt(cfg.Options, "context_max_prompt_tokens"); ok {
			overrides.MaxPromptTokens = value
		}
		if value, ok := contextOptionInt(cfg.Options, "context_max_messages"); ok {
			overrides.MaxMessages = value
		}
		if value, ok := contextOptionInt(cfg.Options, "context_keep_recent_messages"); ok {
			overrides.KeepRecentMessages = value
		}
		if value, ok := contextOptionInt(cfg.Options, "context_max_recall_results"); ok {
			overrides.MaxRecallResults = value
		}
		if value, ok := contextOptionInt(cfg.Options, "context_max_observation_items"); ok {
			overrides.MaxObservationItems = value
		}
	}
	budget := contextmgr.ResolveBudget(profile, overrides)
	manager := contextmgr.NewManagerWithProfile(profile, budget, store)
	if manager != nil {
		manager.Strategy = contextmgr.ResolveStrategy(profile, strategy)
		attachWorkspaceContext(manager, cfg)
	}
	return manager
}

func attachWorkspaceContext(manager *contextmgr.Manager, cfg *Config) {
	if manager == nil || cfg == nil || cfg.Options == nil {
		return
	}
	path, wsCfg := workspaceOptions(cfg.Options)
	if path == "" {
		return
	}
	scanner := workspace.NewScanner(wsCfg)
	scan, err := scanner.Scan(path)
	if err != nil {
		return
	}
	manager.Workspace = workspace.NewContextBuilder(scan, nil)
}

func contextOptionInt(options map[string]interface{}, key string) (int, bool) {
	if len(options) == 0 {
		return 0, false
	}
	switch value := options[key].(type) {
	case int:
		return value, value > 0
	case int32:
		return int(value), value > 0
	case int64:
		return int(value), value > 0
	case float64:
		return int(value), value > 0
	default:
		return 0, false
	}
}

func workspaceOptions(options map[string]interface{}) (string, *workspace.WorkspaceConfig) {
	if len(options) == 0 {
		return "", nil
	}
	rawPath, _ := options["workspace_path"].(string)
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", nil
	}
	cfg := workspace.DefaultWorkspaceConfig()
	if value, ok := optionInt64(options, "workspace_max_file_size"); ok && value > 0 {
		cfg.MaxFileSize = value
	}
	if value, ok := contextOptionInt(options, "workspace_max_chunk_size"); ok && value > 0 {
		cfg.MaxChunkSize = value
	}
	if value, ok := contextOptionInt(options, "workspace_chunk_overlap"); ok && value >= 0 {
		cfg.ChunkOverlap = value
	}
	if includes := optionStringSlice(options, "workspace_include"); len(includes) > 0 {
		cfg.IncludePatterns = includes
	}
	if excludes := optionStringSlice(options, "workspace_exclude"); len(excludes) > 0 {
		cfg.ExcludePatterns = excludes
	}
	return path, cfg
}

func optionInt64(options map[string]interface{}, key string) (int64, bool) {
	if len(options) == 0 {
		return 0, false
	}
	switch value := options[key].(type) {
	case int64:
		return value, value > 0
	case int:
		return int64(value), value > 0
	case int32:
		return int64(value), value > 0
	case float64:
		return int64(value), value > 0
	default:
		return 0, false
	}
}

func optionStringSlice(options map[string]interface{}, key string) []string {
	if len(options) == 0 {
		return nil
	}
	raw, ok := options[key]
	if !ok || raw == nil {
		return nil
	}
	switch typed := raw.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				continue
			}
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			out = append(out, text)
		}
		return out
	default:
		return nil
	}
}

func newDefaultToolCatalog(mcpManager skill.MCPManager) *mcpcatalog.Catalog {
	return mcpcatalog.New()
}
