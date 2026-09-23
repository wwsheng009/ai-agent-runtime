package agentconfig

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 会话级 Agent 路由解析（方案文档
// docs/plan/session-scoped-agent-routing-management-plan-20260922.md §3/§4.1）。
//
// 本文件是纯函数解析器：五层逐字段合并（请求 > 会话 > 工作区 > 全局配置 >
// 内置默认），校验失败时按「覆盖层由近及远」逐字段回退（最多 3 轮），绝不
// 原地改写传入的 *Config（发生任何覆盖合并时先深拷贝）。
//
// 快路径（M8，REG 前提）：四层都没有任何生效字段时，直接返回
// EffectiveMainAgentRoutingConfig(cfg) 的同一指针；调用方可用
// res.Effective == EffectiveMainAgentRoutingConfig(cfg) 判定「零覆盖」。

// RoutingSource 标识解析结果的来源层。取值与 commands 包的
// chatPreferenceSource 对齐（session/workspace/config/default），并新增
// derived（推导）与 request（请求级，仅本 turn）。
type RoutingSource string

const (
	// RoutingSourceRequest 是请求级覆盖（仅本 turn，不落盘）。
	RoutingSourceRequest RoutingSource = "request"
	// RoutingSourceSession 是会话覆盖（Session.Metadata.Context）。
	RoutingSourceSession RoutingSource = "session"
	// RoutingSourceWorkspace 是工作区偏好（chat-prefs.yaml 的 routing 节）。
	RoutingSourceWorkspace RoutingSource = "workspace"
	// RoutingSourceConfig 是全局配置层（aicli.main_agent.routing 等）。
	RoutingSourceConfig RoutingSource = "config"
	// RoutingSourceDefault 是内置默认值。
	RoutingSourceDefault RoutingSource = "default"
	// RoutingSourceDerived 是开启推导（§3.5.3）产生的值。
	RoutingSourceDerived RoutingSource = "derived"
)

// SessionRoutingOverrideContextKey 是会话覆盖在 Session.Metadata.Context 中的键名（§3.4）。
const SessionRoutingOverrideContextKey = "aicli_routing_override"

// RoutingWarning 记录被丢弃字段/回退原因（INV-A6，§3.1）。
type RoutingWarning struct {
	Field      string        // 如 main_agent.profiles.easy.model
	Reason     string        // 如 "not in levels"、"allow_expert requires finite max_consecutive_expensive_steps"
	FallbackTo RoutingSource // 回退后的最终来源
}

// RoutingResolution 是解析结果；恒非 nil（B5）。关闭态 Effective 为 nil，
// 其余字段仍填充 Sources/Warnings/Disabled 语义所需信息。
type RoutingResolution struct {
	Effective    *AICLIMainAgentRoutingConfig // 可为 nil（关闭/未配置）
	Sources      map[string]RoutingSource     // 逐字段来源，键见 §3.5.1 键名表（前缀 main_agent.）
	Warnings     []RoutingWarning             // 被丢弃字段/回退原因
	EffectiveSub *AICLISubagentRoutingConfig  // 子 Agent 侧
	SubSources   map[string]RoutingSource     // 子 Agent 逐字段来源（前缀 sub_agent.）
}

// AICLISessionRoutingOverride 是会话级覆盖（JSON 字符串持久化，§3.2）。
// 所有标量字段使用指针：nil=继承，非 nil=显式覆盖（含显式 false/0）。
type AICLISessionRoutingOverride struct {
	MainAgent *AICLISessionMainAgentRoutingOverride `json:"main_agent,omitempty"`
	SubAgent  *AICLISessionSubAgentRoutingOverride  `json:"sub_agent,omitempty"`
	UpdatedAt time.Time                             `json:"updated_at,omitempty"`
	UpdatedBy string                                `json:"updated_by,omitempty"` // aicli-tui | web | api | config
}

// AICLISessionMainAgentRoutingOverride 镜像真实 AICLIMainAgentRoutingConfig
// 的可用子集（不存在的字段一律不得出现）。
type AICLISessionMainAgentRoutingOverride struct {
	Enabled                      *bool                                       `json:"enabled,omitempty"`
	Levels                       *[]string                                   `json:"levels,omitempty"`
	AllowExpert                  *bool                                       `json:"allow_expert,omitempty"`
	DefaultDifficulty            *string                                     `json:"default_difficulty,omitempty"`
	CostGuardMode                *string                                     `json:"cost_guard_mode,omitempty"` // 仅 soft|hard（M1）
	MaxConsecutiveExpensiveSteps *int                                        `json:"max_consecutive_expensive_steps,omitempty"`
	ExpensiveLevels              *[]string                                   `json:"expensive_levels,omitempty"`
	MaxInvalidReportsPerTurn     *int                                        `json:"max_invalid_reports_per_turn,omitempty"`
	DowngradeConfirmSteps        *int                                        `json:"downgrade_confirm_steps,omitempty"`
	MinDwellSteps                *int                                        `json:"min_dwell_steps,omitempty"`
	RespectProviderHealth        *bool                                       `json:"respect_provider_health,omitempty"`
	Profiles                     map[string]AICLISessionRouteProfileOverride `json:"profiles,omitempty"`
}

// AICLISessionSubAgentRoutingOverride 镜像真实 AICLISubagentRoutingConfig 的可用子集。
type AICLISessionSubAgentRoutingOverride struct {
	Enabled           *bool                                       `json:"enabled,omitempty"`
	DefaultDifficulty *string                                     `json:"default_difficulty,omitempty"`
	Levels            map[string]AICLISessionRouteProfileOverride `json:"levels,omitempty"`
}

// AICLISessionRouteProfileOverride 是 profile 字段级覆盖：nil=继承配置层同名字段（INV-A7）。
type AICLISessionRouteProfileOverride struct {
	Provider           *string                        `json:"provider,omitempty"`
	Model              *string                        `json:"model,omitempty"`
	ReasoningEffort    *string                        `json:"reasoning_effort,omitempty"`
	ThinkingEffort     *string                        `json:"thinking_effort,omitempty"`
	MaxTokens          *int                           `json:"max_tokens,omitempty"`
	Timeout            *string                        `json:"timeout,omitempty"` // duration 字符串（如 "90s"）
	Temperature        *float64                       `json:"temperature,omitempty"`
	Availability       *string                        `json:"availability,omitempty"`
	AvailabilityReason *string                        `json:"availability_reason,omitempty"`
	PromptCache        *bool                          `json:"prompt_cache,omitempty"`
	Candidates         *[]AICLISubagentRouteCandidate `json:"candidates,omitempty"` // 整链替换
}

// AICLIRequestRoutingOverride 是请求级覆盖（仅本 turn、不落盘、优先级最高）。
type AICLIRequestRoutingOverride struct {
	MainAgent *AICLISessionMainAgentRoutingOverride `json:"main_agent,omitempty"`
	SubAgent  *AICLISessionSubAgentRoutingOverride  `json:"sub_agent,omitempty"`
}

// AICLIWorkspaceRoutingPreferences 是工作区偏好文件（chat-prefs.yaml）的 routing 节（§3.3）。
// 注意：全局配置中的 aicli.chat.routing 不参与解析——解析器只读
// aicli.main_agent.routing / aicli.subagents.routing。
type AICLIWorkspaceRoutingPreferences struct {
	MainAgent *AICLIMainAgentRoutingConfig `yaml:"main_agent,omitempty" mapstructure:"main_agent"`
	SubAgent  *AICLISubagentRoutingConfig  `yaml:"sub_agent,omitempty" mapstructure:"sub_agent"`
}

