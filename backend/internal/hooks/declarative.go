package hooks

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Codex-compatible declarative hooks support.
//
// Codex discovers hooks from two representations per config layer:
//   1. a hooks.json file in the layer's config folder, and
//   2. a top-level hooks table in config.toml.
// This file implements the equivalent read side for the runtime:
//   - `.aicli/hooks.json` (project scope)
//   - `~/.aicli/hooks.json` (user scope)
//   - a plugin manifest's hooks file (plugin scope)
//   - runtime.yaml `hooks:` (native scope, already supported)
//
// The wire format follows Codex/Claude conventions:
//
//   {
//     "description": "...",
//     "hooks": {
//       "PreToolUse": [
//         {"matcher": "Bash|Write", "hooks": [
//           {"type": "command", "command": "...", "timeout": 30,
//            "async": false, "statusMessage": "checking..."}
//         ]}
//       ]
//     }
//   }
//
// The legacy runtime format ({hooks: [HookConfig...]}) and a bare array are
// still accepted, so existing files keep loading.

// HookScope identifies the trust/discovery layer of a handler.
type HookScope string

const (
	ScopeRuntime HookScope = "runtime"
	ScopeUser    HookScope = "user"
	ScopeProject HookScope = "project"
	ScopePlugin  HookScope = "plugin"
	ScopeManaged HookScope = "managed"
)

// DiscoverOptions controls declarative hook discovery.
type DiscoverOptions struct {
	// ProjectRoot is the workspace root. Empty means the current directory.
	ProjectRoot string
	// UserHome overrides the user home directory (tests / portable installs).
	UserHome string
	// SkipProject disables project-scope discovery (folder-trust denial).
	SkipProject bool
	// SkipUser disables user-scope discovery.
	SkipUser bool
	// ExtraFiles are explicit hook files to load in addition to standard
	// scopes. Each file is attributed to ScopeRuntime.
	ExtraFiles []string
	// PluginFiles are hook files contributed by trusted+enabled plugins.
	PluginFiles []PluginHookFile
	// ManagedFiles are administrator-controlled hook files. They are always
	// loaded first and are the only files considered when AllowManagedOnly is
	// set.
	ManagedFiles []string
	// AllowManagedOnly mirrors Codex's allow_managed_hooks_only: when true,
	// user/project/plugin/extra hooks are ignored.
	AllowManagedOnly bool
	// DisabledIDs disables matching handler IDs. IDs are stable:
	//   <scope>:<source_path>:<event>:<group_index>:<handler_index>
	DisabledIDs map[string]bool
	// Warnings receives non-fatal parse/load issues. The caller owns the slice.
	Warnings *[]string
}

// PluginHookFile is one declarative hooks file contributed by a plugin.
type PluginHookFile struct {
	Path     string
	PluginID string
}

// DiscoverResult is the merged, normalized declarative hook set.
type DiscoverResult struct {
	Hooks    []HookConfig
	Warnings []string
	// Sources lists every file that was considered, in load order.
	Sources []string
}

// codexHooksDocument mirrors the Codex hooks.json wire shape.
type codexHooksDocument struct {
	Description string                 `json:"description" yaml:"description"`
	Hooks       map[string][]HookGroup `json:"hooks" yaml:"hooks"`
	State       map[string]HookState   `json:"state" yaml:"state"`
}

// HookGroup is one matcher group.
type HookGroup struct {
	Matcher string             `json:"matcher" yaml:"matcher"`
	Hooks   []codexHookHandler `json:"hooks" yaml:"hooks"`
}

// codexHookHandler is one handler inside a matcher group.
type codexHookHandler struct {
	Type                   string                 `json:"type" yaml:"type"`
	Command                string                 `json:"command" yaml:"command"`
	CommandWindows         string                 `json:"commandWindows" yaml:"commandWindows"`
	Timeout                *float64               `json:"timeout" yaml:"timeout"`
	Async                  bool                   `json:"async" yaml:"async"`
	StatusMessage          string                 `json:"statusMessage" yaml:"statusMessage"`
	AdditionalContextLimit *int                   `json:"additionalContextLimit" yaml:"additionalContextLimit"`
	Server                 string                 `json:"server" yaml:"server"`
	Tool                   string                 `json:"tool" yaml:"tool"`
	Input                  map[string]interface{} `json:"input" yaml:"input"`
}

// legacyHooksDocument is the original runtime format.
type legacyHooksDocument struct {
	Hooks []HookConfig `json:"hooks" yaml:"hooks"`
}

