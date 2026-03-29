package chatcore

import (
	"sort"
	"strings"

	"github.com/ai-gateway/ai-agent-runtime/internal/capability"
	runtimepolicy "github.com/ai-gateway/ai-agent-runtime/internal/policy"
)

const (
	SkillExposureAuto   = "auto"
	SkillExposurePrefer = "prefer"
	SkillExposureOnly   = "only"
)

// CatalogEntry is the shared chat-core view of an exposed function/tool.
type CatalogEntry struct {
	Name       string
	Schema     map[string]interface{}
	Descriptor *capability.Descriptor
	IsSkill    bool
}

// CatalogStats summarizes the functions currently known to the catalog.
type CatalogStats struct {
	TotalFunctions int `json:"total_functions"`
	BuiltinTools   int `json:"builtin_tools"`
	SkillFunctions int `json:"skill_functions"`
}

// FunctionSelection is the pure selection result used by chat entrypoints.
type FunctionSelection struct {
	Mode               string                   `json:"mode,omitempty"`
	IncludeBuiltin     bool                     `json:"include_builtin"`
	BuiltinFunctions   []string                 `json:"builtin_functions,omitempty"`
	SkillFunctions     []string                 `json:"skill_functions,omitempty"`
	FinalFunctionNames []string                 `json:"final_function_names,omitempty"`
	Schemas            []map[string]interface{} `json:"schemas,omitempty"`
}

// SelectionOptions control pure capability selection from a catalog snapshot.
type SelectionOptions struct {
	ExposureMode  string
	ExposedSkills map[string]struct{}
	ToolPolicy    *runtimepolicy.ToolExecutionPolicy
}

// Catalog stores the shared transport-neutral function/catalog metadata.
type Catalog struct {
	entries    map[string]*CatalogEntry
	entryOrder []string
}

// NewCatalog creates an empty catalog.
func NewCatalog() *Catalog {
	return &Catalog{
		entries: make(map[string]*CatalogEntry),
	}
}

// Upsert inserts or replaces a catalog entry while preserving deterministic order.
func (c *Catalog) Upsert(entry CatalogEntry) {
	if c == nil {
		return
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		return
	}
	if c.entries == nil {
		c.entries = make(map[string]*CatalogEntry)
	}
	if _, exists := c.entries[name]; !exists {
		c.entryOrder = append(c.entryOrder, name)
		sort.Strings(c.entryOrder)
	}
	cloned := entry
	cloned.Name = name
	cloned.Schema = cloneSchema(entry.Schema)
	cloned.Descriptor = cloneDescriptor(entry.Descriptor)
	c.entries[name] = &cloned
}

// Stats returns grouped builtin/skill counts.
func (c *Catalog) Stats() CatalogStats {
	stats := CatalogStats{}
	if c == nil {
		return stats
	}
	stats.TotalFunctions = len(c.entries)
	for _, entry := range c.entries {
		if entry == nil {
			continue
		}
		if entry.IsSkill {
			stats.SkillFunctions++
			continue
		}
		stats.BuiltinTools++
	}
	return stats
}

// BuiltinSchemas returns builtin schemas in deterministic order.
func (c *Catalog) BuiltinSchemas() []map[string]interface{} {
	if c == nil {
		return nil
	}
	schemas := make([]map[string]interface{}, 0, len(c.entries))
	for _, name := range c.entryOrder {
		entry := c.entries[name]
		if entry == nil || entry.IsSkill || len(entry.Schema) == 0 {
			continue
		}
		schemas = append(schemas, cloneSchema(entry.Schema))
	}
	return schemas
}

// SkillSchema returns a cloned schema for the named skill function.
func (c *Catalog) SkillSchema(name string) map[string]interface{} {
	if c == nil || strings.TrimSpace(name) == "" {
		return nil
	}
	entry, ok := c.entries[strings.TrimSpace(name)]
	if !ok || entry == nil || !entry.IsSkill {
		return nil
	}
	return cloneSchema(entry.Schema)
}

// SkillFunctionNames returns skill names in deterministic order.
func (c *Catalog) SkillFunctionNames() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0)
	for _, name := range c.entryOrder {
		entry := c.entries[name]
		if entry == nil || !entry.IsSkill {
			continue
		}
		names = append(names, name)
	}
	return names
}

// BuiltinFunctionNames returns builtin function names in deterministic order.
func (c *Catalog) BuiltinFunctionNames() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0)
	for _, name := range c.entryOrder {
		entry := c.entries[name]
		if entry == nil || entry.IsSkill {
			continue
		}
		names = append(names, name)
	}
	return names
}

// Descriptor returns a cloned descriptor for the named entry.
func (c *Catalog) Descriptor(name string) *capability.Descriptor {
	if c == nil || strings.TrimSpace(name) == "" {
		return nil
	}
	entry, ok := c.entries[strings.TrimSpace(name)]
	if !ok || entry == nil {
		return nil
	}
	return cloneDescriptor(entry.Descriptor)
}

// Descriptors returns cloned descriptors in deterministic order.
func (c *Catalog) Descriptors() []*capability.Descriptor {
	if c == nil {
		return nil
	}
	descriptors := make([]*capability.Descriptor, 0, len(c.entryOrder))
	for _, name := range c.entryOrder {
		entry := c.entries[name]
		if entry == nil || entry.Descriptor == nil {
			continue
		}
		descriptors = append(descriptors, cloneDescriptor(entry.Descriptor))
	}
	return descriptors
}

// Select applies exposure mode, builtin tool filtering, and deterministic ordering.
func (c *Catalog) Select(options SelectionOptions) *FunctionSelection {
	if c == nil {
		return nil
	}
	mode := NormalizeSkillExposureMode(options.ExposureMode)
	if mode == "" {
		mode = SkillExposureAuto
	}

	selection := &FunctionSelection{
		Mode: mode,
	}

	includeBuiltin := mode != SkillExposureOnly && !(mode == SkillExposurePrefer && len(options.ExposedSkills) > 0)
	selection.IncludeBuiltin = includeBuiltin

	if includeBuiltin {
		for _, name := range c.BuiltinFunctionNames() {
			if options.ToolPolicy != nil && !options.ToolPolicy.AllowsDefinition(name) {
				continue
			}
			entry := c.entries[name]
			if entry == nil || len(entry.Schema) == 0 {
				continue
			}
			selection.BuiltinFunctions = append(selection.BuiltinFunctions, name)
			selection.FinalFunctionNames = append(selection.FinalFunctionNames, name)
			selection.Schemas = append(selection.Schemas, cloneSchema(entry.Schema))
		}
	}

	if len(options.ExposedSkills) > 0 {
		for _, name := range c.SkillFunctionNames() {
			if _, ok := options.ExposedSkills[name]; !ok {
				continue
			}
			entry := c.entries[name]
			if entry == nil || len(entry.Schema) == 0 {
				continue
			}
			selection.SkillFunctions = append(selection.SkillFunctions, name)
			selection.FinalFunctionNames = append(selection.FinalFunctionNames, name)
			selection.Schemas = append(selection.Schemas, cloneSchema(entry.Schema))
		}
	}

	return selection
}

// NormalizeSkillExposureMode normalizes known exposure mode values.
func NormalizeSkillExposureMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		return ""
	case SkillExposureAuto:
		return SkillExposureAuto
	case SkillExposurePrefer:
		return SkillExposurePrefer
	case SkillExposureOnly:
		return SkillExposureOnly
	default:
		return SkillExposureAuto
	}
}

func cloneSchema(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]interface{}, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneDescriptor(input *capability.Descriptor) *capability.Descriptor {
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
