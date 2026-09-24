package profileinput

import (
	"fmt"
	"sort"
	"strings"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// BuildSkillFilter converts a profile skill selection into a loader name
// predicate. It returns nil when no selection was declared so callers keep the
// unfiltered (pre-profile) behavior byte-for-byte (NFR-1).
//
// The allow/deny semantics (deny wins, empty allowlist = all, "*" wildcard,
// case-insensitive) live in internal/profile; this helper only adapts the
// mirror type and never re-implements them.
func BuildSkillFilter(selection ResolvedSkillSelection) func(string) bool {
	authority := profilesys.ResolvedSkillSelection{
		Allowlist: append([]string(nil), selection.Allowlist...),
		Denylist:  append([]string(nil), selection.Denylist...),
	}
	if authority.Empty() {
		return nil
	}
	return authority.AllowsSkill
}

// ApplyMCPSelection narrows an MCP config snapshot to the servers allowed by
// the profile declaration. The input config is never mutated: an active
// selection returns a clone (so shared configs are unaffected); an empty
// selection returns the input untouched.
//
// The second return value lists the dropped server names for diagnostics.
func ApplyMCPSelection(cfg *mcpconfig.Config, selection ResolvedMCPSelection) (*mcpconfig.Config, []string) {
	if cfg == nil {
		return nil, nil
	}
	authority := profilesys.ResolvedMCPSelection{
		UseServers:     append([]string(nil), selection.UseServers...),
		ExcludeServers: append([]string(nil), selection.ExcludeServers...),
	}
	if authority.Empty() {
		return cfg, nil
	}

	filtered := mcpconfig.CloneWithDefaults(cfg)
	if filtered.MCPServers == nil {
		return filtered, nil
	}
	dropped := make([]string, 0)
	for name := range filtered.MCPServers {
		if authority.AllowsServer(name) {
			continue
		}
		delete(filtered.MCPServers, name)
		dropped = append(dropped, name)
	}
	sort.Strings(dropped)
	return filtered, dropped
}

// LoadMCPConfigSnapshot loads an MCP config file and applies the profile server
// selection, producing the in-memory snapshot consumed by
// manager.LoadConfigFromConfig. An empty path yields (nil, nil, nil).
func LoadMCPConfigSnapshot(path string, selection ResolvedMCPSelection) (*mcpconfig.Config, []string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil, nil
	}
	cfg, err := mcpconfig.NewLoader(path).Load()
	if err != nil {
		return nil, nil, fmt.Errorf("load mcp config %s: %w", path, err)
	}
	filtered, dropped := ApplyMCPSelection(cfg, selection)
	return filtered, dropped, nil
}

// ComposeProfileSystemPrompt applies the profile prompt composition mode to the
// host system prompt:
//
//   - "replace" (default): the profile prompt replaces the host prompt, which
//     is the pre-profile behavior.
//   - "append": the profile prompt is appended after the host prompt.
//
// An unknown mode is an error (never silently reinterpreted).
func ComposeProfileSystemPrompt(hostPrompt, profilePrompt, mode string) (string, error) {
	normalized, ok := profilesys.NormalizePromptMode(mode)
	if !ok {
		return "", fmt.Errorf("%w: prompts.mode %q", profilesys.ErrInvalidProfileSpec, mode)
	}
	hostPrompt = strings.TrimSpace(hostPrompt)
	profilePrompt = strings.TrimSpace(profilePrompt)

	if normalized == profilesys.PromptModeAppend && hostPrompt != "" {
		if profilePrompt == "" {
			return hostPrompt, nil
		}
		return hostPrompt + "\n\n" + profilePrompt, nil
	}
	if profilePrompt == "" {
		return hostPrompt, nil
	}
	return profilePrompt, nil
}

// ProfilePromptModeIsAppend reports whether a declared prompt mode means
// "append profile text after the built-in base prompt" (FR-7).
//
// It delegates to the profile package so the CLI/server never re-implement the
// mode dialect; unknown values are treated as the default replace mode, which
// matches the resolver's validated output.
func ProfilePromptModeIsAppend(mode string) bool {
	normalized, ok := profilesys.NormalizePromptMode(mode)
	return ok && normalized == profilesys.PromptModeAppend
}