// DecodeSessionRoutingOverride 解码会话覆盖 JSON 字符串（M9）。
// 空字符串返回 (nil, nil)——调用方按「无覆盖」处理。
func DecodeSessionRoutingOverride(raw string) (*AICLISessionRoutingOverride, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var override AICLISessionRoutingOverride
	if err := json.Unmarshal([]byte(trimmed), &override); err != nil {
		return nil, fmt.Errorf("decode session routing override: %w", err)
	}
	return &override, nil
}

// EncodeSessionRoutingOverride 编码会话覆盖为 JSON 字符串。
func EncodeSessionRoutingOverride(override *AICLISessionRoutingOverride) (string, error) {
	if override == nil {
		return "", nil
	}
	raw, err := json.Marshal(override)
	if err != nil {
		return "", fmt.Errorf("encode session routing override: %w", err)
	}
	return string(raw), nil
}

// HasMainAgentFields 报告会话覆盖是否含任何主 Agent 生效字段。
func (o *AICLISessionRoutingOverride) HasMainAgentFields() bool {
	return o != nil && mainOverridePresent(o.MainAgent)
}

// HasSubAgentFields 报告会话覆盖是否含任何子 Agent 生效字段。
func (o *AICLISessionRoutingOverride) HasSubAgentFields() bool {
	return o != nil && subOverridePresent(o.SubAgent)
}

// HasRoutingFields 报告工作区偏好是否含任何生效字段。
func (p *AICLIWorkspaceRoutingPreferences) HasRoutingFields() bool {
	if p == nil {
		return false
	}
	return workspaceMainPresent(p.MainAgent) || workspaceSubPresent(p.SubAgent)
}

// LoadWorkspaceRoutingPreferences 读取当前 cwd 工作区偏好文件的 routing 子树
// （方案 §3.3）。文件缺失返回 (nil, nil)。
func LoadWorkspaceRoutingPreferences() (*AICLIWorkspaceRoutingPreferences, error) {
	return loadWorkspaceRoutingPreferencesAt(WorkspacePrefsPath())
}

// LoadWorkspaceRoutingPreferencesForPath 读取指定工作区路径的 routing 子树
// （§3.3/N9：以会话绑定 workspace 路径为准）。文件缺失返回 (nil, nil)。
func LoadWorkspaceRoutingPreferencesForPath(workspacePath string) (*AICLIWorkspaceRoutingPreferences, error) {
	return loadWorkspaceRoutingPreferencesAt(WorkspacePrefsPathForPath(workspacePath))
}

func loadWorkspaceRoutingPreferencesAt(path string) (*AICLIWorkspaceRoutingPreferences, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	chatConfig, err := loadWorkspaceChatPreferencesAt(path)
	if err != nil {
		return nil, err
	}
	if chatConfig == nil {
		return nil, nil
	}
	return chatConfig.Routing, nil
}

func mainOverridePresent(o *AICLISessionMainAgentRoutingOverride) bool {
	if o == nil {
		return false
	}
	if o.Enabled != nil || o.Levels != nil || o.AllowExpert != nil || o.DefaultDifficulty != nil ||
		o.CostGuardMode != nil || o.MaxConsecutiveExpensiveSteps != nil || o.ExpensiveLevels != nil ||
		o.MaxInvalidReportsPerTurn != nil || o.DowngradeConfirmSteps != nil || o.MinDwellSteps != nil ||
		o.RespectProviderHealth != nil {
		return true
	}
	return len(o.Profiles) > 0
}

func subOverridePresent(o *AICLISessionSubAgentRoutingOverride) bool {
	if o == nil {
		return false
	}
	return o.Enabled != nil || o.DefaultDifficulty != nil || len(o.Levels) > 0
}

// workspaceMainPresent 用「非零值=已设置」判定工作区层的生效字段：
// 工作区层是完整结构（非指针），零值无法区分显式 0/false 与未设置，
// 因此与全局配置层同口径——零值视为未设置（§3.5.4 的显式 0 规则只对
// 指针化的会话/请求层生效）。
func workspaceMainPresent(o *AICLIMainAgentRoutingConfig) bool {
	if o == nil {
		return false
	}
	if o.Enabled || len(o.Levels) > 0 || o.AllowExpert || strings.TrimSpace(o.DefaultDifficulty) != "" ||
		strings.TrimSpace(o.CostGuardMode) != "" || o.MaxConsecutiveExpensiveSteps != 0 ||
		len(o.ExpensiveLevels) > 0 || o.MaxInvalidReportsPerTurn != 0 || o.DowngradeConfirmSteps != 0 ||
		o.MinDwellSteps != 0 || o.HealthGate.RespectProviderHealth || len(o.Profiles) > 0 {
		return true
	}
	return false
}

