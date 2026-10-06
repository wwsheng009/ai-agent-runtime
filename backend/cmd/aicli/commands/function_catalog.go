package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/internal/capability"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolargs"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

type aicliCatalogEntry struct {
	name       string
	fn         functions.Function
	schema     map[string]interface{}
	descriptor *capability.Descriptor
	isSkill    bool
}

type aicliFunctionCatalogStats = runtimechatcore.CatalogStats
type aicliFunctionSelection = runtimechatcore.FunctionSelection

// aicliFunctionCatalog unifies builtin tools, skill functions, schema caches, and execution.
type aicliFunctionCatalog struct {
	// mu 保护以下全部可变状态：registry 内部 map、entries/entryOrder、以及
	// 惰性解析的 builder/skillsBinding/toolPolicy/workspaceRootResolver。
	// 热加载回调（后台 goroutine）注册函数与请求线程读取函数面并发发生，
	// 所有公共入口都必须先取锁；*Locked 后缀的内部实现假定调用方已持锁，
	// 且不得再调用本 catalog 的公共方法（sync.RWMutex 不可重入）。
	mu            sync.RWMutex
	registry      *functions.FunctionRegistry
	builder       functions.FunctionCallBuilder
	skillsBinding *skillsRuntimeBinding
	toolPolicy    *runtimepolicy.ToolExecutionPolicy
	entries       map[string]*aicliCatalogEntry
	entryOrder    []string
	// workspaceRootResolver lazily resolves the session workspace root used to
	// anchor relative path arguments for the static tool policy. It stays nil
	// for catalogs without a session (unit tests, embedders).
	workspaceRootResolver func() string
}

func newAICLIFunctionCatalog(protocol string, registry *functions.FunctionRegistry) *aicliFunctionCatalog {
	if registry == nil {
		registry = functions.NewFunctionRegistry()
	}
	return &aicliFunctionCatalog{
		registry: registry,
		builder:  functions.GetFunctionCallBuilder(protocol),
		entries:  make(map[string]*aicliCatalogEntry),
	}
}

func ensureFunctionCatalog(session *ChatSession) *aicliFunctionCatalog {
	if session == nil {
		return nil
	}
	if session.FunctionCatalog == nil {
		session.FunctionCatalog = newAICLIFunctionCatalog(session.Provider.GetProtocol(), session.FunctionRegistry)
	}
	catalog := session.FunctionCatalog
	catalog.mu.Lock()
	if catalog.registry == nil {
		if session.FunctionRegistry != nil {
			catalog.registry = session.FunctionRegistry
		} else {
			catalog.registry = functions.NewFunctionRegistry()
		}
	}
	if catalog.builder == nil {
		if session.FunctionBuilder != nil {
			catalog.builder = session.FunctionBuilder
		} else {
			catalog.builder = functions.GetFunctionCallBuilder(session.Provider.GetProtocol())
		}
	}
	if catalog.entries == nil {
		catalog.entries = make(map[string]*aicliCatalogEntry)
	}
	if catalog.skillsBinding == nil && session.SkillsBinding != nil {
		catalog.skillsBinding = session.SkillsBinding
	}
	if catalog.toolPolicy == nil && session.ToolPolicy != nil {
		catalog.toolPolicy = session.ToolPolicy
	}
	if catalog.workspaceRootResolver == nil {
		catalog.workspaceRootResolver = func() string {
			return resolveLocalWorkspacePath(loadRuntimeToolConfig(session.Config, session), session)
		}
	}

	catalog.syncFromRegistryLocked()
	registry := catalog.registry
	builder := catalog.builder
	binding := catalog.skillsBinding
	catalog.mu.Unlock()

	session.FunctionRegistry = registry
	session.FunctionBuilder = builder
	if session.SkillsBinding == nil && binding != nil {
		session.SkillsBinding = binding
	}
	session.BuiltinSchemas = catalog.BuiltinSchemas()

	return catalog
}

func (c *aicliFunctionCatalog) Registry() *functions.FunctionRegistry {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.registry
}

func (c *aicliFunctionCatalog) Builder(protocol string) functions.FunctionCallBuilder {
	if c == nil {
		return functions.GetFunctionCallBuilder(protocol)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.builder == nil {
		c.builder = functions.GetFunctionCallBuilder(protocol)
	}
	return c.builder
}

func (c *aicliFunctionCatalog) SetSkillsBinding(binding *skillsRuntimeBinding) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.skillsBinding = binding
}

