package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/internal/capability"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
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
	registry      *functions.FunctionRegistry
	builder       functions.FunctionCallBuilder
	skillsBinding *skillsRuntimeBinding
	toolPolicy    *runtimepolicy.ToolExecutionPolicy
	entries       map[string]*aicliCatalogEntry
	entryOrder    []string
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
	if session.FunctionCatalog.registry == nil {
		if session.FunctionRegistry != nil {
			session.FunctionCatalog.registry = session.FunctionRegistry
		} else {
			session.FunctionCatalog.registry = functions.NewFunctionRegistry()
		}
	}
	if session.FunctionCatalog.builder == nil {
		if session.FunctionBuilder != nil {
			session.FunctionCatalog.builder = session.FunctionBuilder
		} else {
			session.FunctionCatalog.builder = functions.GetFunctionCallBuilder(session.Provider.GetProtocol())
		}
	}
	if session.FunctionCatalog.entries == nil {
		session.FunctionCatalog.entries = make(map[string]*aicliCatalogEntry)
	}
	if session.FunctionCatalog.skillsBinding == nil && session.SkillsBinding != nil {
		session.FunctionCatalog.skillsBinding = session.SkillsBinding
	}
	if session.FunctionCatalog.toolPolicy == nil && session.ToolPolicy != nil {
		session.FunctionCatalog.toolPolicy = session.ToolPolicy
	}

	session.FunctionCatalog.syncFromRegistry()

	session.FunctionRegistry = session.FunctionCatalog.registry
	session.FunctionBuilder = session.FunctionCatalog.builder
	if session.SkillsBinding == nil && session.FunctionCatalog.skillsBinding != nil {
		session.SkillsBinding = session.FunctionCatalog.skillsBinding
	}
	session.BuiltinSchemas = session.FunctionCatalog.BuiltinSchemas()

	return session.FunctionCatalog
}

func (c *aicliFunctionCatalog) Registry() *functions.FunctionRegistry {
	if c == nil {
		return nil
	}
	return c.registry
}

func (c *aicliFunctionCatalog) Builder(protocol string) functions.FunctionCallBuilder {
	if c == nil {
		return functions.GetFunctionCallBuilder(protocol)
	}
	if c.builder == nil {
		c.builder = functions.GetFunctionCallBuilder(protocol)
	}
	return c.builder
}

func (c *aicliFunctionCatalog) SetSkillsBinding(binding *skillsRuntimeBinding) {
	if c == nil {
		return
	}
	c.skillsBinding = binding
}

func (c *aicliFunctionCatalog) SetToolPolicy(policy *runtimepolicy.ToolExecutionPolicy) {
	if c == nil {
		return
	}
	c.toolPolicy = policy
}