func workspaceSubPresent(o *AICLISubagentRoutingConfig) bool {
	if o == nil {
		return false
	}
	if o.Enabled != nil && *o.Enabled {
		return true
	}
	if strings.TrimSpace(o.DefaultDifficulty) != "" || len(o.Levels) > 0 {
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// 主 Agent 解析

// routingFallbackMaxRounds 是 §3.5.2 阶梯回退的最大轮数。
const routingFallbackMaxRounds = 3

// mainRoutingFieldOrder 是主 Agent 顶层字段的确定性处理顺序（§3.5.1 键名表）。
var mainRoutingFieldOrder = []string{
	"enabled",
	"levels",
	"allow_expert",
	"default_difficulty",
	"cost_guard_mode",
	"max_consecutive_expensive_steps",
	"expensive_levels",
	"max_invalid_reports_per_turn",
	"downgrade_confirm_steps",
	"min_dwell_steps",
	"health_gate.respect_provider_health",
}

// mainProfileFieldOrder 是 profile 字段的确定性处理顺序（§3.5.1 键名表）。
var mainProfileFieldOrder = []string{
	"provider",
	"model",
	"reasoning_effort",
	"thinking_effort",
	"max_tokens",
	"timeout",
	"temperature",
	"availability",
	"availability_reason",
	"prompt_cache",
	"candidates",
}

// ResolveMainAgentRouting 解析主 Agent 路由（五层逐字段，§4.1）。
//
// 快路径：四层都没有任何生效字段时返回 EffectiveMainAgentRoutingConfig(cfg)
// 的同一指针（M8）；否则深拷贝合并，绝不原地改写 cfg。
func ResolveMainAgentRouting(
	cfg *Config,
	sessionOverride *AICLISessionRoutingOverride,
	workspace *AICLIWorkspaceRoutingPreferences,
	request *AICLIRequestRoutingOverride,
) RoutingResolution {
	baseline := EffectiveMainAgentRoutingConfig(cfg)
	res := RoutingResolution{
		Sources:    map[string]RoutingSource{},
		SubSources: map[string]RoutingSource{},
	}

	var requestMain *AICLISessionMainAgentRoutingOverride
	if request != nil {
		requestMain = request.MainAgent
	}
	var sessionMain *AICLISessionMainAgentRoutingOverride
	if sessionOverride != nil {
		sessionMain = sessionOverride.MainAgent
	}
	var workspaceMain *AICLIMainAgentRoutingConfig
	if workspace != nil {
		workspaceMain = workspace.MainAgent
	}

	if !mainOverridePresent(requestMain) && !mainOverridePresent(sessionMain) && !workspaceMainPresent(workspaceMain) {
		res.Effective = baseline
		seedMainSources(baseline, res.Sources)
		return res
	}

	rebuild := func(skip map[string]bool) (*AICLIMainAgentRoutingConfig, map[string]RoutingSource, []RoutingWarning) {
		return rebuildMainRouting(baseline, workspaceMain, sessionMain, requestMain, skip)
	}

	merged, sources, layerWarnings := rebuild(nil)
	validationWarnings, err := ValidateMainAgentRoutingConfig(merged)
	fallbackWarnings := []RoutingWarning{}

	if err != nil {
		order := mainFallbackOrder(workspaceMain, sessionMain, requestMain)
		skip := map[string]bool{}
		for round := 0; round < routingFallbackMaxRounds; round++ {
			field, ok := nextMainFallbackCandidate(order, skip)
			if !ok {
				break
			}
			skip[field] = true
			previousErr := err
			merged, sources, layerWarnings = rebuild(skip)
			validationWarnings, err = ValidateMainAgentRoutingConfig(merged)
			fallbackWarnings = append(fallbackWarnings, RoutingWarning{
				Field:      mainSourceKey(field),
				Reason:     previousErr.Error(),
				FallbackTo: sources[mainSourceKey(field)],
			})
			if err == nil {
				break
			}
		}
	}

	res.Effective = merged
	res.Sources = sources
	res.Warnings = append(res.Warnings, layerWarnings...)
	res.Warnings = append(res.Warnings, fallbackWarnings...)
	res.Warnings = append(res.Warnings, mainValidationWarnings(validationWarnings, baseline)...)
	return res
}

func mainSourceKey(field string) string {
	if strings.HasPrefix(field, "main_agent.") {
		return field
	}
	return "main_agent." + field
}

func mainValidationWarnings(messages []string, baseline *AICLIMainAgentRoutingConfig) []RoutingWarning {
	if len(messages) == 0 {
		return nil
	}
	fallback := RoutingSourceDefault
	if baseline != nil {
		fallback = RoutingSourceConfig
	}
	out := make([]RoutingWarning, 0, len(messages))
	for _, message := range messages {
		out = append(out, RoutingWarning{Field: "main_agent", Reason: message, FallbackTo: fallback})
	}
	return out
}

// rebuildMainRouting 按「配置 → 工作区 → 会话 → 请求」逐字段合并并应用
// 推导与默认值；skip 中的字段被强制回退到下层（阶梯回退用）。
func rebuildMainRouting(
	baseline *AICLIMainAgentRoutingConfig,
	workspaceMain *AICLIMainAgentRoutingConfig,
	sessionMain *AICLISessionMainAgentRoutingOverride,
	requestMain *AICLISessionMainAgentRoutingOverride,
	skip map[string]bool,
) (*AICLIMainAgentRoutingConfig, map[string]RoutingSource, []RoutingWarning) {
	merged := cloneMainAgentRoutingConfig(baseline)
	if merged == nil {
		merged = &AICLIMainAgentRoutingConfig{}
	}
	sources := map[string]RoutingSource{}
	warnings := []RoutingWarning{}

	applyMainWorkspaceLayer(merged, workspaceMain, skip, sources, &warnings)
	applyMainOverrideLayer(merged, sessionMain, RoutingSourceSession, skip, sources, &warnings)
	applyMainOverrideLayer(merged, requestMain, RoutingSourceRequest, skip, sources, &warnings)
	applyMainDerivation(merged, sources, &warnings)
	merged.ApplyMainAgentRoutingDefaults()
	seedMainSources(baseline, sources)
	return merged, sources, warnings
}

func applyMainWorkspaceLayer(
	cfg *AICLIMainAgentRoutingConfig,
	ws *AICLIMainAgentRoutingConfig,
	skip map[string]bool,
	sources map[string]RoutingSource,
	warnings *[]RoutingWarning,
) {
	if ws == nil {
		return
	}
	set := func(field string) bool { return !skip[field] }
	if ws.Enabled && set("enabled") {
		cfg.Enabled = true
		sources["main_agent.enabled"] = RoutingSourceWorkspace
	}
	if len(ws.Levels) > 0 && set("levels") {
		if levels, ok := normalizeRoutingLevels(ws.Levels, "main_agent.levels", warnings); ok {
			cfg.Levels = levels
			sources["main_agent.levels"] = RoutingSourceWorkspace
		}
	}
	if ws.AllowExpert && set("allow_expert") {
		cfg.AllowExpert = true
		sources["main_agent.allow_expert"] = RoutingSourceWorkspace
	}
	if value := strings.TrimSpace(ws.DefaultDifficulty); value != "" && set("default_difficulty") {
		cfg.DefaultDifficulty = value
		sources["main_agent.default_difficulty"] = RoutingSourceWorkspace
	}
	if value := strings.TrimSpace(ws.CostGuardMode); value != "" && set("cost_guard_mode") {
		cfg.CostGuardMode = value
		sources["main_agent.cost_guard_mode"] = RoutingSourceWorkspace
	}
	if ws.MaxConsecutiveExpensiveSteps != 0 && set("max_consecutive_expensive_steps") {
		cfg.MaxConsecutiveExpensiveSteps = ws.MaxConsecutiveExpensiveSteps
		sources["main_agent.max_consecutive_expensive_steps"] = RoutingSourceWorkspace
	}
	if len(ws.ExpensiveLevels) > 0 && set("expensive_levels") {
		if levels, ok := normalizeRoutingLevels(ws.ExpensiveLevels, "main_agent.expensive_levels", warnings); ok {
			cfg.ExpensiveLevels = levels
			sources["main_agent.expensive_levels"] = RoutingSourceWorkspace
		}
	}
	if ws.MaxInvalidReportsPerTurn != 0 && set("max_invalid_reports_per_turn") {
		cfg.MaxInvalidReportsPerTurn = ws.MaxInvalidReportsPerTurn
		sources["main_agent.max_invalid_reports_per_turn"] = RoutingSourceWorkspace
	}
	if ws.DowngradeConfirmSteps != 0 && set("downgrade_confirm_steps") {
		cfg.DowngradeConfirmSteps = ws.DowngradeConfirmSteps
		sources["main_agent.downgrade_confirm_steps"] = RoutingSourceWorkspace
	}
	if ws.MinDwellSteps != 0 && set("min_dwell_steps") {
		cfg.MinDwellSteps = ws.MinDwellSteps
		sources["main_agent.min_dwell_steps"] = RoutingSourceWorkspace
	}
	if ws.HealthGate.RespectProviderHealth && set("health_gate.respect_provider_health") {
		cfg.HealthGate.RespectProviderHealth = true
		sources["main_agent.health_gate.respect_provider_health"] = RoutingSourceWorkspace
	}
	for levelKey, profile := range ws.Profiles {
		level, ok := normalizeSubagentDifficulty(levelKey)
		if !ok {
			*warnings = append(*warnings, RoutingWarning{
				Field:      "main_agent.profiles." + levelKey,
				Reason:     "invalid level key; ignored",
				FallbackTo: RoutingSourceConfig,
			})
			continue
		}
		applyMainWorkspaceProfile(cfg, level, profile, skip, sources)
	}
}

func applyMainWorkspaceProfile(
	cfg *AICLIMainAgentRoutingConfig,
	level string,
	ws AICLISubagentRouteProfile,
	skip map[string]bool,
	sources map[string]RoutingSource,
) {
	profile := cfg.Profiles[level]
	changed := false
	set := func(field string, apply func()) {
		key := mainProfileKey(level, field)
		if skip[key] {
			return
		}
		apply()
		sources[key] = RoutingSourceWorkspace
		changed = true
	}
	if value := strings.TrimSpace(ws.Provider); value != "" {
		set("provider", func() { profile.Provider = value })
	}
	if value := strings.TrimSpace(ws.Model); value != "" {
		set("model", func() { profile.Model = value })
	}
	if value := strings.TrimSpace(ws.ReasoningEffort); value != "" {
		set("reasoning_effort", func() { profile.ReasoningEffort = value })
	}
	if value := strings.TrimSpace(ws.ThinkingEffort); value != "" {
		set("thinking_effort", func() { profile.ThinkingEffort = value })
	}
	if ws.MaxTokens != 0 {
		set("max_tokens", func() { profile.MaxTokens = ws.MaxTokens })
	}
	if ws.Timeout != 0 {
		set("timeout", func() { profile.Timeout = ws.Timeout })
	}
	if ws.Temperature != nil {
		set("temperature", func() { value := *ws.Temperature; profile.Temperature = &value })
	}
	if value := strings.TrimSpace(ws.Availability); value != "" {
		set("availability", func() { profile.Availability = value })
	}
	if value := strings.TrimSpace(ws.AvailabilityReason); value != "" {
		set("availability_reason", func() { profile.AvailabilityReason = value })
	}
	if ws.PromptCache != nil {
		set("prompt_cache", func() { value := *ws.PromptCache; profile.PromptCache = &value })
	}
	if len(ws.Candidates) > 0 {
		set("candidates", func() { profile.Candidates = cloneSubagentRouteCandidates(ws.Candidates) })
	}
	if changed {
		ensureMainProfile(cfg, level, profile)
	}
}

func applyMainOverrideLayer(
	cfg *AICLIMainAgentRoutingConfig,
	override *AICLISessionMainAgentRoutingOverride,
	layer RoutingSource,
	skip map[string]bool,
	sources map[string]RoutingSource,
	warnings *[]RoutingWarning,
) {
	if override == nil {
		return
	}
	set := func(field string) bool { return !skip[field] }
	if override.Enabled != nil && set("enabled") {
		cfg.Enabled = *override.Enabled
		sources["main_agent.enabled"] = layer
	}
	if override.Levels != nil && set("levels") {
		if levels, ok := normalizeRoutingLevels(*override.Levels, "main_agent.levels", warnings); ok {
			cfg.Levels = levels
			sources["main_agent.levels"] = layer
		}
	}
	if override.AllowExpert != nil && set("allow_expert") {
		cfg.AllowExpert = *override.AllowExpert
		sources["main_agent.allow_expert"] = layer
	}
	if override.DefaultDifficulty != nil && set("default_difficulty") {
		cfg.DefaultDifficulty = *override.DefaultDifficulty
		sources["main_agent.default_difficulty"] = layer
	}
	if override.CostGuardMode != nil && set("cost_guard_mode") {
		cfg.CostGuardMode = *override.CostGuardMode
		sources["main_agent.cost_guard_mode"] = layer
	}
	if override.MaxConsecutiveExpensiveSteps != nil && set("max_consecutive_expensive_steps") {
		cfg.MaxConsecutiveExpensiveSteps = *override.MaxConsecutiveExpensiveSteps
		sources["main_agent.max_consecutive_expensive_steps"] = layer
	}
	if override.ExpensiveLevels != nil && set("expensive_levels") {
		if levels, ok := normalizeRoutingLevels(*override.ExpensiveLevels, "main_agent.expensive_levels", warnings); ok {
			cfg.ExpensiveLevels = levels
			sources["main_agent.expensive_levels"] = layer
		}
	}
	if override.MaxInvalidReportsPerTurn != nil && set("max_invalid_reports_per_turn") {
		cfg.MaxInvalidReportsPerTurn = *override.MaxInvalidReportsPerTurn
		sources["main_agent.max_invalid_reports_per_turn"] = layer
	}
	if override.DowngradeConfirmSteps != nil && set("downgrade_confirm_steps") {
		cfg.DowngradeConfirmSteps = *override.DowngradeConfirmSteps
		sources["main_agent.downgrade_confirm_steps"] = layer
	}
	if override.MinDwellSteps != nil && set("min_dwell_steps") {
		cfg.MinDwellSteps = *override.MinDwellSteps
		sources["main_agent.min_dwell_steps"] = layer
	}
	if override.RespectProviderHealth != nil && set("health_gate.respect_provider_health") {
		cfg.HealthGate.RespectProviderHealth = *override.RespectProviderHealth
		sources["main_agent.health_gate.respect_provider_health"] = layer
	}
	for levelKey, profile := range override.Profiles {
		level, ok := normalizeSubagentDifficulty(levelKey)
		if !ok {
			*warnings = append(*warnings, RoutingWarning{
				Field:      "main_agent.profiles." + levelKey,
				Reason:     "invalid level key; ignored",
				FallbackTo: RoutingSourceConfig,
			})
			continue
		}
		applyMainOverrideProfile(cfg, level, profile, layer, skip, sources, warnings)
	}
}

func applyMainOverrideProfile(
	cfg *AICLIMainAgentRoutingConfig,
	level string,
	override AICLISessionRouteProfileOverride,
	layer RoutingSource,
	skip map[string]bool,
	sources map[string]RoutingSource,
	warnings *[]RoutingWarning,
) {
	profile := cfg.Profiles[level]
	changed := false
	assign := func(field string, apply func()) {
		key := mainProfileKey(level, field)
		if skip[key] {
			return
		}
		apply()
		sources[key] = layer
		changed = true
	}
	if override.Provider != nil {
		assign("provider", func() { profile.Provider = strings.TrimSpace(*override.Provider) })
	}
	if override.Model != nil {
		assign("model", func() { profile.Model = strings.TrimSpace(*override.Model) })
	}
	if override.ReasoningEffort != nil {
		assign("reasoning_effort", func() { profile.ReasoningEffort = strings.TrimSpace(*override.ReasoningEffort) })
	}
	if override.ThinkingEffort != nil {
		assign("thinking_effort", func() { profile.ThinkingEffort = strings.TrimSpace(*override.ThinkingEffort) })
	}
	if override.MaxTokens != nil {
		assign("max_tokens", func() { profile.MaxTokens = *override.MaxTokens })
	}
	if override.Timeout != nil {
		key := mainProfileKey(level, "timeout")
		if !skip[key] {
			duration, err := time.ParseDuration(strings.TrimSpace(*override.Timeout))
			if err != nil {
				*warnings = append(*warnings, RoutingWarning{
					Field:      key,
					Reason:     fmt.Sprintf("invalid timeout %q; ignored", *override.Timeout),
					FallbackTo: sources[key],
				})
			} else {
				profile.Timeout = duration
				sources[key] = layer
				changed = true
			}
		}
	}
	if override.Temperature != nil {
		assign("temperature", func() { value := *override.Temperature; profile.Temperature = &value })
	}
	if override.Availability != nil {
		assign("availability", func() { profile.Availability = strings.TrimSpace(*override.Availability) })
	}
	if override.AvailabilityReason != nil {
		assign("availability_reason", func() { profile.AvailabilityReason = strings.TrimSpace(*override.AvailabilityReason) })
	}
	if override.PromptCache != nil {
		assign("prompt_cache", func() { value := *override.PromptCache; profile.PromptCache = &value })
	}
	if override.Candidates != nil {
		assign("candidates", func() { profile.Candidates = cloneSubagentRouteCandidates(*override.Candidates) })
	}
	if changed {
		ensureMainProfile(cfg, level, profile)
	}
}

func mainProfileKey(level, field string) string {
	return fmt.Sprintf("main_agent.profiles.%s.%s", level, field)
}

func ensureMainProfile(cfg *AICLIMainAgentRoutingConfig, level string, profile AICLISubagentRouteProfile) {
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]AICLISubagentRouteProfile{}
	}
	cfg.Profiles[level] = profile
}

