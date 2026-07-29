package policy

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ToolTaxonomy captures stable capability-oriented tool metadata.
type ToolTaxonomy struct {
	Name        string
	Kind        string
	ReadOnly    bool
	MutatesFS   bool
	RequiresNet bool
}

// knownToolTaxonomy is the built-in table for core toolkit / broker tools.
// CapabilityResolver prefers this over name heuristics when present.
var knownToolTaxonomy = map[string]ToolTaxonomy{
	"view":                   {Name: "view", Kind: types.ToolKindRead, ReadOnly: true},
	"grep":                   {Name: "grep", Kind: types.ToolKindSearch, ReadOnly: true},
	"glob":                   {Name: "glob", Kind: types.ToolKindSearch, ReadOnly: true},
	"ls":                     {Name: "ls", Kind: types.ToolKindRead, ReadOnly: true},
	"sourcegraph":            {Name: "sourcegraph", Kind: types.ToolKindSearch, ReadOnly: true, RequiresNet: true},
	"web_search":             {Name: "web_search", Kind: types.ToolKindSearch, ReadOnly: true, RequiresNet: true},
	"search_tool":            {Name: "search_tool", Kind: types.ToolKindSearch, ReadOnly: true},
	"fetch":                  {Name: "fetch", Kind: types.ToolKindNetwork, ReadOnly: true, RequiresNet: true},
	"download":               {Name: "download", Kind: types.ToolKindNetwork, MutatesFS: true, RequiresNet: true},
	"write":                  {Name: "write", Kind: types.ToolKindEdit, MutatesFS: true},
	"edit":                   {Name: "edit", Kind: types.ToolKindEdit, MutatesFS: true},
	"multiedit":              {Name: "multiedit", Kind: types.ToolKindEdit, MutatesFS: true},
	"append_write":           {Name: "append_write", Kind: types.ToolKindEdit, MutatesFS: true},
	"apply_patch":            {Name: "apply_patch", Kind: types.ToolKindEdit, MutatesFS: true},
	"shell":                  {Name: "shell", Kind: types.ToolKindExec},
	"bash":                   {Name: "bash", Kind: types.ToolKindExec},
	"aicli_exec":             {Name: "aicli_exec", Kind: types.ToolKindExec},
	"ask_user_question":      {Name: "ask_user_question", Kind: types.ToolKindControl, ReadOnly: true},
	"enter_plan_mode":        {Name: "enter_plan_mode", Kind: types.ToolKindControl, ReadOnly: true},
	"exit_plan_mode":         {Name: "exit_plan_mode", Kind: types.ToolKindControl, ReadOnly: true},
	"background_task":        {Name: "background_task", Kind: types.ToolKindControl},
	"task_output":            {Name: "task_output", Kind: types.ToolKindRead, ReadOnly: true},
	"spawn_agent":            {Name: "spawn_agent", Kind: types.ToolKindControl, ReadOnly: true},
	"list_agents":            {Name: "list_agents", Kind: types.ToolKindControl, ReadOnly: true},
	"send_message":           {Name: "send_message", Kind: types.ToolKindControl, ReadOnly: true},
	"followup_task":          {Name: "followup_task", Kind: types.ToolKindControl, ReadOnly: true},
	"send_input":             {Name: "send_input", Kind: types.ToolKindControl, ReadOnly: true},
	"wait_agent":             {Name: "wait_agent", Kind: types.ToolKindControl, ReadOnly: true},
	"read_agent_events":      {Name: "read_agent_events", Kind: types.ToolKindControl, ReadOnly: true},
	"close_agent":            {Name: "close_agent", Kind: types.ToolKindControl, ReadOnly: true},
	"resume_agent":           {Name: "resume_agent", Kind: types.ToolKindControl, ReadOnly: true},
	"resolve_agent_approval": {Name: "resolve_agent_approval", Kind: types.ToolKindControl, ReadOnly: true},
	"spawn_team":             {Name: "spawn_team", Kind: types.ToolKindControl, ReadOnly: true},
	"wait_team":              {Name: "wait_team", Kind: types.ToolKindControl, ReadOnly: true},
	"send_team_message":      {Name: "send_team_message", Kind: types.ToolKindControl, ReadOnly: true},
	"read_mailbox_digest":    {Name: "read_mailbox_digest", Kind: types.ToolKindControl, ReadOnly: true},
	"read_task_spec":         {Name: "read_task_spec", Kind: types.ToolKindControl, ReadOnly: true},
	"read_task_context":      {Name: "read_task_context", Kind: types.ToolKindControl, ReadOnly: true},
	"report_task_outcome":    {Name: "report_task_outcome", Kind: types.ToolKindControl, ReadOnly: true},
	"block_current_task":     {Name: "block_current_task", Kind: types.ToolKindControl, ReadOnly: true},
	"todos":                  {Name: "todos", Kind: types.ToolKindControl, ReadOnly: true},
	"get_goal":               {Name: "get_goal", Kind: types.ToolKindControl, ReadOnly: true},
	"update_goal":            {Name: "update_goal", Kind: types.ToolKindControl, ReadOnly: true},
	"openai_image_generate":  {Name: "openai_image_generate", Kind: types.ToolKindNetwork, RequiresNet: true},
}

