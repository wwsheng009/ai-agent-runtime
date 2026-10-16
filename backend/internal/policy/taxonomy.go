package policy

import (
	"fmt"
	"sort"
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
//
// 只登记**规范名**：LookupToolTaxonomy 先经 toolregistry.Canonical 归一，
// 别名（read / rg / bash / execute_shell_command）命中规范名那一行，不再各占一行。
// 手抄别名行必然漂移——execute_shell_command 曾经就是漏抄的那一个，于是它的
// 请求掉进名称启发式回退，而不是按表里的 exec 语义处理。
var knownToolTaxonomy = map[string]ToolTaxonomy{
	"view":              {Name: "view", Kind: types.ToolKindRead, ReadOnly: true},
	"grep":              {Name: "grep", Kind: types.ToolKindSearch, ReadOnly: true},
	"glob":              {Name: "glob", Kind: types.ToolKindSearch, ReadOnly: true},
	"ls":                {Name: "ls", Kind: types.ToolKindRead, ReadOnly: true},
	"sourcegraph":       {Name: "sourcegraph", Kind: types.ToolKindSearch, ReadOnly: true, RequiresNet: true},
	"web_search":        {Name: "web_search", Kind: types.ToolKindSearch, ReadOnly: true, RequiresNet: true},
	"search_tool":       {Name: "search_tool", Kind: types.ToolKindSearch, ReadOnly: true},
	"fetch":             {Name: "fetch", Kind: types.ToolKindNetwork, ReadOnly: true, RequiresNet: true},
	"download":          {Name: "download", Kind: types.ToolKindNetwork, MutatesFS: true, RequiresNet: true},
	"write":             {Name: "write", Kind: types.ToolKindEdit, MutatesFS: true},
	"edit":              {Name: "edit", Kind: types.ToolKindEdit, MutatesFS: true},
	"multiedit":         {Name: "multiedit", Kind: types.ToolKindEdit, MutatesFS: true},
	"append_write":      {Name: "append_write", Kind: types.ToolKindEdit, MutatesFS: true},
	"apply_patch":       {Name: "apply_patch", Kind: types.ToolKindEdit, MutatesFS: true},
	"shell":             {Name: "shell", Kind: types.ToolKindExec},
	"aicli_exec":        {Name: "aicli_exec", Kind: types.ToolKindExec},
	"ask_user_question": {Name: "ask_user_question", Kind: types.ToolKindControl, ReadOnly: true},
	"plan_enter":        {Name: "plan_enter", Kind: types.ToolKindControl, ReadOnly: true},
	"plan_exit":         {Name: "plan_exit", Kind: types.ToolKindControl, ReadOnly: true},
	"plan_review":       {Name: "plan_review", Kind: types.ToolKindControl, ReadOnly: true},
	"job_start":         {Name: "job_start", Kind: types.ToolKindControl},
	"job_output":        {Name: "job_output", Kind: types.ToolKindRead, ReadOnly: true},
	"job_cancel":        {Name: "job_cancel", Kind: types.ToolKindControl},
	"job_monitor":       {Name: "job_monitor", Kind: types.ToolKindControl},
	"job_control":       {Name: "job_control", Kind: types.ToolKindControl},
	"job_input":         {Name: "job_input", Kind: types.ToolKindControl},
	"job_list":          {Name: "job_list", Kind: types.ToolKindRead, ReadOnly: true},
	"job_search":        {Name: "job_search", Kind: types.ToolKindRead, ReadOnly: true},
	// Retired job-family spellings (background_task / task_output / task_kill /
	// task_monitor / task_control / write_stdin / task_input) deliberately have
	// no row: toolregistry.Normalize folds them onto the job_* keys above, so a
	// profile or grant written against them still validates — through the one
	// vocabulary, not through a second unreachable copy of the name list.
	"subagent_spawn":            {Name: "subagent_spawn", Kind: types.ToolKindControl, ReadOnly: true},
	"subagent_list":             {Name: "subagent_list", Kind: types.ToolKindControl, ReadOnly: true},
	"subagent_send":             {Name: "subagent_send", Kind: types.ToolKindControl, ReadOnly: true},
	"subagent_followup":         {Name: "subagent_followup", Kind: types.ToolKindControl, ReadOnly: true},
	"subagent_input":            {Name: "subagent_input", Kind: types.ToolKindControl, ReadOnly: true},
	"subagent_approve":          {Name: "subagent_approve", Kind: types.ToolKindControl, ReadOnly: true},
	"subagent_wait":             {Name: "subagent_wait", Kind: types.ToolKindControl, ReadOnly: true},
	"subagent_events":           {Name: "subagent_events", Kind: types.ToolKindControl, ReadOnly: true},
	"subagent_close":            {Name: "subagent_close", Kind: types.ToolKindControl, ReadOnly: true},
	"subagent_resume":           {Name: "subagent_resume", Kind: types.ToolKindControl, ReadOnly: true},
	"subagent_apply_worktree":   {Name: "subagent_apply_worktree", Kind: types.ToolKindControl},
	"subagent_discard_worktree": {Name: "subagent_discard_worktree", Kind: types.ToolKindControl},
	"supervision_snapshot":      {Name: "supervision_snapshot", Kind: types.ToolKindRead, ReadOnly: true},
	"supervision_descendants":   {Name: "supervision_descendants", Kind: types.ToolKindRead, ReadOnly: true},
	"read_agent_result":         {Name: "read_agent_result", Kind: types.ToolKindRead, ReadOnly: true},
	"subagent_status":           {Name: "subagent_status", Kind: types.ToolKindRead, ReadOnly: true},
	"subagent_inspect_task":     {Name: "subagent_inspect_task", Kind: types.ToolKindRead, ReadOnly: true},
	"subagent_ack_lifecycle":    {Name: "subagent_ack_lifecycle", Kind: types.ToolKindControl},
	"subagent_control":          {Name: "subagent_control", Kind: types.ToolKindControl},
	"team_spawn":                {Name: "team_spawn", Kind: types.ToolKindControl, ReadOnly: true},
	"team_wait":                 {Name: "team_wait", Kind: types.ToolKindControl, ReadOnly: true},
	"team_send":                 {Name: "team_send", Kind: types.ToolKindControl, ReadOnly: true},
	"team_mailbox":              {Name: "team_mailbox", Kind: types.ToolKindControl, ReadOnly: true},
	"team_task_spec":            {Name: "team_task_spec", Kind: types.ToolKindControl, ReadOnly: true},
	"team_task_context":         {Name: "team_task_context", Kind: types.ToolKindControl, ReadOnly: true},
	"team_task_report":          {Name: "team_task_report", Kind: types.ToolKindControl, ReadOnly: true},
	"team_task_block":           {Name: "team_task_block", Kind: types.ToolKindControl, ReadOnly: true},
	"schedule_create":           {Name: "schedule_create", Kind: types.ToolKindControl},
	"schedule_list":             {Name: "schedule_list", Kind: types.ToolKindControl, ReadOnly: true},
	"schedule_update":           {Name: "schedule_update", Kind: types.ToolKindControl},
	"schedule_delete":           {Name: "schedule_delete", Kind: types.ToolKindControl},
	"todos":                     {Name: "todos", Kind: types.ToolKindControl, ReadOnly: true},
	"artifact_read":             {Name: "artifact_read", Kind: types.ToolKindRead, ReadOnly: true},
	"get_goal":                  {Name: "get_goal", Kind: types.ToolKindControl, ReadOnly: true},
	"read_goal":                 {Name: "read_goal", Kind: types.ToolKindControl, ReadOnly: true},
	"update_goal":               {Name: "update_goal", Kind: types.ToolKindControl, ReadOnly: true},
	// 符号索引（code.*）：条件注册的 workspace 本地索引读取，随 grep 语义归 read
	// 组（toolregistry/registry.go 的 GroupRead）。它们既无本表行、也没有
	// DefinitionMetadata，能力只能由名字子串决定 —— "code_search" 命中
	// Contains("search") 就被判成需要 network，把一个纯本地读取拖进派生能力面，
	// 禁网 profile 下 AllowCapabilities(child) 会误拒子代理（那条拒绝路径由
	// profileinput/disabled_capabilities_e2e_test.go 钉住）。本地性证据：
	// code_index_resolver.go 只按 workspace root 解析、无 root 即 fail closed，
	// 语义通道借本地语言服务器进程，不触网。
	"code_search":           {Name: "code_search", Kind: types.ToolKindRead, ReadOnly: true},
	"code_inspect":          {Name: "code_inspect", Kind: types.ToolKindRead, ReadOnly: true},
	"code_navigate":         {Name: "code_navigate", Kind: types.ToolKindRead, ReadOnly: true},
	"code_references":       {Name: "code_references", Kind: types.ToolKindRead, ReadOnly: true},
	"code_callers":          {Name: "code_callers", Kind: types.ToolKindRead, ReadOnly: true},
	"openai_image_generate": {Name: "openai_image_generate", Kind: types.ToolKindNetwork, RequiresNet: true},
}

// LookupToolTaxonomy returns built-in taxonomy for a tool name when known.
//
// 归一到**规范名**（toolregistry.Canonical：别名与退役名一并折叠），因此表里只登记
// 规范名一行。别名与旧拼写仍然可解析——这正是删掉别名行的前提：已落盘的
// profile / grant 写的是 read / rg / bash / execute_shell_command 也照样命中。
func LookupToolTaxonomy(toolName string) (ToolTaxonomy, bool) {
	name := canonicalToolName(toolName)
	tax, ok := knownToolTaxonomy[name]
	return tax, ok
}

// KnownToolTaxonomyNames returns the sorted names registered in the built-in
// taxonomy (the same table `profile validate` consults). It is the single
// source for "全量工具清单" counts used by the quantified profile acceptance
// (实施方案 §1.3 第 1 条)；调用方不得再维护第二份名单。
func KnownToolTaxonomyNames() []string {
	names := make([]string, 0, len(knownToolTaxonomy))
	for name := range knownToolTaxonomy {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
	// Control-plane tools consult the shared table first: their capability needs
	// are not derivable from ReadOnly/Kind heuristics below (a control write is
	// not a filesystem write, and Kind alone cannot express agent_management).
	if caps, ok := controlPlaneToolCapabilities(name); ok {
		return caps
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