// applyMainDerivation 实现 §3.5.3 开启推导：enabled=true 且任何层都未显式
// 设置 levels 时推导档位集合；expert 仅在 allow_expert=true 且
// max_consecutive_expensive_steps 为有限值时纳入。
func applyMainDerivation(cfg *AICLIMainAgentRoutingConfig, sources map[string]RoutingSource, warnings *[]RoutingWarning) {
	if cfg == nil || !cfg.Enabled {
		return
	}
	if len(cfg.Levels) == 0 {
		levels := []string{"easy", "normal", "hard"}
		if cfg.AllowExpert {
			if cfg.MaxConsecutiveExpensiveSteps != 0 {
				levels = append(levels, "expert")
			} else {
				*warnings = append(*warnings, RoutingWarning{
					Field:      "main_agent.levels",
					Reason:     "expert not derived: allow_expert requires a finite max_consecutive_expensive_steps",
					FallbackTo: RoutingSourceDerived,
				})
			}
		}
		cfg.Levels = levels
		sources["main_agent.levels"] = RoutingSourceDerived
	}
	if strings.TrimSpace(cfg.DefaultDifficulty) == "" {
		cfg.DefaultDifficulty = MainAgentDefaultDifficulty
		sources["main_agent.default_difficulty"] = RoutingSourceDerived
	}
}

