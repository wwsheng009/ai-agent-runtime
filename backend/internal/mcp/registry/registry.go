//go:build !win7compat

package registry

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/client"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolschema"
)

const maxCanonicalToolNameLength = 64

// ToolInfo 工具信息
type ToolInfo struct {
	Tool     *protocol.Tool
	MCPName  string
	Enabled  bool
	Metadata map[string]interface{}
}

// QuarantinedToolInfo records an externally supplied tool that failed schema
// validation. It is retained for diagnostics but never exposed for execution.
type QuarantinedToolInfo struct {
	MCPName    string
	ToolName   string
	SchemaHash string
	Error      string
}

// AmbiguousToolError is returned when a short MCP tool name maps to more than
// one enabled server. Callers must choose a canonical name instead.
type AmbiguousToolError struct {
	Name       string
	Candidates []string
}

func (e *AmbiguousToolError) Error() string {
	if e == nil {
		return "ambiguous MCP tool"
	}
	return fmt.Sprintf("ambiguous MCP tool %q; use one of: %s", e.Name, strings.Join(e.Candidates, ", "))
}

// IsAmbiguousToolError reports whether err is the fail-closed short-name error.
func IsAmbiguousToolError(err error) bool {
	var target *AmbiguousToolError
	return errors.As(err, &target)
}

// Registry MCP 工具注册表
type Registry struct {
	mu          sync.RWMutex
	tools       map[string]*ToolInfo // key: mcp name + NUL + raw tool name
	quarantined map[string]*QuarantinedToolInfo
	mcps        map[string]client.Client
}

// NewRegistry 创建注册表
func NewRegistry() *Registry {
	return &Registry{
		tools:       make(map[string]*ToolInfo),
		quarantined: make(map[string]*QuarantinedToolInfo),
		mcps:        make(map[string]client.Client),
	}
}

// RegisterClient 注册 MCP 客户端
func (r *Registry) RegisterClient(name string, cli client.Client) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.mcps[name] = cli
}

// UnregisterClient 注销 MCP 客户端
func (r *Registry) UnregisterClient(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 移除客户端
	delete(r.mcps, name)

	// 移除相关工具
	for key, info := range r.tools {
		if info.MCPName == name {
			delete(r.tools, key)
		}
	}
	for key, info := range r.quarantined {
		if info.MCPName == name {
			delete(r.quarantined, key)
		}
	}
}

// RegisterTool 注册工具
func (r *Registry) RegisterTool(mcpName string, tool *protocol.Tool, enabled bool) error {
	mcpName = strings.TrimSpace(mcpName)
	if mcpName == "" {
		return fmt.Errorf("MCP name is required")
	}
	if tool == nil {
		return fmt.Errorf("MCP tool is nil")
	}
	toolName := strings.TrimSpace(tool.Name)
	if toolName == "" {
		return fmt.Errorf("MCP tool name is required")
	}

	canonical, warnings, err := toolschema.CanonicalizeAndValidate(tool.InputSchema)
	if err != nil {
		r.quarantineTool(mcpName, toolName, tool.InputSchema, err)
		return fmt.Errorf("invalid schema for MCP tool %s/%s: %w", mcpName, toolName, err)
	}
	metadata := cloneMetadata(tool.Metadata)
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	metadata["mcp_name"] = mcpName
	metadata["mcp_raw_tool_name"] = toolName
	metadata["mcp_canonical_name"] = CanonicalToolName(mcpName, toolName)
	if schemaHash, hashErr := toolschema.Hash(canonical); hashErr == nil {
		metadata["tool_schema_hash"] = schemaHash
	}
	if len(warnings) > 0 {
		metadata["tool_schema_warnings"] = warnings
	}
	normalizedTool := &protocol.Tool{
		Name:        toolName,
		Description: tool.Description,
		InputSchema: canonical,
		Metadata:    cloneMetadata(metadata),
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := r.makeToolKey(mcpName, toolName)
	canonicalName := CanonicalToolName(mcpName, toolName)
	for existingKey, existing := range r.tools {
		if existingKey == key || existing == nil || existing.Tool == nil {
			continue
		}
		if CanonicalToolName(existing.MCPName, existing.Tool.Name) != canonicalName {
			continue
		}
		collisionErr := fmt.Errorf("canonical MCP tool name %q collides with %s/%s", canonicalName, existing.MCPName, existing.Tool.Name)
		delete(r.tools, key)
		r.quarantined[key] = &QuarantinedToolInfo{
			MCPName:    mcpName,
			ToolName:   toolName,
			SchemaHash: stringMetadata(metadata, "tool_schema_hash"),
			Error:      collisionErr.Error(),
		}
		return collisionErr
	}
	r.tools[key] = &ToolInfo{
		Tool:     normalizedTool,
		MCPName:  mcpName,
		Enabled:  enabled,
		Metadata: metadata,
	}
	delete(r.quarantined, key)
	return nil
}

// UnregisterTool 注销工具
func (r *Registry) UnregisterTool(mcpName, toolName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := r.makeToolKey(mcpName, toolName)
	delete(r.tools, key)
	delete(r.quarantined, key)
}

// ListTools 列出所有工具
func (r *Registry) ListTools() []*ToolInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]*ToolInfo, 0, len(r.tools))
	for _, info := range r.tools {
		if info.Enabled {
			tools = append(tools, cloneToolInfo(info))
		}
	}
	sort.Slice(tools, func(i, j int) bool {
		leftName, rightName := "", ""
		leftMCP, rightMCP := "", ""
		if tools[i] != nil {
			leftMCP = tools[i].MCPName
			if tools[i].Tool != nil {
				leftName = tools[i].Tool.Name
			}
		}
		if tools[j] != nil {
			rightMCP = tools[j].MCPName
			if tools[j].Tool != nil {
				rightName = tools[j].Tool.Name
			}
		}
		if leftName == rightName {
			return leftMCP < rightMCP
		}
		return leftName < rightName
	})
	return tools
}