// LookupToolTaxonomy returns built-in taxonomy for a tool name when known.
func LookupToolTaxonomy(toolName string) (ToolTaxonomy, bool) {
	name := normalizeToolName(toolName)
	tax, ok := knownToolTaxonomy[name]
	return tax, ok
}

// TaxonomyFromMetadata builds taxonomy from tool definition metadata map.
func TaxonomyFromMetadata(toolName string, metadata map[string]interface{}) (ToolTaxonomy, bool) {
	if len(metadata) == 0 {
		return ToolTaxonomy{}, false
	}
	tax := ToolTaxonomy{Name: normalizeToolName(toolName)}
	found := false
	if kind, ok := stringMetadataValue(metadata, types.ToolMetadataKindKey); ok {
		tax.Kind = kind
		found = true
	}
	if v, ok := types.BoolMetadataValue(metadata, types.ToolMetadataReadOnlyKey); ok {
		tax.ReadOnly = v
		found = true
	}
	if v, ok := types.BoolMetadataValue(metadata, types.ToolMetadataMutatesFSKey); ok {
		tax.MutatesFS = v
		found = true
	}
	if v, ok := types.BoolMetadataValue(metadata, types.ToolMetadataRequiresNetKey); ok {
		tax.RequiresNet = v
		found = true
	}
	if !found {
		return ToolTaxonomy{}, false
	}
	return tax, true
}

// ResolveToolTaxonomy prefers request metadata, then built-in table.
func ResolveToolTaxonomy(toolName string, metadata map[string]interface{}) (ToolTaxonomy, bool) {
	if tax, ok := TaxonomyFromMetadata(toolName, metadata); ok {
		return tax, true
	}
	return LookupToolTaxonomy(toolName)
}

func capabilitiesFromTaxonomy(tax ToolTaxonomy) []Capability {
	name := normalizeToolName(tax.Name)

	switch name {
	case "ask_user_question":
		return []Capability{CapAskUser}
	case "enter_plan_mode", "exit_plan_mode":
		// Control tools usable while already in plan mode (read-only + ask_user).
		return []Capability{CapReadOnly, CapAskUser}
	case "background_task":
		return []Capability{CapBackgroundTask}
	case "spawn_agent", "send_message", "followup_task", "send_input", "close_agent", "resume_agent", "resolve_agent_approval", "spawn_team", "send_team_message":
		return []Capability{CapReadOnly, CapAgentManagement}
	case "list_agents", "wait_agent", "read_agent_events", "wait_team", "read_mailbox_digest", "read_task_spec", "read_task_context", "report_task_outcome", "block_current_task":
		return []Capability{CapReadOnly}
	}

	caps := make([]Capability, 0, 4)
	if tax.MutatesFS || tax.Kind == types.ToolKindEdit {
		caps = append(caps, CapWriteFS)
	} else if tax.ReadOnly || tax.Kind == types.ToolKindRead || tax.Kind == types.ToolKindSearch || tax.Kind == types.ToolKindControl {
		caps = append(caps, CapReadOnly)
	} else if tax.Kind != types.ToolKindExec {
		caps = append(caps, CapReadOnly)
	}

	if tax.Kind == types.ToolKindExec {
		caps = append(caps, CapExecShell)
	}
	if tax.RequiresNet || tax.Kind == types.ToolKindNetwork {
		caps = append(caps, CapNetwork)
	}
	return dedupeCapabilities(caps)
}

func stringMetadataValue(metadata map[string]interface{}, key string) (string, bool) {
	if len(metadata) == 0 {
		return "", false
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return "", false
	}
	switch typed := raw.(type) {
	case string:
		value := strings.TrimSpace(typed)
		if value == "" {
			return "", false
		}
		return value, true
	default:
		value := strings.TrimSpace(fmt.Sprint(raw))
		if value == "" || value == "<nil>" {
			return "", false
		}
		return value, true
	}
}