// eventNameToEvent maps Codex's PascalCase event names to the runtime's
// canonical snake_case event identifiers. Matching is case-insensitive for the
// native names so both spellings are accepted.
var codexEventAliases = map[string]Event{
	"pretooluse":        EventPreToolUse,
	"permissionrequest": EventPermissionRequest,
	"posttooluse":       EventPostToolUse,
	"precompact":        EventPreCompact,
	"postcompact":       EventPostCompact,
	"sessionstart":      EventSessionStart,
	"sessionend":        EventSessionEnd,
	"userpromptsubmit":  EventUserPromptSubmit,
	"subagentstart":     EventSubagentStart,
	"subagentstop":      EventSubagentStop,
	"stop":              EventStop,
	"interrupt":         EventInterrupt,
}

// ParseEventName normalizes a Codex or native event name.
func ParseEventName(raw string) (Event, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	key := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(trimmed))
	if event, ok := codexEventAliases[key]; ok {
		return event, true
	}
	// Accept native canonical names verbatim (e.g. post_tool_use_failure).
	native := Event(strings.ToLower(trimmed))
	switch native {
	case EventSessionStart, EventSessionEnd, EventUserPromptSubmit,
		EventPreToolUse, EventPermissionRequest, EventPostToolUse,
		EventPostToolUseFailure, EventSubagentStart, EventSubagentStop,
		EventStop, EventStopFailure, EventPreCompact, EventPostCompact,
		EventCheckpointCreated, EventRewindCompleted, EventBacktrackCompleted,
		EventInterrupt:
		return native, true
	}
	return "", false
}

// LoadDeclarativeFile parses one hooks file. Format is inferred from the file
// extension; .json uses JSON, everything else tries YAML then JSON.
func LoadDeclarativeFile(path string, scope HookScope, pluginID string) ([]HookConfig, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, nil, nil
	}

	var warnings []string
	// Legacy formats first: a bare array or {hooks:[...]} where hooks is a list.
	if hooks, ok := parseLegacyHooks(trimmed, filepath.Ext(path)); ok {
		return stampHooks(hooks, scope, path, pluginID), warnings, nil
	}

	var doc codexHooksDocument
	if err := unmarshalHookDocument(path, data, &doc); err != nil {
		return nil, warnings, fmt.Errorf("parse hooks file %s: %w", path, err)
	}
	if len(doc.Hooks) == 0 {
		return nil, warnings, nil
	}

	// Deterministic event order: stable load order matters for "first block
	// wins" and for reproducible tests. Codex uses a fixed event array; we sort
	// by canonical event name and then group index.
	eventNames := make([]string, 0, len(doc.Hooks))
	for name := range doc.Hooks {
		eventNames = append(eventNames, name)
	}
	sort.SliceStable(eventNames, func(i, j int) bool {
		left, _ := ParseEventName(eventNames[i])
		right, _ := ParseEventName(eventNames[j])
		return string(left) < string(right)
	})

	absPath, _ := filepath.Abs(path)
	var hooks []HookConfig
	for _, eventName := range eventNames {
		event, ok := ParseEventName(eventName)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("unknown hook event %q in %s", eventName, path))
			continue
		}
		for groupIndex, group := range doc.Hooks[eventName] {
			for handlerIndex, handler := range group.Hooks {
				config, skipReason := normalizeDeclarativeHandler(
					event, group.Matcher, handler, absPath, scope, pluginID,
					groupIndex, handlerIndex,
				)
				if skipReason != "" {
					warnings = append(warnings, skipReason)
					continue
				}
				hooks = append(hooks, config)
			}
		}
	}
	return hooks, warnings, nil
}