// CanonicalToolName returns the provider-safe identity for an MCP tool.
func CanonicalToolName(mcpName, toolName string) string {
	canonical := "mcp__" + portableNamePart(mcpName) + "__" + portableNamePart(toolName)
	if len(canonical) <= maxCanonicalToolNameLength {
		return canonical
	}
	suffix := "_" + shortIdentityHash(mcpName+"\x00"+toolName)
	return canonical[:maxCanonicalToolNameLength-len(suffix)] + suffix
}

// CallableToolNames returns names aligned with tools. Unique raw names remain
// backward compatible; collisions are exposed only through canonical names.
func CallableToolNames(tools []*ToolInfo) []string {
	counts, canonicalNames := callableNameStats(tools)
	names := make([]string, len(tools))
	for index, info := range tools {
		if info == nil || info.Tool == nil {
			continue
		}
		names[index] = callableToolName(info, counts, canonicalNames)
	}
	return names
}

// CallableToolName returns one tool's projected name for the given inventory.
func CallableToolName(info *ToolInfo, tools []*ToolInfo) string {
	if info == nil || info.Tool == nil {
		return ""
	}
	counts, canonicalNames := callableNameStats(tools)
	return callableToolName(info, counts, canonicalNames)
}

// ExecutionLookupName returns the name adapters should pass to a Manager that
// resolves both raw and canonical identities. Raw names remain the default for
// compatibility. A raw name that equals another tool's canonical identity must
// use its own canonical identity so the resolver cannot select the other tool.
func ExecutionLookupName(info *ToolInfo, tools []*ToolInfo) string {
	if info == nil || info.Tool == nil {
		return ""
	}
	rawName := strings.TrimSpace(info.Tool.Name)
	for _, candidate := range tools {
		if candidate == nil || candidate.Tool == nil || !candidate.Enabled || candidate.MCPName != info.MCPName {
			continue
		}
		candidateRawName := strings.TrimSpace(candidate.Tool.Name)
		if candidateRawName != rawName && CanonicalToolName(candidate.MCPName, candidateRawName) == rawName {
			return CanonicalToolName(info.MCPName, rawName)
		}
	}
	return rawName
}

func callableNameStats(tools []*ToolInfo) (map[string]int, map[string]struct{}) {
	counts := make(map[string]int, len(tools))
	canonicalNames := make(map[string]struct{}, len(tools))
	for _, info := range tools {
		if info == nil || info.Tool == nil || !info.Enabled {
			continue
		}
		rawName := strings.TrimSpace(info.Tool.Name)
		counts[rawName]++
		canonicalNames[CanonicalToolName(info.MCPName, rawName)] = struct{}{}
	}
	return counts, canonicalNames
}