// ---------------------------------------------------------------------------
// 深拷贝与规范化辅助

// cloneMainAgentRoutingConfig 深拷贝主 Agent 路由配置（INV-A5：绝不原地改写 cfg）。
func cloneMainAgentRoutingConfig(base *AICLIMainAgentRoutingConfig) *AICLIMainAgentRoutingConfig {
	if base == nil {
		return nil
	}
	clone := *base
	clone.Levels = append([]string(nil), base.Levels...)
	clone.ExpensiveLevels = append([]string(nil), base.ExpensiveLevels...)
	if base.Profiles != nil {
		clone.Profiles = make(map[string]AICLISubagentRouteProfile, len(base.Profiles))
		for level, profile := range base.Profiles {
			clone.Profiles[level] = cloneSubagentRouteProfile(profile)
		}
	}
	return &clone
}

func cloneSubagentRouteProfile(profile AICLISubagentRouteProfile) AICLISubagentRouteProfile {
	clone := profile
	if profile.Temperature != nil {
		value := *profile.Temperature
		clone.Temperature = &value
	}
	if profile.PromptCache != nil {
		value := *profile.PromptCache
		clone.PromptCache = &value
	}
	clone.Candidates = cloneSubagentRouteCandidates(profile.Candidates)
	return clone
}

func cloneSubagentRouteCandidates(in []AICLISubagentRouteCandidate) []AICLISubagentRouteCandidate {
	if len(in) == 0 {
		return nil
	}
	out := make([]AICLISubagentRouteCandidate, 0, len(in))
	for _, candidate := range in {
		clone := candidate
		if candidate.PromptCache != nil {
			value := *candidate.PromptCache
			clone.PromptCache = &value
		}
		out = append(out, clone)
	}
	return out
}

// normalizeRoutingLevels 归一化档位列表并去重；全部非法时返回 ok=false，
// 调用方应保持下层取值（INV-A3：非法值不生效）。
func normalizeRoutingLevels(raw []string, label string, warnings *[]RoutingWarning) ([]string, bool) {
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, entry := range raw {
		level, ok := normalizeSubagentDifficulty(entry)
		if !ok {
			*warnings = append(*warnings, RoutingWarning{
				Field:      label,
				Reason:     fmt.Sprintf("invalid level %q; ignored", entry),
				FallbackTo: RoutingSourceConfig,
			})
			continue
		}
		if seen[level] {
			continue
		}
		seen[level] = true
		out = append(out, level)
	}
	if len(out) == 0 && len(raw) > 0 {
		return nil, false
	}
	return out, true
}

// seedMainSources 为未被任何覆盖层设置的字段补 config/default 来源（§3.5.1）。
func seedMainSources(base *AICLIMainAgentRoutingConfig, sources map[string]RoutingSource) {
	put := func(field string, explicit bool) {
		key := "main_agent." + field
		if _, ok := sources[key]; ok {
			return
		}
		if explicit {
			sources[key] = RoutingSourceConfig
		} else {
			sources[key] = RoutingSourceDefault
		}
	}
	if base == nil {
		for _, field := range mainRoutingFieldOrder {
			put(field, false)
		}
		return
	}
	put("enabled", true)
	put("levels", len(base.Levels) > 0)
	put("allow_expert", base.AllowExpert)
	put("default_difficulty", strings.TrimSpace(base.DefaultDifficulty) != "")
	put("cost_guard_mode", strings.TrimSpace(base.CostGuardMode) != "")
	put("max_consecutive_expensive_steps", base.MaxConsecutiveExpensiveSteps != 0)
	put("expensive_levels", len(base.ExpensiveLevels) > 0)
	put("max_invalid_reports_per_turn", base.MaxInvalidReportsPerTurn != 0)
	put("downgrade_confirm_steps", base.DowngradeConfirmSteps != 0)
	put("min_dwell_steps", base.MinDwellSteps != 0)
	put("health_gate.respect_provider_health", base.HealthGate.RespectProviderHealth)
	for level, profile := range base.Profiles {
		prefix := "main_agent.profiles." + level + "."
		putProfile := func(field string, explicit bool) {
			key := prefix + field
			if _, ok := sources[key]; ok {
				return
			}
			if explicit {
				sources[key] = RoutingSourceConfig
			} else {
				sources[key] = RoutingSourceDefault
			}
		}
		putProfile("provider", strings.TrimSpace(profile.Provider) != "")
		putProfile("model", strings.TrimSpace(profile.Model) != "")
		putProfile("reasoning_effort", strings.TrimSpace(profile.ReasoningEffort) != "")
		putProfile("thinking_effort", strings.TrimSpace(profile.ThinkingEffort) != "")
		putProfile("max_tokens", profile.MaxTokens != 0)
		putProfile("timeout", profile.Timeout != 0)
		putProfile("temperature", profile.Temperature != nil)
		putProfile("availability", strings.TrimSpace(profile.Availability) != "")
		putProfile("availability_reason", strings.TrimSpace(profile.AvailabilityReason) != "")
		putProfile("prompt_cache", profile.PromptCache != nil)
		putProfile("candidates", len(profile.Candidates) > 0)
	}
}

// ---------------------------------------------------------------------------
// 阶梯回退顺序（§3.5.2：覆盖层由近及远逐字段回退）

// mainFallbackOrder 产出「可回退字段」的确定性顺序：
// 顶层字段按 mainRoutingFieldOrder，profile 字段按档位名升序 + 字段固定序。
// 只包含至少被一个覆盖层显式设置的字段。
func mainFallbackOrder(
	workspaceMain *AICLIMainAgentRoutingConfig,
	sessionMain *AICLISessionMainAgentRoutingOverride,
	requestMain *AICLISessionMainAgentRoutingOverride,
) []string {
	seen := map[string]bool{}
	order := make([]string, 0, len(mainRoutingFieldOrder))
	add := func(field string) {
		if seen[field] {
			return
		}
		seen[field] = true
		order = append(order, field)
	}
	for _, field := range mainRoutingFieldOrder {
		if workspaceMainTopFieldPresent(workspaceMain, field) ||
			overrideTopFieldPresent(sessionMain, field) ||
			overrideTopFieldPresent(requestMain, field) {
			add(field)
		}
	}

	wsProfiles := normalizeWorkspaceMainProfiles(workspaceMain)
	sessProfiles := normalizeOverrideMainProfiles(sessionMain)
	reqProfiles := normalizeOverrideMainProfiles(requestMain)
	levelSet := map[string]bool{}
	for level := range wsProfiles {
		levelSet[level] = true
	}
	for level := range sessProfiles {
		levelSet[level] = true
	}
	for level := range reqProfiles {
		levelSet[level] = true
	}
	levels := make([]string, 0, len(levelSet))
	for level := range levelSet {
		levels = append(levels, level)
	}
	sort.Strings(levels)
	for _, level := range levels {
		for _, field := range mainProfileFieldOrder {
			if workspaceProfileFieldPresent(wsProfiles, level, field) ||
				overrideProfileFieldPresent(sessProfiles, level, field) ||
				overrideProfileFieldPresent(reqProfiles, level, field) {
				add("profiles." + level + "." + field)
			}
		}
	}
	return order
}