func (c *aicliFunctionCatalog) SkillsBinding() *skillsRuntimeBinding {
	if c == nil {
		return nil
	}
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

func (c *aicliFunctionCatalog) syncFromRegistry() {
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
	c.syncFromRegistry()
	return c.sharedCapabilityCatalog().BuiltinSchemas()
}

func (c *aicliFunctionCatalog) SkillSchema(name string) map[string]interface{} {
	if c == nil || name == "" {
		return nil
	}
	c.syncFromRegistry()
	return c.sharedCapabilityCatalog().SkillSchema(name)
}

func (c *aicliFunctionCatalog) SkillFunctionNames() []string {
	if c == nil {
		return nil
	}
	c.syncFromRegistry()
	return c.sharedCapabilityCatalog().SkillFunctionNames()
}

func (c *aicliFunctionCatalog) BuiltinFunctionNames() []string {
	if c == nil {
		return nil
	}
	c.syncFromRegistry()
	return c.sharedCapabilityCatalog().BuiltinFunctionNames()
}

func (c *aicliFunctionCatalog) Descriptor(name string) *capability.Descriptor {
	if c == nil || name == "" {
		return nil
	}
	c.syncFromRegistry()
	return c.sharedCapabilityCatalog().Descriptor(name)
}

func (c *aicliFunctionCatalog) Descriptors() []*capability.Descriptor {
	if c == nil {
		return nil
	}
	c.syncFromRegistry()
	return c.sharedCapabilityCatalog().Descriptors()
}

func (c *aicliFunctionCatalog) SelectRequestFunctions(session *ChatSession, prompt string) (*aicliFunctionSelection, *skillExposureDetails) {
	if session == nil || c == nil || c.registry == nil {
		return nil, nil
	}
	c.syncFromRegistry()

	var exposedSkills map[string]struct{}
	var exposureDetails *skillExposureDetails
	exposureMode := skillExposureAuto
	if binding := c.SkillsBinding(); binding != nil {
		exposedSkills, exposureDetails = binding.AnalyzeSkillExposure(session, prompt)
		if mode := normalizeSkillExposureMode(binding.exposureMode); mode != "" {
			exposureMode = mode
		}
	} else if mode := normalizeSkillExposureMode(session.SkillsMode); mode != "" {
		exposureMode = mode
	}
	selection := c.sharedCapabilityCatalog().Select(runtimechatcore.SelectionOptions{
		ExposureMode:  exposureMode,
		ToolPolicy:    c.toolPolicy,
		ExposedSkills: exposedSkills,
	})
	if selection == nil {
		selection = &aicliFunctionSelection{Mode: exposureMode}
	} else {
		selection = filterImageGenerationToolExposure(session, prompt, selection, exposureDetails)
	}
	return c.ensureInvariantGoalFunctionsSelected(selection), exposureDetails
}

func (c *aicliFunctionCatalog) SelectStableSessionFunctions(session *ChatSession) *aicliFunctionSelection {
	if session == nil || c == nil || c.registry == nil {
		return nil
	}
	c.syncFromRegistry()

	exposureMode := skillExposureAuto
	if binding := c.SkillsBinding(); binding != nil {
		if mode := normalizeSkillExposureMode(binding.exposureMode); mode != "" {
			exposureMode = mode
		}
	} else if mode := normalizeSkillExposureMode(session.SkillsMode); mode != "" {
		exposureMode = mode
	}

	shared := c.sharedCapabilityCatalog()
	selection := &aicliFunctionSelection{
		Mode:           exposureMode,
		IncludeBuiltin: exposureMode != skillExposureOnly,
	}
	if selection.IncludeBuiltin {
		for _, name := range shared.BuiltinFunctionNames() {
			if c.toolPolicy != nil && !c.toolPolicy.AllowsDefinition(name) {
				continue
			}
			entry := c.entries[name]
			if entry == nil || len(entry.schema) == 0 {
				continue
			}
			selection.BuiltinFunctions = append(selection.BuiltinFunctions, name)
			selection.FinalFunctionNames = append(selection.FinalFunctionNames, name)
			selection.Schemas = append(selection.Schemas, cloneFunctionSchema(entry.schema))
		}
	}
	for _, name := range shared.SkillFunctionNames() {
		entry := c.entries[name]
		if entry == nil || len(entry.schema) == 0 {
			continue
		}
		selection.SkillFunctions = append(selection.SkillFunctions, name)
		selection.FinalFunctionNames = append(selection.FinalFunctionNames, name)
		selection.Schemas = append(selection.Schemas, cloneFunctionSchema(entry.schema))
	}
	selection = filterStableImageGenerationToolExposure(session, selection)
	return c.normalizeFunctionSelection(c.ensureInvariantGoalFunctionsSelected(selection))
}

func (c *aicliFunctionCatalog) ensureInvariantGoalFunctionsSelected(selection *aicliFunctionSelection) *aicliFunctionSelection {
	if c == nil {
		return selection
	}
	if selection == nil {
		selection = &aicliFunctionSelection{}
	}
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

func (c *aicliFunctionCatalog) sharedCapabilityCatalog() *runtimechatcore.Catalog {
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
	if c == nil || c.registry == nil {
		return "", nil, fmt.Errorf("function catalog is not initialized")
	}
	c.syncFromRegistry()
	if entry, ok := c.entries[name]; ok && entry != nil && !entry.isSkill && c.toolPolicy != nil {
		if err := c.toolPolicy.AllowToolCall(skill.ToolInfo{Name: name}, args); err != nil {
			return "", nil, err
		}
	}
	return c.registry.ExecuteFunctionWithMeta(ctx, name, args)
}

func (c *aicliFunctionCatalog) Stats() aicliFunctionCatalogStats {
	if c == nil {
		return aicliFunctionCatalogStats{}
	}
	c.syncFromRegistry()
	return c.sharedCapabilityCatalog().Stats()
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