func callableToolName(info *ToolInfo, counts map[string]int, canonicalNames map[string]struct{}) string {
	rawName := strings.TrimSpace(info.Tool.Name)
	_, shadowsCanonicalIdentity := canonicalNames[rawName]
	if counts[rawName] > 1 || shadowsCanonicalIdentity || !providerToolNamePattern.MatchString(rawName) {
		return CanonicalToolName(info.MCPName, rawName)
	}
	return rawName
}

// ResolveTool resolves an exact canonical identity or a unique short name.
// Ambiguous short names fail closed and return canonical candidates.
func (r *Registry) ResolveTool(name string) (*ToolInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("工具名称不能为空")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	canonicalMatches := r.matchingToolsLocked(func(info *ToolInfo) bool {
		return CanonicalToolName(info.MCPName, info.Tool.Name) == name
	})
	if len(canonicalMatches) == 1 {
		return cloneToolInfo(canonicalMatches[0]), nil
	}
	if len(canonicalMatches) > 1 {
		return nil, ambiguousToolError(name, canonicalMatches)
	}
	rawMatches := r.matchingToolsLocked(func(info *ToolInfo) bool {
		return strings.TrimSpace(info.Tool.Name) == name
	})
	if len(rawMatches) == 1 {
		return cloneToolInfo(rawMatches[0]), nil
	}
	if len(rawMatches) > 1 {
		return nil, ambiguousToolError(name, rawMatches)
	}
	return nil, fmt.Errorf("工具不存在: %s", name)
}

// ResolveToolForMCP resolves a raw or canonical name within one server.
func (r *Registry) ResolveToolForMCP(mcpName, name string) (*ToolInfo, error) {
	mcpName = strings.TrimSpace(mcpName)
	name = strings.TrimSpace(name)
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Canonical identities take precedence over raw aliases. This mirrors
	// ResolveTool and prevents a raw name such as "mcp__docs__search" from
	// shadowing the canonical identity of docs/search.
	matches := r.matchingToolsLocked(func(info *ToolInfo) bool {
		return info.MCPName == mcpName && CanonicalToolName(info.MCPName, info.Tool.Name) == name
	})
	if len(matches) == 1 {
		return cloneToolInfo(matches[0]), nil
	}
	if len(matches) > 1 {
		return nil, ambiguousToolError(name, matches)
	}
	if info, ok := r.tools[r.makeToolKey(mcpName, name)]; ok && info.Enabled {
		return cloneToolInfo(info), nil
	}
	return nil, fmt.Errorf("工具不存在: %s/%s", mcpName, name)
}

// ListToolsByMCP 列出指定 MCP 的所有工具
func (r *Registry) ListToolsByMCP(mcpName string) []*ToolInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]*ToolInfo, 0)
	for _, info := range r.tools {
		if info.MCPName == mcpName && info.Enabled {
			tools = append(tools, cloneToolInfo(info))
		}
	}
	sort.Slice(tools, func(i, j int) bool {
		leftName, rightName := "", ""
		if tools[i] != nil && tools[i].Tool != nil {
			leftName = tools[i].Tool.Name
		}
		if tools[j] != nil && tools[j].Tool != nil {
			rightName = tools[j].Tool.Name
		}
		return leftName < rightName
	})
	return tools
}

// GetTool 获取工具信息
func (r *Registry) GetTool(mcpName, toolName string) (*ToolInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := r.makeToolKey(mcpName, toolName)
	info, ok := r.tools[key]
	if !ok {
		return nil, fmt.Errorf("工具不存在: %s", toolName)
	}

	return cloneToolInfo(info), nil
}

// EnableTool 启用工具
func (r *Registry) EnableTool(mcpName, toolName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := r.makeToolKey(mcpName, toolName)
	info, ok := r.tools[key]
	if !ok {
		return fmt.Errorf("工具不存在: %s", toolName)
	}

	info.Enabled = true
	return nil
}