func nextMainFallbackCandidate(order []string, skip map[string]bool) (string, bool) {
	for _, field := range order {
		if skip[field] {
			continue
		}
		return field, true
	}
	return "", false
}

func normalizeWorkspaceMainProfiles(ws *AICLIMainAgentRoutingConfig) map[string]AICLISubagentRouteProfile {
	out := map[string]AICLISubagentRouteProfile{}
	if ws == nil {
		return out
	}
	for key, profile := range ws.Profiles {
		if level, ok := normalizeSubagentDifficulty(key); ok {
			out[level] = profile
		}
	}
	return out
}

func normalizeOverrideMainProfiles(o *AICLISessionMainAgentRoutingOverride) map[string]AICLISessionRouteProfileOverride {
	out := map[string]AICLISessionRouteProfileOverride{}
	if o == nil {
		return out
	}
	for key, profile := range o.Profiles {
		if level, ok := normalizeSubagentDifficulty(key); ok {
			out[level] = profile
		}
	}
	return out
}

func workspaceMainTopFieldPresent(ws *AICLIMainAgentRoutingConfig, field string) bool {
	if ws == nil {
		return false
	}
	switch field {
	case "enabled":
		return ws.Enabled
	case "levels":
		return len(ws.Levels) > 0
	case "allow_expert":
		return ws.AllowExpert
	case "default_difficulty":
		return strings.TrimSpace(ws.DefaultDifficulty) != ""
	case "cost_guard_mode":
		return strings.TrimSpace(ws.CostGuardMode) != ""
	case "max_consecutive_expensive_steps":
		return ws.MaxConsecutiveExpensiveSteps != 0
	case "expensive_levels":
		return len(ws.ExpensiveLevels) > 0
	case "max_invalid_reports_per_turn":
		return ws.MaxInvalidReportsPerTurn != 0
	case "downgrade_confirm_steps":
		return ws.DowngradeConfirmSteps != 0
	case "min_dwell_steps":
		return ws.MinDwellSteps != 0
	case "health_gate.respect_provider_health":
		return ws.HealthGate.RespectProviderHealth
	}
	return false
}

func overrideTopFieldPresent(o *AICLISessionMainAgentRoutingOverride, field string) bool {
	if o == nil {
		return false
	}
	switch field {
	case "enabled":
		return o.Enabled != nil
	case "levels":
		return o.Levels != nil
	case "allow_expert":
		return o.AllowExpert != nil
	case "default_difficulty":
		return o.DefaultDifficulty != nil
	case "cost_guard_mode":
		return o.CostGuardMode != nil
	case "max_consecutive_expensive_steps":
		return o.MaxConsecutiveExpensiveSteps != nil
	case "expensive_levels":
		return o.ExpensiveLevels != nil
	case "max_invalid_reports_per_turn":
		return o.MaxInvalidReportsPerTurn != nil
	case "downgrade_confirm_steps":
		return o.DowngradeConfirmSteps != nil
	case "min_dwell_steps":
		return o.MinDwellSteps != nil
	case "health_gate.respect_provider_health":
		return o.RespectProviderHealth != nil
	}
	return false
}

func workspaceProfileFieldPresent(profiles map[string]AICLISubagentRouteProfile, level, field string) bool {
	profile, ok := profiles[level]
	if !ok {
		return false
	}
	switch field {
	case "provider":
		return strings.TrimSpace(profile.Provider) != ""
	case "model":
		return strings.TrimSpace(profile.Model) != ""
	case "reasoning_effort":
		return strings.TrimSpace(profile.ReasoningEffort) != ""
	case "thinking_effort":
		return strings.TrimSpace(profile.ThinkingEffort) != ""
	case "max_tokens":
		return profile.MaxTokens != 0
	case "timeout":
		return profile.Timeout != 0
	case "temperature":
		return profile.Temperature != nil
	case "availability":
		return strings.TrimSpace(profile.Availability) != ""
	case "availability_reason":
		return strings.TrimSpace(profile.AvailabilityReason) != ""
	case "prompt_cache":
		return profile.PromptCache != nil
	case "candidates":
		return len(profile.Candidates) > 0
	}
	return false
}

func overrideProfileFieldPresent(profiles map[string]AICLISessionRouteProfileOverride, level, field string) bool {
	profile, ok := profiles[level]
	if !ok {
		return false
	}
	switch field {
	case "provider":
		return profile.Provider != nil
	case "model":
		return profile.Model != nil
	case "reasoning_effort":
		return profile.ReasoningEffort != nil
	case "thinking_effort":
		return profile.ThinkingEffort != nil
	case "max_tokens":
		return profile.MaxTokens != nil
	case "timeout":
		return profile.Timeout != nil
	case "temperature":
		return profile.Temperature != nil
	case "availability":
		return profile.Availability != nil
	case "availability_reason":
		return profile.AvailabilityReason != nil
	case "prompt_cache":
		return profile.PromptCache != nil
	case "candidates":
		return profile.Candidates != nil
	}
	return false
}

// ---------------------------------------------------------------------------
// 子 Agent 解析

// subagentRoutingBaseline 返回子 Agent 路由的配置层基线；未配置时 nil。
func subagentRoutingBaseline(cfg *Config) *AICLISubagentRoutingConfig {
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.Subagents == nil {
		return nil
	}
	return cfg.AICLI.Subagents.Routing
}

// ResolveSubagentRouting 解析子 Agent 路由（五层逐字段，§4.1）。
//
// 覆盖面：enabled / default_difficulty / levels.<level>.<profile 字段>；
// task_types 与 roles 表不在会话覆盖范围内（v1 只读，见 §3.2 字段白名单）。
func ResolveSubagentRouting(
	cfg *Config,
	sessionOverride *AICLISessionRoutingOverride,
	workspace *AICLIWorkspaceRoutingPreferences,
	request *AICLIRequestRoutingOverride,
) RoutingResolution {
	baseline := subagentRoutingBaseline(cfg)
	res := RoutingResolution{
		Sources:    map[string]RoutingSource{},
		SubSources: map[string]RoutingSource{},
	}

	var requestSub *AICLISessionSubAgentRoutingOverride
	if request != nil {
		requestSub = request.SubAgent
	}
	var sessionSub *AICLISessionSubAgentRoutingOverride
	if sessionOverride != nil {
		sessionSub = sessionOverride.SubAgent
	}
	var workspaceSub *AICLISubagentRoutingConfig
	if workspace != nil {
		workspaceSub = workspace.SubAgent
	}

	if !subOverridePresent(requestSub) && !subOverridePresent(sessionSub) && !workspaceSubPresent(workspaceSub) {
		res.EffectiveSub = baseline
		seedSubSources(baseline, res.SubSources)
		return res
	}

	rebuild := func(skip map[string]bool) (*AICLISubagentRoutingConfig, map[string]RoutingSource, []RoutingWarning) {
		return rebuildSubagentRouting(baseline, workspaceSub, sessionSub, requestSub, skip)
	}

	merged, sources, layerWarnings := rebuild(nil)
	validationWarnings, err := ValidateSubagentRoutingConfig("aicli.subagents.routing", merged)
	fallbackWarnings := []RoutingWarning{}

	if err != nil {
		order := subFallbackOrder(workspaceSub, sessionSub, requestSub)
		skip := map[string]bool{}
		for round := 0; round < routingFallbackMaxRounds; round++ {
			field, ok := nextMainFallbackCandidate(order, skip)
			if !ok {
				break
			}
			skip[field] = true
			previousErr := err
			merged, sources, layerWarnings = rebuild(skip)
			validationWarnings, err = ValidateSubagentRoutingConfig("aicli.subagents.routing", merged)
			fallbackWarnings = append(fallbackWarnings, RoutingWarning{
				Field:      subSourceKey(field),
				Reason:     previousErr.Error(),
				FallbackTo: sources[subSourceKey(field)],
			})
			if err == nil {
				break
			}
		}
	}

	res.EffectiveSub = merged
	res.SubSources = sources
	res.Warnings = append(res.Warnings, layerWarnings...)
	res.Warnings = append(res.Warnings, fallbackWarnings...)
	res.Warnings = append(res.Warnings, subValidationWarnings(validationWarnings, baseline)...)
	return res
}