func normalizeDeclarativeHandler(
	event Event,
	matcher string,
	handler codexHookHandler,
	sourcePath string,
	scope HookScope,
	pluginID string,
	groupIndex, handlerIndex int,
) (HookConfig, string) {
	handlerType := strings.ToLower(strings.TrimSpace(handler.Type))
	if handlerType == "" {
		handlerType = "command"
	}
	config := HookConfig{
		Event:         event,
		Matcher:       strings.TrimSpace(matcher),
		Async:         handler.Async,
		StatusMessage: strings.TrimSpace(handler.StatusMessage),
		Source:        string(scope),
		SourcePath:    sourcePath,
		OnError:       "fail_open",
	}
	if handler.Timeout != nil && *handler.Timeout > 0 {
		config.Timeout = durationFromSeconds(*handler.Timeout)
	}
	if handler.AdditionalContextLimit != nil {
		config.AdditionalContextLimit = *handler.AdditionalContextLimit
	}
	if pluginID != "" {
		config.Source = string(scope) + ":" + pluginID
	}
	config.ID = fmt.Sprintf("%s:%s:%s:%d:%d", scope, sourcePath, event, groupIndex, handlerIndex)

	switch handlerType {
	case "command":
		command := handler.Command
		if runtime.GOOS == "windows" && strings.TrimSpace(handler.CommandWindows) != "" {
			command = handler.CommandWindows
		}
		if strings.TrimSpace(command) == "" {
			return HookConfig{}, fmt.Sprintf("skipping empty hook command in %s", sourcePath)
		}
		config.Exec = ExecConfig{Type: "shell", Command: command}
	case "mcp_tool", "mcp-tool":
		if strings.TrimSpace(handler.Server) == "" || strings.TrimSpace(handler.Tool) == "" {
			return HookConfig{}, fmt.Sprintf("skipping MCP tool hook in %s: server and tool are required", sourcePath)
		}
		config.Exec = ExecConfig{
			Type:   "mcp",
			Server: strings.TrimSpace(handler.Server),
			Tool:   strings.TrimSpace(handler.Tool),
			Input:  handler.Input,
		}
	default:
		return HookConfig{}, fmt.Sprintf("skipping hook %s in %s: unsupported type %q", config.ID, sourcePath, handler.Type)
	}
	return config, ""
}

func parseLegacyHooks(trimmed string, ext string) ([]HookConfig, bool) {
	// Bare array.
	var list []HookConfig
	if strings.HasPrefix(trimmed, "[") && json.Unmarshal([]byte(trimmed), &list) == nil {
		return list, true
	}
	// {hooks: [...]}.
	var doc legacyHooksDocument
	var err error
	if strings.EqualFold(ext, ".json") {
		err = json.Unmarshal([]byte(trimmed), &doc)
	} else {
		err = yaml.Unmarshal([]byte(trimmed), &doc)
		if err != nil {
			err = json.Unmarshal([]byte(trimmed), &doc)
		}
	}
	if err == nil && len(doc.Hooks) > 0 {
		return doc.Hooks, true
	}
	return nil, false
}

func unmarshalHookDocument(path string, data []byte, dest *codexHooksDocument) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return json.Unmarshal(data, dest)
	default:
		if err := yaml.Unmarshal(data, dest); err != nil {
			return json.Unmarshal(data, dest)
		}
		return nil
	}
}

func stampHooks(hooks []HookConfig, scope HookScope, path, pluginID string) []HookConfig {
	absPath, _ := filepath.Abs(path)
	source := string(scope)
	if pluginID != "" {
		source += ":" + pluginID
	}
	out := make([]HookConfig, 0, len(hooks))
	for index, hook := range hooks {
		if strings.TrimSpace(hook.ID) == "" {
			hook.ID = fmt.Sprintf("%s:%s:%s:%d", scope, absPath, hook.Event, index)
		}
		if strings.TrimSpace(hook.Source) == "" {
			hook.Source = source
		}
		if strings.TrimSpace(hook.SourcePath) == "" {
			hook.SourcePath = absPath
		}
		if strings.TrimSpace(hook.OnError) == "" {
			hook.OnError = "fail_open"
		}
		out = append(out, hook)
	}
	return out
}