func (c *aicliFunctionCatalog) SetToolPolicy(policy *runtimepolicy.ToolExecutionPolicy) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.toolPolicy = policy
}

func (c *aicliFunctionCatalog) SkillsBinding() *skillsRuntimeBinding {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.skillsBinding
}

func (c *aicliFunctionCatalog) RegisterBuiltinToolFunction(fn functions.Function, desc runtimetools.ToolDescriptor) {
	if c == nil || fn == nil {
		return
	}
	descriptor := &capability.Descriptor{
		ID:          fn.Name(),
		Name:        fn.Name(),
		Kind:        capability.KindTool,
		Description: fn.Description(),
		Metadata: map[string]interface{}{
			"source": "aicli_builtin_tool",
			"tool":   desc.Name,
		},
	}
	c.registerFunction(fn, descriptor, false)
}

func (c *aicliFunctionCatalog) RegisterFunction(fn functions.Function) {
	if c == nil || fn == nil {
		return
	}
	c.registerFunction(fn, buildGenericFunctionDescriptor(fn), false)
}

func (c *aicliFunctionCatalog) RegisterSkillFunction(fn *SkillFunction) {
	if c == nil || fn == nil {
		return
	}
	descriptor := buildSkillFunctionDescriptor(fn)
	c.registerFunction(fn, descriptor, true)
}

func (c *aicliFunctionCatalog) registerFunction(fn functions.Function, descriptor *capability.Descriptor, isSkill bool) {
	if c == nil || fn == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.registry == nil {
		c.registry = functions.NewFunctionRegistry()
	}
	if c.entries == nil {
		c.entries = make(map[string]*aicliCatalogEntry)
	}

	name := fn.Name()
	if _, exists := c.entries[name]; !exists {
		c.entryOrder = append(c.entryOrder, name)
		sort.Strings(c.entryOrder)
	}
	c.registry.Register(fn)
	c.entries[name] = &aicliCatalogEntry{
		name:       name,
		fn:         fn,
		schema:     buildFunctionSchema(fn),
		descriptor: descriptor,
		isSkill:    isSkill,
	}
}

// PruneSkillFunctionsExcept 撤销 catalog 与 registry 中所有不在 keep 集合内的
// skill 函数，返回被撤销的函数名（升序）。
//
// per-skill 启停热刷新的收口点：只挡新注册、不撤销已注册会造成假开关——
// 停用后的 skill 仍能被 /skills 选中并执行。
func (c *aicliFunctionCatalog) PruneSkillFunctionsExcept(keep map[string]struct{}) []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		return nil
	}
	removed := make([]string, 0, 4)
	for name, entry := range c.entries {
		if entry == nil || !entry.isSkill {
			continue
		}
		if keep != nil {
			if _, kept := keep[name]; kept {
				continue
			}
		}
		delete(c.entries, name)
		if c.registry != nil {
			c.registry.Unregister(name)
		}
		removed = append(removed, name)
	}
	if len(removed) == 0 {
		return nil
	}
	if len(c.entryOrder) > 0 {
		drop := make(map[string]struct{}, len(removed))
		for _, name := range removed {
			drop[name] = struct{}{}
		}
		keptOrder := c.entryOrder[:0]
		for _, item := range c.entryOrder {
			if _, gone := drop[item]; gone {
				continue
			}
			keptOrder = append(keptOrder, item)
		}
		c.entryOrder = keptOrder
	}
	sort.Strings(removed)
	return removed
}