func subSourceKey(field string) string {
	if strings.HasPrefix(field, "sub_agent.") {
		return field
	}
	return "sub_agent." + field
}

func subValidationWarnings(messages []string, baseline *AICLISubagentRoutingConfig) []RoutingWarning {
	if len(messages) == 0 {
		return nil
	}
	fallback := RoutingSourceDefault
	if baseline != nil {
		fallback = RoutingSourceConfig
	}
	out := make([]RoutingWarning, 0, len(messages))
	for _, message := range messages {
		out = append(out, RoutingWarning{Field: "sub_agent", Reason: message, FallbackTo: fallback})
	}
	return out
}

func cloneSubagentRoutingConfig(base *AICLISubagentRoutingConfig) *AICLISubagentRoutingConfig {
	if base == nil {
		return nil
	}
	clone := *base
	if base.Enabled != nil {
		value := *base.Enabled
		clone.Enabled = &value
	}
	if base.Levels != nil {
		clone.Levels = make(map[string]AICLISubagentRouteProfile, len(base.Levels))
		for level, profile := range base.Levels {
			clone.Levels[level] = cloneSubagentRouteProfile(profile)
		}
	}
	return &clone
}

func rebuildSubagentRouting(
	baseline *AICLISubagentRoutingConfig,
	workspaceSub *AICLISubagentRoutingConfig,
	sessionSub *AICLISessionSubAgentRoutingOverride,
	requestSub *AICLISessionSubAgentRoutingOverride,
	skip map[string]bool,
) (*AICLISubagentRoutingConfig, map[string]RoutingSource, []RoutingWarning) {
	merged := cloneSubagentRoutingConfig(baseline)
	if merged == nil {
		merged = &AICLISubagentRoutingConfig{}
	}
	sources := map[string]RoutingSource{}
	warnings := []RoutingWarning{}

	applySubWorkspaceLayer(merged, workspaceSub, skip, sources)
	applySubOverrideLayer(merged, sessionSub, RoutingSourceSession, skip, sources, &warnings)
	applySubOverrideLayer(merged, requestSub, RoutingSourceRequest, skip, sources, &warnings)
	seedSubSources(baseline, sources)
	return merged, sources, warnings
}

func applySubWorkspaceLayer(
	cfg *AICLISubagentRoutingConfig,
	ws *AICLISubagentRoutingConfig,
	skip map[string]bool,
	sources map[string]RoutingSource,
) {
	if ws == nil {
		return
	}
	// 工作区层只能「打开」子 Agent 路由（false 与未设置同义，无法表达显式关闭）。
	if ws.Enabled != nil && *ws.Enabled && !skip["enabled"] {
		value := true
		cfg.Enabled = &value
		sources["sub_agent.enabled"] = RoutingSourceWorkspace
	}
	if value := strings.TrimSpace(ws.DefaultDifficulty); value != "" && !skip["default_difficulty"] {
		cfg.DefaultDifficulty = value
		sources["sub_agent.default_difficulty"] = RoutingSourceWorkspace
	}
	for levelKey, profile := range ws.Levels {
		level, ok := normalizeSubagentDifficulty(levelKey)
		if !ok {
			continue
		}
		if applySubWorkspaceProfile(cfg, level, profile, skip, sources) {
			ensureSubProfile(cfg, level)
		}
	}
}

func applySubOverrideLayer(
	cfg *AICLISubagentRoutingConfig,
	override *AICLISessionSubAgentRoutingOverride,
	layer RoutingSource,
	skip map[string]bool,
	sources map[string]RoutingSource,
	warnings *[]RoutingWarning,
) {
	if override == nil {
		return
	}
	if override.Enabled != nil && !skip["enabled"] {
		value := *override.Enabled
		cfg.Enabled = &value
		sources["sub_agent.enabled"] = layer
	}
	if override.DefaultDifficulty != nil && !skip["default_difficulty"] {
		cfg.DefaultDifficulty = *override.DefaultDifficulty
		sources["sub_agent.default_difficulty"] = layer
	}
	for levelKey, profile := range override.Levels {
		level, ok := normalizeSubagentDifficulty(levelKey)
		if !ok {
			*warnings = append(*warnings, RoutingWarning{
				Field:      "sub_agent.levels." + levelKey,
				Reason:     "invalid level key; ignored",
				FallbackTo: RoutingSourceConfig,
			})
			continue
		}
		if applySubOverrideProfile(cfg, level, profile, layer, skip, sources, warnings) {
			ensureSubProfile(cfg, level)
		}
	}
}

func subProfileKey(level, field string) string {
	return fmt.Sprintf("sub_agent.levels.%s.%s", level, field)
}

func ensureSubProfile(cfg *AICLISubagentRoutingConfig, level string) {
	if cfg.Levels == nil {
		cfg.Levels = map[string]AICLISubagentRouteProfile{}
	}
	if _, ok := cfg.Levels[level]; !ok {
		cfg.Levels[level] = AICLISubagentRouteProfile{}
	}
}

func applySubWorkspaceProfile(
	cfg *AICLISubagentRoutingConfig,
	level string,
	ws AICLISubagentRouteProfile,
	skip map[string]bool,
	sources map[string]RoutingSource,
) bool {
	profile := cfg.Levels[level]
	changed := false
	set := func(field string, apply func()) {
		key := subProfileKey(level, field)
		if skip[key] {
			return
		}
		apply()
		sources[key] = RoutingSourceWorkspace
		changed = true
	}
	if value := strings.TrimSpace(ws.Provider); value != "" {
		set("provider", func() { profile.Provider = value })
	}
	if value := strings.TrimSpace(ws.Model); value != "" {
		set("model", func() { profile.Model = value })
	}
	if value := strings.TrimSpace(ws.ReasoningEffort); value != "" {
		set("reasoning_effort", func() { profile.ReasoningEffort = value })
	}
	if value := strings.TrimSpace(ws.ThinkingEffort); value != "" {
		set("thinking_effort", func() { profile.ThinkingEffort = value })
	}
	if ws.MaxTokens != 0 {
		set("max_tokens", func() { profile.MaxTokens = ws.MaxTokens })
	}
	if ws.Timeout != 0 {
		set("timeout", func() { profile.Timeout = ws.Timeout })
	}
	if ws.Temperature != nil {
		set("temperature", func() { value := *ws.Temperature; profile.Temperature = &value })
	}
	if value := strings.TrimSpace(ws.Availability); value != "" {
		set("availability", func() { profile.Availability = value })
	}
	if value := strings.TrimSpace(ws.AvailabilityReason); value != "" {
		set("availability_reason", func() { profile.AvailabilityReason = value })
	}
	if ws.PromptCache != nil {
		set("prompt_cache", func() { value := *ws.PromptCache; profile.PromptCache = &value })
	}
	if len(ws.Candidates) > 0 {
		set("candidates", func() { profile.Candidates = cloneSubagentRouteCandidates(ws.Candidates) })
	}
	if changed {
		if cfg.Levels == nil {
			cfg.Levels = map[string]AICLISubagentRouteProfile{}
		}
		cfg.Levels[level] = profile
	}
	return changed
}