func durationFromSeconds(seconds float64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// DiscoverDeclarativeHooks loads hooks from standard scopes.
//
// Load order is low-to-high precedence: user -> project -> plugin -> explicit
// extras. Later handlers do not replace earlier ones; the manager evaluates all
// matching handlers in order and the first blocking decision wins.
func DiscoverDeclarativeHooks(opts DiscoverOptions) DiscoverResult {
	var result DiscoverResult
	appendWarnings := func(warnings []string) {
		result.Warnings = append(result.Warnings, warnings...)
		if opts.Warnings != nil {
			*opts.Warnings = append(*opts.Warnings, warnings...)
		}
	}
	load := func(path string, scope HookScope, pluginID string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return
		}
		result.Sources = append(result.Sources, path)
		hooks, warnings, err := LoadDeclarativeFile(path, scope, pluginID)
		appendWarnings(warnings)
		if err != nil {
			appendWarnings([]string{err.Error()})
			return
		}
		for _, hook := range hooks {
			if opts.DisabledIDs != nil && opts.DisabledIDs[hook.ID] {
				continue
			}
			if hook.Enabled != nil && !*hook.Enabled {
				continue
			}
			result.Hooks = append(result.Hooks, hook)
		}
	}

	for _, managed := range opts.ManagedFiles {
		load(managed, ScopeManaged, "")
	}
	if !opts.AllowManagedOnly && !opts.SkipUser {
		home := strings.TrimSpace(opts.UserHome)
		if home == "" {
			if resolved, err := os.UserHomeDir(); err == nil {
				home = resolved
			}
		}
		if home != "" {
			for _, name := range []string{"hooks.json", "hooks.yaml", "hooks.yml"} {
				load(filepath.Join(home, ".aicli", name), ScopeUser, "")
			}
		}
	}

	if !opts.AllowManagedOnly && !opts.SkipProject {
		root := strings.TrimSpace(opts.ProjectRoot)
		if root == "" {
			root, _ = os.Getwd()
		}
		if root != "" {
			for _, rel := range []string{
				filepath.Join(".aicli", "hooks.json"),
				filepath.Join(".aicli", "hooks.yaml"),
				filepath.Join(".aicli", "hooks.yml"),
				"hooks.json",
				"hooks.yaml",
				"hooks.yml",
			} {
				load(filepath.Join(root, rel), ScopeProject, "")
			}
		}
	}

	if !opts.AllowManagedOnly {
		for _, plugin := range opts.PluginFiles {
			load(plugin.Path, ScopePlugin, plugin.PluginID)
		}
		for _, extra := range opts.ExtraFiles {
			load(extra, ScopeRuntime, "")
		}
	}
	// Apply persisted enable/trust overlays from each source file's `state`
	// map. This mirrors Codex's hooks state: a handler can be disabled without
	// deleting its declaration.
	for _, source := range result.Sources {
		states, err := LoadHookStatesFile(source)
		if err != nil || len(states) == 0 {
			continue
		}
		var stateWarnings []string
		result.Hooks, stateWarnings = resolveHookStates(result.Hooks, states)
		appendWarnings(stateWarnings)
	}
	return result
}

// LoadHookStatesFile reads the Codex-style `state` map from a hooks file.
// Missing files return an empty map. Both hooks.json and hooks.yaml work.
func LoadHookStatesFile(path string) (map[string]HookState, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return map[string]HookState{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]HookState{}, nil
		}
		return nil, err
	}
	var doc codexHooksDocument
	if err := unmarshalHookDocument(path, data, &doc); err != nil {
		return nil, fmt.Errorf("parse hooks state %s: %w", path, err)
	}
	if doc.State == nil {
		return map[string]HookState{}, nil
	}
	return doc.State, nil
}

// ResolveHookStates applies a Codex-style state map (enabled/trusted_hash) to
// handlers. A non-empty trusted_hash must match the handler's executable
// declaration; mismatches are dropped by DiscoverDeclarativeHooks with a
// warning. Unknown IDs are ignored.
func ResolveHookStates(hooks []HookConfig, states map[string]HookState) []HookConfig {
	out, _ := resolveHookStates(hooks, states)
	return out
}

func resolveHookStates(hooks []HookConfig, states map[string]HookState) ([]HookConfig, []string) {
	if len(hooks) == 0 || len(states) == 0 {
		return hooks, nil
	}
	out := make([]HookConfig, 0, len(hooks))
	var warnings []string
	for _, hook := range hooks {
		if state, ok := states[hook.ID]; ok {
			if state.TrustedHash != "" && state.TrustedHash != handlerTrustHash(hook) {
				warnings = append(warnings, fmt.Sprintf("hook %s disabled: trusted_hash mismatch", hook.ID))
				continue
			}
			if state.Enabled != nil {
				enabled := *state.Enabled
				hook.Enabled = &enabled
			}
		}
		if hook.Enabled != nil && !*hook.Enabled {
			continue
		}
		out = append(out, hook)
	}
	return out, warnings
}

// handlerTrustHash is a stable content hash of the executable declaration.
// It intentionally excludes display-only fields (status message, source path)
// so cosmetic edits do not invalidate trust.
func handlerTrustHash(hook HookConfig) string {
	payload, _ := json.Marshal(struct {
		Event Event      `json:"event"`
		Match string     `json:"matcher"`
		Exec  ExecConfig `json:"exec"`
		Async bool       `json:"async"`
		OnErr string     `json:"on_error"`
	}{
		Event: hook.Event,
		Match: hook.Matcher,
		Exec:  hook.Exec,
		Async: hook.Async,
		OnErr: hook.OnError,
	})
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%x", sum)
}