// DisableTool 禁用工具
func (r *Registry) DisableTool(mcpName, toolName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := r.makeToolKey(mcpName, toolName)
	info, ok := r.tools[key]
	if !ok {
		return fmt.Errorf("工具不存在: %s", toolName)
	}

	info.Enabled = false
	return nil
}

// ToolEnabled 检查工具是否启用
func (r *Registry) ToolEnabled(mcpName, toolName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := r.makeToolKey(mcpName, toolName)
	info, ok := r.tools[key]
	if !ok {
		return false
	}
	return info.Enabled
}

// GetClient 获取 MCP 客户端
func (r *Registry) GetClient(mcpName string) (client.Client, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cli, ok := r.mcps[mcpName]
	if !ok {
		return nil, fmt.Errorf("MCP 客户端不存在: %s", mcpName)
	}

	return cli, nil
}

// ListClients 列出所有客户端
func (r *Registry) ListClients() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	clients := make([]string, 0, len(r.mcps))
	for name := range r.mcps {
		clients = append(clients, name)
	}
	return clients
}

// Clear 清空注册表
func (r *Registry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.tools = make(map[string]*ToolInfo)
	r.quarantined = make(map[string]*QuarantinedToolInfo)
	r.mcps = make(map[string]client.Client)
}

// makeToolKey 生成工具键
func (r *Registry) makeToolKey(mcpName, toolName string) string {
	return mcpName + "\x00" + toolName
}

// ListQuarantinedTools returns a stable diagnostic snapshot.
func (r *Registry) ListQuarantinedTools() []QuarantinedToolInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]QuarantinedToolInfo, 0, len(r.quarantined))
	for _, info := range r.quarantined {
		if info != nil {
			result = append(result, *info)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].MCPName == result[j].MCPName {
			return result[i].ToolName < result[j].ToolName
		}
		return result[i].MCPName < result[j].MCPName
	})
	return result
}

func (r *Registry) quarantineTool(mcpName, toolName string, schema map[string]interface{}, cause error) {
	schemaHash, _ := toolschema.RawHash(schema)
	key := r.makeToolKey(mcpName, toolName)
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, key)
	r.quarantined[key] = &QuarantinedToolInfo{
		MCPName:    mcpName,
		ToolName:   toolName,
		SchemaHash: schemaHash,
		Error:      cause.Error(),
	}
}

func (r *Registry) matchingToolsLocked(matches func(*ToolInfo) bool) []*ToolInfo {
	result := make([]*ToolInfo, 0, 1)
	for _, info := range r.tools {
		if info == nil || info.Tool == nil || !info.Enabled {
			continue
		}
		if matches(info) {
			result = append(result, info)
		}
	}
	return result
}

func ambiguousToolError(name string, matches []*ToolInfo) error {
	candidates := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, info := range matches {
		candidate := CanonicalToolName(info.MCPName, info.Tool.Name)
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}
	sort.Strings(candidates)
	return &AmbiguousToolError{Name: name, Candidates: candidates}
}

func portableNamePart(value string) string {
	raw := strings.TrimSpace(value)
	portable := strings.Trim(invalidNameChars.ReplaceAllString(raw, "_"), "_")
	if portable == "" {
		portable = "unnamed"
	}
	if portable != raw {
		portable += "_" + shortNameHash(raw)
	}
	return portable
}

var invalidNameChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
var providerToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func shortNameHash(value string) string {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(value))
	return fmt.Sprintf("%08x", hash.Sum32())[:6]
}

func shortIdentityHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:6])
}

func stringMetadata(metadata map[string]interface{}, key string) string {
	value, _ := metadata[key].(string)
	return value
}

func cloneToolInfo(info *ToolInfo) *ToolInfo {
	if info == nil {
		return nil
	}
	cloned := &ToolInfo{
		MCPName:  info.MCPName,
		Enabled:  info.Enabled,
		Metadata: cloneMetadata(info.Metadata),
	}
	if info.Tool == nil {
		return cloned
	}
	inputSchema, _ := toolschema.Clone(info.Tool.InputSchema)
	cloned.Tool = &protocol.Tool{
		Name:        info.Tool.Name,
		Description: info.Tool.Description,
		InputSchema: inputSchema,
		Metadata:    cloneMetadata(info.Tool.Metadata),
	}
	return cloned
}

func cloneMetadata(metadata map[string]interface{}) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
