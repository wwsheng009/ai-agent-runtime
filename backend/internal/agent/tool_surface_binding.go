package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ToolDefinitionsFingerprint returns a stable content hash for a tool schema
// surface. Order is significant so prompt-cache / HTTP tool order changes are
// visible in debug metadata.
func ToolDefinitionsFingerprint(tools []types.ToolDefinition) string {
	if len(tools) == 0 {
		return ""
	}
	type fingerprintTool struct {
		Name        string                 `json:"name,omitempty"`
		Description string                 `json:"description,omitempty"`
		Parameters  map[string]interface{} `json:"parameters,omitempty"`
	}
	payload := make([]fingerprintTool, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		payload = append(payload, fingerprintTool{
			Name:        name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		})
	}
	if len(payload) == 0 {
		return ""
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:])
}

// BuildToolSurfaceEligibilityKey captures the inputs that authorize a durable
// session-stable tool surface. When any of these change, StableToolSurface must
// be discarded and re-frozen instead of reused across turns.
//
// Goal-specific projection is intentionally excluded: session stability is for
// schema continuity, while per-turn goal projection only applies on first freeze.
func BuildToolSurfaceEligibilityKey(permissionMode string, policy *ToolExecutionPolicy, catalog []types.ToolDefinition) string {
	type keyPayload struct {
		PermissionMode string                 `json:"permission_mode,omitempty"`
		Policy         map[string]interface{} `json:"policy,omitempty"`
		Catalog        string                 `json:"catalog,omitempty"`
	}
	payload := keyPayload{
		PermissionMode: strings.TrimSpace(permissionMode),
		Catalog:        ToolDefinitionsFingerprint(sortToolDefinitionsCopy(catalog)),
	}
	if policy != nil {
		policyMap := map[string]interface{}{
			"read_only":            policy.ReadOnly,
			"allowlist_enabled":    policy.AllowlistEnabled,
			"block_untrusted_mcp":  policy.BlockUntrustedMCP,
			"block_remote_writes":  policy.BlockRemoteWrites,
			"capability_scope_on":  policy.CapabilityScopeEnabled,
			"allowed_tools":        policy.AllowedToolNames(),
			"denied_tools":         sortedPolicyToolNames(policy.DeniedTools),
			"allowed_capabilities": sortedCapabilityNames(policy),
		}
		payload.Policy = policyMap
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:])
}

// CollectToolCatalogDefinitions returns the pre-freeze tool catalog that feeds
// eligibility binding. It intentionally skips goal projection and annotation
// compaction so only policy / MCP / broker changes affect the key.
func (a *Agent) CollectToolCatalogDefinitions(ctx context.Context) []types.ToolDefinition {
	if a == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tools := make([]types.ToolDefinition, 0, 8)
	seen := make(map[string]bool)

	if a.mcpManager != nil {
		for _, mt := range a.mcpManager.ListTools() {
			if policy := a.GetToolExecutionPolicy(); policy != nil && policy.AllowToolInfo(mt) != nil {
				continue
			}
			if seen[mt.Name] {
				continue
			}
			seen[mt.Name] = true
			tools = append(tools, types.ToolDefinition{
				Name:        mt.Name,
				Description: mt.Description,
				Parameters:  normalizeToolParameters(mt.InputSchema),
			})
		}
	}

	if a.GetSubagentScheduler() != nil {
		if a.GetToolExecutionPolicy() == nil || a.GetToolExecutionPolicy().AllowsDefinition("spawn_subagents") {
			definition := spawnSubagentsToolDefinition()
			if !seen[definition.Name] {
				seen[definition.Name] = true
				tools = append(tools, definition)
			}
		}
	}

	if broker := a.GetToolBroker(); broker != nil {
		for _, def := range broker.DefinitionsForContext(ctx) {
			if policy := a.GetToolExecutionPolicy(); policy != nil && !policy.AllowsDefinition(def.Name) {
				continue
			}
			if seen[def.Name] {
				continue
			}
			seen[def.Name] = true
			tools = append(tools, def)
		}
	}

	tools = optimizeModelToolSurface(tools)
	sortToolDefinitionsByName(tools)
	return tools
}

// EligibilityKeyForAgent builds the session-stable binding for the agent's
// current policy and live tool catalog.
func (a *Agent) EligibilityKeyForAgent(ctx context.Context, permissionMode string) string {
	return BuildToolSurfaceEligibilityKey(permissionMode, a.GetToolExecutionPolicy(), a.CollectToolCatalogDefinitions(ctx))
}

func sortToolDefinitionsCopy(tools []types.ToolDefinition) []types.ToolDefinition {
	if len(tools) == 0 {
		return nil
	}
	cloned := cloneToolDefinitions(tools)
	sortToolDefinitionsByName(cloned)
	return cloned
}

func sortedPolicyToolNames(values map[string]bool) []string {
	if len(values) == 0 {
		return nil
	}
	names := make([]string, 0, len(values))
	for name, enabled := range values {
		if !enabled {
			continue
		}
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			names = append(names, trimmed)
		}
	}
	sort.Strings(names)
	return names
}

func sortedCapabilityNames(policy *ToolExecutionPolicy) []string {
	if policy == nil || !policy.CapabilityScopeEnabled || len(policy.AllowedCapabilities) == 0 {
		return nil
	}
	names := make([]string, 0, len(policy.AllowedCapabilities))
	for capability, allowed := range policy.AllowedCapabilities {
		if !allowed {
			continue
		}
		if text := strings.TrimSpace(string(capability)); text != "" {
			names = append(names, text)
		}
	}
	sort.Strings(names)
	return names
}