func applySubOverrideProfile(
	cfg *AICLISubagentRoutingConfig,
	level string,
	override AICLISessionRouteProfileOverride,
	layer RoutingSource,
	skip map[string]bool,
	sources map[string]RoutingSource,
	warnings *[]RoutingWarning,
) bool {
	profile := cfg.Levels[level]
	changed := false
	assign := func(field string, apply func()) {
		key := subProfileKey(level, field)
		if skip[key] {
			return
		}
		apply()
		sources[key] = layer
		changed = true
	}
	if override.Provider != nil {
		assign("provider", func() { profile.Provider = strings.TrimSpace(*override.Provider) })
	}
	if override.Model != nil {
		assign("model", func() { profile.Model = strings.TrimSpace(*override.Model) })
	}
	if override.ReasoningEffort != nil {
		assign("reasoning_effort", func() { profile.ReasoningEffort = strings.TrimSpace(*override.ReasoningEffort) })
	}
	if override.ThinkingEffort != nil {
		assign("thinking_effort", func() { profile.ThinkingEffort = strings.TrimSpace(*override.ThinkingEffort) })
	}
	if override.MaxTokens != nil {
		assign("max_tokens", func() { profile.MaxTokens = *override.MaxTokens })
	}
	if override.Timeout != nil {
		key := subProfileKey(level, "timeout")
		if !skip[key] {
			duration, err := time.ParseDuration(strings.TrimSpace(*override.Timeout))
			if err != nil {
				*warnings = append(*warnings, RoutingWarning{
					Field:      key,
					Reason:     fmt.Sprintf("invalid timeout %q; ignored", *override.Timeout),
					FallbackTo: sources[key],
				})
			} else {
				profile.Timeout = duration
				sources[key] = layer
				changed = true
			}
		}
	}
	if override.Temperature != nil {
		assign("temperature", func() { value := *override.Temperature; profile.Temperature = &value })
	}
	if override.Availability != nil {
		assign("availability", func() { profile.Availability = strings.TrimSpace(*override.Availability) })
	}
	if override.AvailabilityReason != nil {
		assign("availability_reason", func() { profile.AvailabilityReason = strings.TrimSpace(*override.AvailabilityReason) })
	}
	if override.PromptCache != nil {
		assign("prompt_cache", func() { value := *override.PromptCache; profile.PromptCache = &value })
	}
	if override.Candidates != nil {
		assign("candidates", func() { profile.Candidates = cloneSubagentRouteCandidates(*override.Candidates) })
	}
	if changed {
		if cfg.Levels == nil {
			cfg.Levels = map[string]AICLISubagentRouteProfile{}
		}
		cfg.Levels[level] = profile
	}
	return changed
}

// seedSubSources 为未被覆盖层设置的子 Agent 字段补 config/default 来源。
func seedSubSources(base *AICLISubagentRoutingConfig, sources map[string]RoutingSource) {
	put := func(field string, explicit bool) {
		key := "sub_agent." + field
		if _, ok := sources[key]; ok {
			return
		}
		if explicit {
			sources[key] = RoutingSourceConfig
		} else {
			sources[key] = RoutingSourceDefault
		}
	}
	if base == nil {
		put("enabled", false)
		put("default_difficulty", false)
		return
	}
	put("enabled", base.Enabled != nil)
	put("default_difficulty", strings.TrimSpace(base.DefaultDifficulty) != "")
	for level, profile := range base.Levels {
		prefix := "sub_agent.levels." + level + "."
		putProfile := func(field string, explicit bool) {
			key := prefix + field
			if _, ok := sources[key]; ok {
				return
			}
			if explicit {
				sources[key] = RoutingSourceConfig
			} else {
				sources[key] = RoutingSourceDefault
			}
		}
		putProfile("provider", strings.TrimSpace(profile.Provider) != "")
		putProfile("model", strings.TrimSpace(profile.Model) != "")
		putProfile("reasoning_effort", strings.TrimSpace(profile.ReasoningEffort) != "")
		putProfile("thinking_effort", strings.TrimSpace(profile.ThinkingEffort) != "")
		putProfile("max_tokens", profile.MaxTokens != 0)
		putProfile("timeout", profile.Timeout != 0)
		putProfile("temperature", profile.Temperature != nil)
		putProfile("availability", strings.TrimSpace(profile.Availability) != "")
		putProfile("availability_reason", strings.TrimSpace(profile.AvailabilityReason) != "")
		putProfile("prompt_cache", profile.PromptCache != nil)
		putProfile("candidates", len(profile.Candidates) > 0)
	}
}

// subFallbackOrder 产出子 Agent 可回退字段的确定性顺序。
func subFallbackOrder(
	workspaceSub *AICLISubagentRoutingConfig,
	sessionSub *AICLISessionSubAgentRoutingOverride,
	requestSub *AICLISessionSubAgentRoutingOverride,
) []string {
	seen := map[string]bool{}
	order := make([]string, 0, 2+len(mainProfileFieldOrder))
	add := func(field string) {
		if seen[field] {
			return
		}
		seen[field] = true
		order = append(order, field)
	}
	if (workspaceSub != nil && workspaceSub.Enabled != nil && *workspaceSub.Enabled) ||
		(sessionSub != nil && sessionSub.Enabled != nil) ||
		(requestSub != nil && requestSub.Enabled != nil) {
		add("enabled")
	}
	if (workspaceSub != nil && strings.TrimSpace(workspaceSub.DefaultDifficulty) != "") ||
		(sessionSub != nil && sessionSub.DefaultDifficulty != nil) ||
		(requestSub != nil && requestSub.DefaultDifficulty != nil) {
		add("default_difficulty")
	}

	wsLevels := normalizeSubWorkspaceLevels(workspaceSub)
	sessLevels := normalizeSubOverrideLevels(sessionSub)
	reqLevels := normalizeSubOverrideLevels(requestSub)
	levelSet := map[string]bool{}
	for level := range wsLevels {
		levelSet[level] = true
	}
	for level := range sessLevels {
		levelSet[level] = true
	}
	for level := range reqLevels {
		levelSet[level] = true
	}
	levels := make([]string, 0, len(levelSet))
	for level := range levelSet {
		levels = append(levels, level)
	}
	sort.Strings(levels)
	for _, level := range levels {
		for _, field := range mainProfileFieldOrder {
			if subWorkspaceProfileFieldPresent(wsLevels, level, field) ||
				subOverrideProfileFieldPresent(sessLevels, level, field) ||
				subOverrideProfileFieldPresent(reqLevels, level, field) {
				add("levels." + level + "." + field)
			}
		}
	}
	return order
}

func normalizeSubWorkspaceLevels(ws *AICLISubagentRoutingConfig) map[string]AICLISubagentRouteProfile {
	out := map[string]AICLISubagentRouteProfile{}
	if ws == nil {
		return out
	}
	for key, profile := range ws.Levels {
		if level, ok := normalizeSubagentDifficulty(key); ok {
			out[level] = profile
		}
	}
	return out
}

func normalizeSubOverrideLevels(o *AICLISessionSubAgentRoutingOverride) map[string]AICLISessionRouteProfileOverride {
	out := map[string]AICLISessionRouteProfileOverride{}
	if o == nil {
		return out
	}
	for key, profile := range o.Levels {
		if level, ok := normalizeSubagentDifficulty(key); ok {
			out[level] = profile
		}
	}
	return out
}

func subWorkspaceProfileFieldPresent(levels map[string]AICLISubagentRouteProfile, level, field string) bool {
	profile, ok := levels[level]
	if !ok {
		return false
	}
	return workspaceProfileFieldPresent(map[string]AICLISubagentRouteProfile{level: profile}, level, field)
}

func subOverrideProfileFieldPresent(levels map[string]AICLISessionRouteProfileOverride, level, field string) bool {
	profile, ok := levels[level]
	if !ok {
		return false
	}
	return overrideProfileFieldPresent(map[string]AICLISessionRouteProfileOverride{level: profile}, level, field)
}