// RemoveFunction 从 catalog 与 registry 撤销单个函数（运行时热操作，
// 例如会话级 MCP 停用后撤销该 server 已注册的工具，避免假开关）。
func (c *aicliFunctionCatalog) RemoveFunction(name string) bool {
	if c == nil {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[name]; !exists {
		return false
	}
	delete(c.entries, name)
	if c.registry != nil {
		c.registry.Unregister(name)
	}
	if len(c.entryOrder) > 0 {
		kept := c.entryOrder[:0]
		for _, item := range c.entryOrder {
			if item != name {
				kept = append(kept, item)
			}
		}
		c.entryOrder = kept
	}
	return true
}

// filterSessionHiddenMCPFunctions 按会话级 MCP 覆盖过滤选择结果：被本会话
// 停用的 server 的 MCP 函数不得进入模型工具面（与注册期撤销构成双保险）。
func (c *aicliFunctionCatalog) filterSessionHiddenMCPFunctions(session *ChatSession, selection *aicliFunctionSelection) *aicliFunctionSelection {
	if c == nil || selection == nil || session == nil || len(session.MCPSessionOverrides) == 0 {
		return selection
	}
	// 只读锁覆盖整个过滤过程：hidden 只做类型断言与会话覆盖表读取，
	// 不会回调 catalog（无重入风险）。
	c.mu.RLock()
	defer c.mu.RUnlock()
	hidden := func(name string) bool {
		entry := c.entries[name]
		if entry == nil || entry.fn == nil {
			return false
		}
		server := mcpFunctionServer(entry.fn)
		if server == "" {
			return false
		}
		return sessionMCPOverrideDisabled(session, server)
	}
	filtered := &aicliFunctionSelection{Mode: selection.Mode, IncludeBuiltin: selection.IncludeBuiltin}
	for _, name := range selection.BuiltinFunctions {
		if hidden(name) {
			continue
		}
		filtered.BuiltinFunctions = append(filtered.BuiltinFunctions, name)
	}
	for _, name := range selection.SkillFunctions {
		if hidden(name) {
			continue
		}
		filtered.SkillFunctions = append(filtered.SkillFunctions, name)
	}
	for _, name := range selection.FinalFunctionNames {
		if hidden(name) {
			continue
		}
		filtered.FinalFunctionNames = append(filtered.FinalFunctionNames, name)
	}
	for _, schema := range selection.Schemas {
		if name, _ := schema["name"].(string); name != "" && hidden(name) {
			continue
		}
		filtered.Schemas = append(filtered.Schemas, schema)
	}
	return filtered
}

func (c *aicliFunctionCatalog) syncFromRegistry() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.syncFromRegistryLocked()
}

// syncFromRegistryLocked 把 registry 中尚未进入 entries 的函数补齐。
// 会修改 entries/entryOrder，调用方必须持有写锁。
func (c *aicliFunctionCatalog) syncFromRegistryLocked() {
	if c == nil || c.registry == nil {
		return
	}
	if c.entries == nil {
		c.entries = make(map[string]*aicliCatalogEntry)
	}
	functionsList := c.registry.List()
	sort.Slice(functionsList, func(i, j int) bool {
		return functionsList[i].Name() < functionsList[j].Name()
	})

	for _, fn := range functionsList {
		if fn == nil {
			continue
		}
		name := fn.Name()
		if _, exists := c.entries[name]; exists {
			continue
		}
		_, isSkill := fn.(*SkillFunction)
		descriptor := buildGenericFunctionDescriptor(fn)
		if skillFn, ok := fn.(*SkillFunction); ok {
			descriptor = buildSkillFunctionDescriptor(skillFn)
		}
		c.entries[name] = &aicliCatalogEntry{
			name:       name,
			fn:         fn,
			schema:     buildFunctionSchema(fn),
			descriptor: descriptor,
			isSkill:    isSkill,
		}
		c.entryOrder = append(c.entryOrder, name)
	}
	sort.Strings(c.entryOrder)
}

func (c *aicliFunctionCatalog) BuiltinSchemas() []map[string]interface{} {
	if c == nil {
		return nil
	}
	return c.sharedCapabilitySnapshot().BuiltinSchemas()
}

func (c *aicliFunctionCatalog) SkillSchema(name string) map[string]interface{} {
	if c == nil || name == "" {
		return nil
	}
	return c.sharedCapabilitySnapshot().SkillSchema(name)
}

func (c *aicliFunctionCatalog) SkillFunctionNames() []string {
	if c == nil {
		return nil
	}
	return c.sharedCapabilitySnapshot().SkillFunctionNames()
}

func (c *aicliFunctionCatalog) BuiltinFunctionNames() []string {
	if c == nil {
		return nil
	}
	return c.sharedCapabilitySnapshot().BuiltinFunctionNames()
}