// MergeDeclarativeHooks appends discovered declarative handlers after native
// runtime handlers. Native handlers keep precedence because they are evaluated
// first; a declarative block cannot be overridden by a later allow.
func MergeDeclarativeHooks(base []HookConfig, discovered DiscoverResult) []HookConfig {
	if len(discovered.Hooks) == 0 {
		return base
	}
	out := make([]HookConfig, 0, len(base)+len(discovered.Hooks))
	out = append(out, base...)
	out = append(out, discovered.Hooks...)
	return out
}

// HostSources is the single host-facing merge input. Hosts only collect
// environment-specific pieces (project root, plugin files, legacy plugin
// hooks, managed files); all ordering/precedence/trust rules live here.
type HostSources struct {
	Base              []HookConfig
	ProjectRoot       string
	SkipProject       bool
	PluginFiles       []PluginHookFile
	LegacyPluginHooks []HookConfig
	ManagedFiles      []string
	AllowManagedOnly  bool
}

// MergeForHost is the single merge entry used by aicli, runtime-server, and
// diagnostics. It centralizes:
//   - managed-only suppression of every non-managed source;
//   - discovery of user/project/plugin/managed hooks;
//   - native runtime.yaml precedence (base first);
//   - legacy plugin hooks (trusted+enabled) appended last.
func MergeForHost(src HostSources) []HookConfig {
	merged, _ := ResolveForHost(src)
	return merged
}

// ResolveForHost returns both the merged handler list and the discovery result
// (sources/warnings). Hosts that render diagnostics should call this once
// instead of DiscoverForHost + MergeForHost.
func ResolveForHost(src HostSources) ([]HookConfig, DiscoverResult) {
	discovered := DiscoverForHost(src)
	base := src.Base
	legacy := src.LegacyPluginHooks
	if src.AllowManagedOnly {
		base = nil
		legacy = nil
	}
	merged := MergeDeclarativeHooks(base, discovered)
	if len(legacy) > 0 {
		merged = append(merged, legacy...)
	}
	return merged, discovered
}

// DiscoverForHost runs the shared discovery half of MergeForHost. Diagnostics
// use it when they need Sources/Warnings but not the final merged list.
func DiscoverForHost(src HostSources) DiscoverResult {
	pluginFiles := src.PluginFiles
	if src.AllowManagedOnly {
		pluginFiles = nil
	}
	return DiscoverDeclarativeHooks(DiscoverOptions{
		ProjectRoot:      src.ProjectRoot,
		SkipProject:      src.SkipProject,
		PluginFiles:      pluginFiles,
		ManagedFiles:     src.ManagedFiles,
		AllowManagedOnly: src.AllowManagedOnly,
	})
}

// MergeForHostFromEnv fills managed/allow-managed-only from the environment.
func MergeForHostFromEnv(src HostSources) []HookConfig {
	src.ManagedFiles = ManagedHookFilesFromEnv()
	src.AllowManagedOnly = AllowManagedHooksOnlyFromEnv()
	return MergeForHost(src)
}

// ResolveForHostFromEnv is the diagnostics-friendly variant.
func ResolveForHostFromEnv(src HostSources) ([]HookConfig, DiscoverResult) {
	src.ManagedFiles = ManagedHookFilesFromEnv()
	src.AllowManagedOnly = AllowManagedHooksOnlyFromEnv()
	return ResolveForHost(src)
}

// HookState is the persisted enable/trust overlay for one handler.
type HookState struct {
	Enabled     *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	TrustedHash string `json:"trusted_hash,omitempty" yaml:"trusted_hash,omitempty"`
}

// ManagedHookFilesFromEnv returns administrator-managed hook files from
// AICLI_MANAGED_HOOKS_FILES (path-list separated by the OS list separator).
func ManagedHookFilesFromEnv() []string {
	raw := strings.TrimSpace(os.Getenv("AICLI_MANAGED_HOOKS_FILES"))
	if raw == "" {
		return nil
	}
	var out []string
	for _, item := range filepath.SplitList(raw) {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// AllowManagedHooksOnlyFromEnv mirrors Codex's allow_managed_hooks_only.
func AllowManagedHooksOnlyFromEnv() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("AICLI_ALLOW_MANAGED_HOOKS_ONLY")))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}