func (c *aicliFunctionCatalog) Descriptor(name string) *capability.Descriptor {
	if c == nil || name == "" {
		return nil
	}
	return c.sharedCapabilitySnapshot().Descriptor(name)
}

func (c *aicliFunctionCatalog) Descriptors() []*capability.Descriptor {
	if c == nil {
		return nil
	}
	return c.sharedCapabilitySnapshot().Descriptors()
}

// sharedCapabilitySnapshot 在锁内完成 registry→entries 同步并生成一份与
// catalog 状态完全解耦的共享目录快照；调用方在锁外查询该快照，避免把
// 外部查询/选择代码放在锁内。
func (c *aicliFunctionCatalog) sharedCapabilitySnapshot() *runtimechatcore.Catalog {
	if c == nil {
		return runtimechatcore.NewCatalog()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.syncFromRegistryLocked()
	return c.sharedCapabilityCatalogLocked()
}

func (c *aicliFunctionCatalog) SelectRequestFunctions(session *ChatSession, prompt string) (*aicliFunctionSelection, *skillExposureDetails) {
	if session == nil || c == nil {
		return nil, nil
	}
	c.mu.Lock()
	c.syncFromRegistryLocked()
	if c.registry == nil {
		c.mu.Unlock()
		return nil, nil
	}
	binding := c.skillsBinding
	toolPolicy := c.toolPolicy
	shared := c.sharedCapabilityCatalogLocked()
	c.mu.Unlock()

	var exposedSkills map[string]struct{}
	var exposureDetails *skillExposureDetails
	exposureMode := skillExposureAuto
	if binding != nil {
		exposedSkills, exposureDetails = binding.AnalyzeSkillExposure(session, prompt)
		if mode := normalizeSkillExposureMode(binding.exposureMode); mode != "" {
			exposureMode = mode
		}
	} else if mode := normalizeSkillExposureMode(session.SkillsMode); mode != "" {
		exposureMode = mode
	}
	selection := shared.Select(runtimechatcore.SelectionOptions{
		ExposureMode:  exposureMode,
		ToolPolicy:    toolPolicy,
		ExposedSkills: exposedSkills,
	})
	if selection == nil {
		selection = &aicliFunctionSelection{Mode: exposureMode}
	} else {
		selection = filterImageGenerationToolExposure(session, prompt, selection, exposureDetails)
	}
	// P3：交互式请求面收敛文本类 skill 函数（mention 注入路径接管其正文）。
	// 图片工具判定在前，保证既有图片暴露/抑制组合语义不变。
	selection = c.filterMentionHiddenTextSkillFunctions(session, selection)
	selection = c.filterSessionHiddenMCPFunctions(session, selection)
	return c.ensureInvariantGoalFunctionsSelected(selection), exposureDetails
}

func (c *aicliFunctionCatalog) SelectStableSessionFunctions(session *ChatSession) *aicliFunctionSelection {
	if session == nil || c == nil {
		return nil
	}
	c.mu.Lock()
	c.syncFromRegistryLocked()
	if c.registry == nil {
		c.mu.Unlock()
		return nil
	}
	binding := c.skillsBinding
	toolPolicy := c.toolPolicy
	shared := c.sharedCapabilityCatalogLocked()
	// 锁内只做条目/函数引用快照；mention 隐藏判定可能读盘解析技能，
	// 必须在锁外执行。
	entries := make(map[string]*aicliCatalogEntry)
	skillFns := make(map[string]*SkillFunction)
	for _, name := range shared.BuiltinFunctionNames() {
		entries[name] = c.entries[name]
	}
	for _, name := range shared.SkillFunctionNames() {
		entry := c.entries[name]
		entries[name] = entry
		if entry != nil && entry.isSkill {
			skillFns[name] = c.skillFunctionForCatalogNameLocked(name)
		}
	}
	c.mu.Unlock()

	exposureMode := skillExposureAuto
	if binding != nil {
		if mode := normalizeSkillExposureMode(binding.exposureMode); mode != "" {
			exposureMode = mode
		}
	} else if mode := normalizeSkillExposureMode(session.SkillsMode); mode != "" {
		exposureMode = mode
	}

	selection := &aicliFunctionSelection{
		Mode:           exposureMode,
		IncludeBuiltin: exposureMode != skillExposureOnly,
	}
	if selection.IncludeBuiltin {
		for _, name := range shared.BuiltinFunctionNames() {
			if toolPolicy != nil && !toolPolicy.AllowsDefinition(name) {
				continue
			}
			entry := entries[name]
			if entry == nil || len(entry.schema) == 0 {
				continue
			}
			selection.BuiltinFunctions = append(selection.BuiltinFunctions, name)
			selection.FinalFunctionNames = append(selection.FinalFunctionNames, name)
			selection.Schemas = append(selection.Schemas, cloneFunctionSchema(entry.schema))
		}
	}
	for _, name := range shared.SkillFunctionNames() {
		entry := entries[name]
		if entry == nil || len(entry.schema) == 0 {
			continue
		}
		// P3：稳定函数面与请求面同一口径，被 mention 隐藏的文本类 skill
		// 不得从稳定超集重新进入模型可见面。
		if skillMentionHideTextSkillFunction(session, skillFns[name]) {
			continue
		}
		selection.SkillFunctions = append(selection.SkillFunctions, name)
		selection.FinalFunctionNames = append(selection.FinalFunctionNames, name)
		selection.Schemas = append(selection.Schemas, cloneFunctionSchema(entry.schema))
	}
	selection = filterStableImageGenerationToolExposure(session, selection)
	selection = c.filterSessionHiddenMCPFunctions(session, selection)
	return c.normalizeFunctionSelection(c.ensureInvariantGoalFunctionsSelected(selection))
}

// skillMentionHideTextSkillFunction 报告交互式请求面是否应隐藏该文本类
// （纯说明型）skill 函数：`$mention` 注入路径接管文本技能正文后，函数面不再
// 暴露 skill__，避免模型绕过注入直接调用（plan §5 P3 / §8 Q5）。
//
// 门控三条件：配置开关（mention_hide_text_skill_functions 默认 on，显式 false
// 可回退）+ 交互式用户回合（headless/JSON 不隐藏）+ 文本类（无 handler/workflow）。
// handler/workflow 技能必须保留暴露；/call、/skill --direct、API/exec 不经此
// 选择面，行为不变。
func skillMentionHideTextSkillFunction(session *ChatSession, fn *SkillFunction) bool {
	if session == nil || fn == nil {
		return false
	}
	cfg := skillRuntimeConfig(session.Config)
	if cfg == nil || !cfg.MentionHideTextSkillFunctionsEnabled() {
		return false
	}
	if !chatSkillMentionInteractiveTurn(session) {
		return false
	}
	return skillUsesDefaultExecution(fn.resolvedTurnSkill())
}

// skillFunctionForCatalogNameLocked 解析目录条目对应的 *SkillFunction（仅 skill
// 条目）；目录条目缺失时回退到 binding 的技能函数表。非技能名返回 nil。
// 调用方必须持有读锁（或写锁）。
func (c *aicliFunctionCatalog) skillFunctionForCatalogNameLocked(name string) *SkillFunction {
	if c == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if entry := c.entries[name]; entry != nil && entry.isSkill {
		if fn, ok := entry.fn.(*SkillFunction); ok && fn != nil {
			return fn
		}
	}
	if c.skillsBinding == nil {
		return nil
	}
	return c.skillsBinding.skillFunctions[name]
}

// filterMentionHiddenTextSkillFunctions 从请求选择中剔除被 mention 收敛的
// 文本类 skill 函数，保持 SkillFunctions / FinalFunctionNames / Schemas
// 三者一致；builtin/goal/图片工具条目不经此路径。
func (c *aicliFunctionCatalog) filterMentionHiddenTextSkillFunctions(session *ChatSession, selection *aicliFunctionSelection) *aicliFunctionSelection {
	if c == nil || selection == nil || len(selection.SkillFunctions) == 0 {
		return selection
	}
	// 锁内只解析函数引用；mention 隐藏判定可能读盘解析技能，必须在锁外执行。
	refs := make(map[string]*SkillFunction, len(selection.SkillFunctions))
	c.mu.RLock()
	for _, name := range selection.SkillFunctions {
		refs[name] = c.skillFunctionForCatalogNameLocked(name)
	}
	c.mu.RUnlock()
	var hidden []string
	for _, name := range selection.SkillFunctions {
		if skillMentionHideTextSkillFunction(session, refs[name]) {
			hidden = append(hidden, name)
		}
	}
	if len(hidden) == 0 {
		return selection
	}
	return removeFunctionsFromSelection(selection, hidden...)
}

func (c *aicliFunctionCatalog) ensureInvariantGoalFunctionsSelected(selection *aicliFunctionSelection) *aicliFunctionSelection {
	if c == nil {
		return selection
	}
	if selection == nil {
		selection = &aicliFunctionSelection{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, name := range []string{getGoalFunctionName, updateGoalFunctionName} {
		entry := c.entries[name]
		if entry == nil || entry.isSkill || len(entry.schema) == 0 {
			continue
		}
		if c.toolPolicy != nil && !c.toolPolicy.AllowsDefinition(name) {
			continue
		}
		if selectionContainsFunction(selection, name) {
			continue
		}
		selection.BuiltinFunctions = append(selection.BuiltinFunctions, name)
		selection.FinalFunctionNames = append(selection.FinalFunctionNames, name)
		selection.Schemas = append(selection.Schemas, cloneFunctionSchema(entry.schema))
	}
	if len(selection.BuiltinFunctions) > 0 {
		selection.IncludeBuiltin = true
		sort.Strings(selection.BuiltinFunctions)
	}
	if len(selection.FinalFunctionNames) > 0 {
		sort.Strings(selection.FinalFunctionNames)
	}
	sort.SliceStable(selection.Schemas, func(i, j int) bool {
		left, _ := selection.Schemas[i]["name"].(string)
		right, _ := selection.Schemas[j]["name"].(string)
		return strings.TrimSpace(left) < strings.TrimSpace(right)
	})
	return selection
}

func (c *aicliFunctionCatalog) normalizeFunctionSelection(selection *aicliFunctionSelection) *aicliFunctionSelection {
	if selection == nil {
		return nil
	}
	selection.BuiltinFunctions = uniqueStrings(selection.BuiltinFunctions)
	selection.SkillFunctions = uniqueStrings(selection.SkillFunctions)
	selection.FinalFunctionNames = uniqueStrings(selection.FinalFunctionNames)
	sort.Strings(selection.BuiltinFunctions)
	sort.Strings(selection.SkillFunctions)
	sort.Strings(selection.FinalFunctionNames)
	sort.SliceStable(selection.Schemas, func(i, j int) bool {
		left, _ := selection.Schemas[i]["name"].(string)
		right, _ := selection.Schemas[j]["name"].(string)
		return strings.TrimSpace(left) < strings.TrimSpace(right)
	})
	return selection
}

// sharedCapabilityCatalogLocked 基于当前 entries 构造共享目录快照，条目
// schema/descriptor 均做深拷贝，返回值不再引用 catalog 状态。调用方必须
// 持有读锁（或写锁）。
func (c *aicliFunctionCatalog) sharedCapabilityCatalogLocked() *runtimechatcore.Catalog {
	shared := runtimechatcore.NewCatalog()
	if c == nil {
		return shared
	}
	for _, name := range c.entryOrder {
		entry := c.entries[name]
		if entry == nil {
			continue
		}
		shared.Upsert(runtimechatcore.CatalogEntry{
			Name:       entry.name,
			Schema:     cloneFunctionSchema(entry.schema),
			Descriptor: cloneCapabilityDescriptor(entry.descriptor),
			IsSkill:    entry.isSkill,
		})
	}
	return shared
}

func (c *aicliFunctionCatalog) ExecuteFunction(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	output, _, err := c.ExecuteFunctionWithMeta(ctx, name, args)
	return output, err
}

func (c *aicliFunctionCatalog) ExecuteFunctionWithMeta(ctx context.Context, name string, args map[string]interface{}) (string, map[string]interface{}, error) {
	if c == nil {
		return "", nil, fmt.Errorf("function catalog is not initialized")
	}
	// 锁内只做 registry 查询与状态快照；工具策略检查与函数执行（外部代码，
	// 可能重入 catalog）都在锁外进行。
	c.mu.Lock()
	if c.registry == nil {
		c.mu.Unlock()
		return "", nil, fmt.Errorf("function catalog is not initialized")
	}
	c.syncFromRegistryLocked()
	fn, found := c.registry.Get(name)
	entry := c.entries[name]
	toolPolicy := c.toolPolicy
	workspaceRootResolver := c.workspaceRootResolver
	c.mu.Unlock()

	args = toolargs.Normalize(args)
	if entry != nil && !entry.isSkill && toolPolicy != nil {
		// Anchor relative path arguments to the session workspace root the
		// executor resolves against. Without this the sandbox check would run
		// against the process working directory, so a relative argument could
		// pass the check and still escape the bound project directory.
		policyCtx := ctx
		if workspaceRootResolver != nil {
			if root := strings.TrimSpace(workspaceRootResolver()); root != "" {
				policyCtx = toolctx.WithWorkspaceRoot(policyCtx, root)
			}
		}
		if err := toolPolicy.AllowToolCallWithContext(policyCtx, skill.ToolInfo{Name: name}, args); err != nil {
			return "", nil, err
		}
	}
	if !found || fn == nil {
		return "", nil, fmt.Errorf("function '%s' not found", name)
	}
	if rich, ok := fn.(functions.FunctionWithMetadata); ok {
		return rich.ExecuteWithMeta(ctx, args)
	}
	output, err := fn.Execute(ctx, args)
	return output, nil, err
}

func (c *aicliFunctionCatalog) Stats() aicliFunctionCatalogStats {
	if c == nil {
		return aicliFunctionCatalogStats{}
	}
	return c.sharedCapabilitySnapshot().Stats()
}

func buildFunctionSchema(fn functions.Function) map[string]interface{} {
	if fn == nil {
		return nil
	}
	schema := map[string]interface{}{
		"name":        fn.Name(),
		"description": fn.Description(),
		"parameters":  fn.Parameters(),
	}
	if provider, ok := fn.(functions.FunctionDefinitionMetadataProvider); ok {
		if metadata := provider.DefinitionMetadata(); len(metadata) > 0 {
			schema["metadata"] = cloneFunctionSchema(metadata)
		}
	}
	return schema
}

func buildGenericFunctionDescriptor(fn functions.Function) *capability.Descriptor {
	if fn == nil {
		return nil
	}
	return &capability.Descriptor{
		ID:          fn.Name(),
		Name:        fn.Name(),
		Kind:        capability.KindTool,
		Description: fn.Description(),
		Metadata: map[string]interface{}{
			"source": "aicli_function",
		},
	}
}

func buildSkillFunctionDescriptor(fn *SkillFunction) *capability.Descriptor {
	if fn == nil {
		return nil
	}
	if fn.skill == nil {
		return buildGenericFunctionDescriptor(fn)
	}

	descriptor := fn.skill.CapabilityDescriptor()
	if descriptor == nil {
		return buildGenericFunctionDescriptor(fn)
	}
	if descriptor.Metadata == nil {
		descriptor.Metadata = make(map[string]interface{})
	}
	descriptor.Metadata["function_name"] = fn.Name()
	descriptor.Metadata["source"] = "aicli_skill_function"
	if skillName := strings.TrimSpace(fn.skill.Name); skillName != "" {
		descriptor.Metadata["skill_name"] = skillName
	}
	if fn.sourcePath != "" {
		descriptor.Metadata["skill_path"] = fn.sourcePath
	}
	return descriptor
}

func cloneCapabilityDescriptor(input *capability.Descriptor) *capability.Descriptor {
	if input == nil {
		return nil
	}

	output := *input
	if len(input.Labels) > 0 {
		output.Labels = append([]string(nil), input.Labels...)
	}
	if len(input.Capabilities) > 0 {
		output.Capabilities = append([]string(nil), input.Capabilities...)
	}
	if len(input.Triggers) > 0 {
		output.Triggers = append([]capability.Trigger(nil), input.Triggers...)
	}
	if len(input.Dependencies) > 0 {
		output.Dependencies = append([]capability.Dependency(nil), input.Dependencies...)
	}
	if input.Source != nil {
		sourceCopy := *input.Source
		output.Source = &sourceCopy
	}
	if len(input.Metadata) > 0 {
		output.Metadata = make(map[string]interface{}, len(input.Metadata))
		for key, value := range input.Metadata {
			output.Metadata[key] = value
		}
	}
	return &output
}
