// Package importers 把其它 agent 工具（Claude Code / Cursor / Gemini CLI /
// OpenCode / Codex）的 MCP 配置解析成 aicli 的 server 中间表示。
//
// 设计约束（对齐 docs/analysis/commandcode-mcp-design-borrowing-20260925.md §4.6）：
//   - 纯解析：只读文件、不写配置、不连网络，便于单测；
//   - 容忍未知字段：不认识的内容记入 Warnings 而不是报错，避免一个 odd field
//     就让用户整份配置导不进来；
//   - 错误隔离：某一份来源文件解析失败不影响其它来源（Err 记在该文件上）。
package importers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// 支持的外部来源标识。
const (
	VendorClaude   = "claude"
	VendorCodex    = "codex"
	VendorCursor   = "cursor"
	VendorGemini   = "gemini"
	VendorOpenCode = "opencode"
	// VendorJSON 读取显式指定的 JSON 文件（Options.JSONFile）。
	VendorJSON = "json"
	// VendorAll 扫描全部来源。
	VendorAll = "all"
)

// SupportedVendors 返回可用于 --from 的取值（不含 all）。
func SupportedVendors() []string {
	return []string{VendorClaude, VendorCodex, VendorCursor, VendorGemini, VendorOpenCode, VendorJSON}
}

// Options 控制扫描范围。
type Options struct {
	// Root 是项目根目录（默认进程 cwd）。
	Root string
	// Home 是用户主目录（默认 os.UserHomeDir）。
	Home string
	// Names 非空时只保留这些 server 名（大小写不敏感）。
	Names []string
	// JSONFile 是 --from json 要读取的显式 JSON 文件路径。
	JSONFile string
}

// ScannedFile 记录一个候选来源文件的扫描结果。
type ScannedFile struct {
	Path    string
	Exists  bool
	Servers int
	// Err 是该文件解析失败的原因（存在但不可用）；空表示成功。
	Err string
}

// Server 是导入的中间表示。
type Server struct {
	Name string
	// Config 是映射到 aicli 的 server 配置（Enabled 已按来源语义填好）。
	Config config.MCPConfig
	// Source 是定义该 server 的文件路径。
	Source string
	// OriginalType 是来源文件里写的传输类型（http/sse/stdio/...），便于诊断映射结果。
	OriginalType string
	// Warnings 是该 server 级别的映射告警（如忽略的未知字段）。
	Warnings []string
}

// Result 是单个来源的扫描结果。
type Result struct {
	Vendor   string
	Scanned  []ScannedFile
	Servers  []Server
	Warnings []string
}

// Import 扫描指定来源；vendor 为空或 "all" 时扫描全部来源。
func Import(vendor string, opts Options) ([]Result, error) {
	normalized := strings.ToLower(strings.TrimSpace(vendor))
	if normalized == "" {
		normalized = VendorAll
	}
	if normalized != VendorAll {
		if !isSupportedVendor(normalized) {
			return nil, fmt.Errorf("不支持的 --from %q，可选: %s, all",
				vendor, strings.Join(SupportedVendors(), ", "))
		}
	}

	opts, err := normalizeOptions(opts)
	if err != nil {
		return nil, err
	}

	vendors := SupportedVendors()
	if normalized != VendorAll {
		vendors = []string{normalized}
		if normalized == VendorJSON {
			var err error
			if opts, err = normalizeJSONFile(opts); err != nil {
				return nil, err
			}
		}
	}
	results := make([]Result, 0, len(vendors))
	for _, name := range vendors {
		results = append(results, scanVendor(name, opts))
	}
	return results, nil
}

func isSupportedVendor(vendor string) bool {
	for _, candidate := range SupportedVendors() {
		if candidate == vendor {
			return true
		}
	}
	return false
}

// normalizeJSONFile 规整显式 JSON 文件路径；--from json 必须提供。
func normalizeJSONFile(opts Options) (Options, error) {
	path := strings.TrimSpace(opts.JSONFile)
	if path == "" {
		return opts, fmt.Errorf("导入 JSON 文件需要指定路径（--file <路径>）")
	}
	opts.JSONFile = path
	return opts, nil
}

func normalizeOptions(opts Options) (Options, error) {
	if strings.TrimSpace(opts.Root) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return opts, fmt.Errorf("解析当前目录失败: %w", err)
		}
		opts.Root = cwd
	}
	if abs, err := filepath.Abs(opts.Root); err == nil {
		opts.Root = abs
	}
	if strings.TrimSpace(opts.Home) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return opts, fmt.Errorf("解析用户主目录失败: %w", err)
		}
		opts.Home = home
	}
	names := make([]string, 0, len(opts.Names))
	for _, name := range opts.Names {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			names = append(names, trimmed)
		}
	}
	opts.Names = names
	return opts, nil
}

// scanVendor 按「用户级 → 项目级」的顺序扫描；同名后者覆盖前者并记入告警。
func scanVendor(vendor string, opts Options) Result {
	result := Result{Vendor: vendor}
	for _, path := range vendorPaths(vendor, opts) {
		servers, warnings, err := parseVendorFile(vendor, path)
		scanned := ScannedFile{Path: path, Exists: fileExists(path), Servers: len(servers)}
		if err != nil {
			scanned.Err = err.Error()
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", path, err))
		}
		result.Scanned = append(result.Scanned, scanned)
		if len(warnings) > 0 {
			result.Warnings = append(result.Warnings, warnings...)
		}
		for _, server := range servers {
			replaced := false
			for index := range result.Servers {
				if result.Servers[index].Name == server.Name {
					result.Warnings = append(result.Warnings, fmt.Sprintf(
						"%s: 同名 server 被更高优先级来源覆盖（%s → %s）",
						server.Name, result.Servers[index].Source, server.Source))
					result.Servers[index] = server
					replaced = true
					break
				}
			}
			if !replaced {
				result.Servers = append(result.Servers, server)
			}
		}
	}
	result.Servers = filterByName(result.Servers, opts.Names)
	sort.Slice(result.Servers, func(i, j int) bool { return result.Servers[i].Name < result.Servers[j].Name })
	return result
}

// vendorPaths 返回候选来源文件（用户级在前、项目级在后 → 后者覆盖）。
func vendorPaths(vendor string, opts Options) []string {
	switch vendor {
	case VendorJSON:
		if path := strings.TrimSpace(opts.JSONFile); path != "" {
			return []string{path}
		}
		return nil
	case VendorClaude:
		return []string{
			filepath.Join(opts.Home, ".claude.json"),
			filepath.Join(opts.Root, ".mcp.json"),
		}
	case VendorCursor:
		return []string{
			filepath.Join(opts.Home, ".cursor", "mcp.json"),
			filepath.Join(opts.Root, ".cursor", "mcp.json"),
		}
	case VendorGemini:
		return []string{
			filepath.Join(opts.Home, ".gemini", "settings.json"),
			filepath.Join(opts.Root, ".gemini", "settings.json"),
		}
	case VendorOpenCode:
		return []string{
			filepath.Join(opts.Home, ".config", "opencode", "opencode.json"),
			filepath.Join(opts.Root, "opencode.json"),
		}
	case VendorCodex:
		return []string{filepath.Join(opts.Home, ".codex", "config.toml")}
	default:
		return nil
	}
}

func parseVendorFile(vendor, path string) ([]Server, []string, error) {
	if !fileExists(path) {
		return nil, nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("读取失败: %w", err)
	}
	if vendor == VendorCodex {
		return parseCodexTOML(data, path)
	}
	if vendor == VendorJSON {
		return parseJSONImportDocument(data, path)
	}
	return parseMCPServersJSON(data, path)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func filterByName(servers []Server, names []string) []Server {
	if len(names) == 0 {
		return servers
	}
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	filtered := make([]Server, 0, len(servers))
	for _, server := range servers {
		if _, ok := wanted[strings.ToLower(server.Name)]; ok {
			filtered = append(filtered, server)
		}
	}
	return filtered
}

// parseMCPServersJSON 解析 JSON 形态的外部配置。
//
// 兼容两种容器键：`mcpServers`（Claude / Cursor / Gemini / OpenCode）与
// `servers`（VS Code 风格）；Claude 的 ~/.claude.json 还带 projects.<path>.mcpServers。
func parseMCPServersJSON(data []byte, source string) ([]Server, []string, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, nil, fmt.Errorf("解析 JSON 失败: %w", err)
	}

	servers := make([]Server, 0)
	warnings := make([]string, 0)
	for _, key := range []string{"mcpServers", "servers"} {
		parsed, parseWarnings, err := parseServerMap(document[key], source)
		if err != nil {
			return nil, nil, err
		}
		servers = append(servers, parsed...)
		warnings = append(warnings, parseWarnings...)
	}
	if projects, ok := document["projects"]; ok {
		parsed, parseWarnings, err := parseClaudeProjects(projects, source)
		if err != nil {
			return nil, nil, err
		}
		servers = append(servers, parsed...)
		warnings = append(warnings, parseWarnings...)
	}
	if len(servers) == 0 && len(warnings) == 0 {
		warnings = append(warnings, fmt.Sprintf("%s: 未找到 mcpServers/servers 段", source))
	}
	return servers, warnings, nil
}

func parseClaudeProjects(raw json.RawMessage, source string) ([]Server, []string, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	var projects map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &projects); err != nil {
		return nil, []string{fmt.Sprintf("%s: projects 段解析失败（已忽略）: %v", source, err)}, nil
	}
	names := make([]string, 0, len(projects))
	for name := range projects {
		names = append(names, name)
	}
	sort.Strings(names)

	servers := make([]Server, 0)
	warnings := make([]string, 0)
	for _, name := range names {
		entry := projects[name]
		for _, key := range []string{"mcpServers", "servers"} {
			parsed, parseWarnings, err := parseServerMap(entry[key], source)
			if err != nil {
				return nil, nil, err
			}
			servers = append(servers, parsed...)
			warnings = append(warnings, parseWarnings...)
		}
	}
	return servers, warnings, nil
}

func parseServerMap(raw json.RawMessage, source string) ([]Server, []string, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	var entries map[string]map[string]interface{}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, nil, fmt.Errorf("解析 server 列表失败: %w", err)
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	servers := make([]Server, 0, len(entries))
	warnings := make([]string, 0)
	for _, name := range names {
		server, serverWarnings := mapServerEntry(name, entries[name], source)
		servers = append(servers, server)
		warnings = append(warnings, serverWarnings...)
	}
	return servers, warnings, nil
}

// knownEntryKeys 是已映射字段；其余字段记 warning 以免静默丢失用户意图。
var knownEntryKeys = map[string]struct{}{
	"type": {}, "transport": {}, "url": {}, "command": {}, "args": {}, "env": {},
	"headers": {}, "http_headers": {}, "httpheaders": {}, "timeout": {}, "timeout_sec": {},
	"timeoutsec": {}, "startup_timeout_ms": {}, "startuptimeoutms": {}, "tool_timeout_sec": {},
	"enabled": {}, "disabled": {}, "description": {}, "name": {},
	"authorization": {}, "oauth": {}, "scopes": {}, "trust": {}, "trustlevel": {},
	"workingdir": {}, "working_dir": {}, "cwd": {}, "maxretry": {}, "max_parallel_calls": {},
	"maxparallelcalls": {},
}

func mapServerEntry(name string, entry map[string]interface{}, source string) (Server, []string) {
	server := Server{
		Name:   name,
		Source: source,
		Config: config.MCPConfig{Name: name, Enabled: true},
	}
	warnings := make([]string, 0)

	server.OriginalType = strings.ToLower(strings.TrimSpace(firstNonEmpty(
		stringValue(entry, "type"), stringValue(entry, "transport"))))
	server.Config.Type = normalizeTransportType(server.OriginalType)

	if value := stringValue(entry, "url"); value != "" {
		server.Config.URL = value
	}
	if value := stringValue(entry, "command"); value != "" {
		server.Config.Command = value
	}
	if value := stringValue(entry, "description"); value != "" {
		server.Config.Description = value
	}
	if value := stringValue(entry, "working_dir", "workingDir", "cwd"); value != "" {
		server.Config.WorkingDir = value
	}
	server.Config.Args = stringSliceValue(entry, "args")

	env, envWarnings := stringMapValue(entry, "env")
	server.Config.Env = env
	warnings = append(warnings, envWarnings...)

	headers, headerWarnings := stringMapValue(entry, "headers", "http_headers", "httpHeaders")
	if len(headers) == 0 {
		if authorization := stringValue(entry, "authorization"); authorization != "" {
			headers = map[string]string{"Authorization": authorization}
		}
	}
	if len(headers) > 0 {
		server.Config.Headers = headers
	}
	warnings = append(warnings, headerWarnings...)

	if seconds, ok, timeoutWarnings := entryTimeoutSeconds(entry); ok {
		server.Config.Timeout = config.Duration{Duration: time.Duration(seconds * float64(time.Second))}
		warnings = append(warnings, timeoutWarnings...)
	}

	if enabled, ok := boolValue(entry, "enabled"); ok {
		server.Config.Enabled = enabled
	}
	if disabled, ok := boolValue(entry, "disabled"); ok && disabled {
		server.Config.Enabled = false
		server.Config.Disabled = true
	}
	if maxParallel, ok := intValue(entry, "max_parallel_calls", "maxParallelCalls"); ok && maxParallel > 0 {
		server.Config.MaxParallelCalls = maxParallel
	}
	if auth, authWarnings := mapOAuthEntry(name, entry); auth != nil {
		server.Config.Auth = auth
		warnings = append(warnings, authWarnings...)
	} else {
		warnings = append(warnings, authWarnings...)
	}

	// 推断传输类型：没有 type 时按 url/command 判定。
	if server.Config.Type == "" {
		switch {
		case strings.TrimSpace(server.Config.URL) != "":
			server.Config.Type = "streamable"
			warnings = append(warnings, fmt.Sprintf("%s: 未声明 type，按 URL 推断为 streamable", name))
		case strings.TrimSpace(server.Config.Command) != "":
			server.Config.Type = "stdio"
		}
	}
	if server.Config.Type == "stdio" && strings.TrimSpace(server.Config.Command) == "" {
		warnings = append(warnings, fmt.Sprintf("%s: stdio 缺少 command，已跳过", name))
	}
	if server.Config.Type != "" && server.Config.Type != "stdio" && strings.TrimSpace(server.Config.URL) == "" {
		warnings = append(warnings, fmt.Sprintf("%s: 远程传输缺少 url，已跳过", name))
	}
	if server.Config.Type == "" {
		warnings = append(warnings, fmt.Sprintf("%s: 无法判定传输类型（既无 url 也无 command），已跳过", name))
	}

	// 未知字段告警（不看大小写与分隔符）。
	for key := range entry {
		normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
		if _, known := knownEntryKeys[normalized]; known {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("%s: 忽略未映射字段 %q", name, key))
	}
	server.Warnings = append(server.Warnings, warnings...)
	return server, warnings
}

// mapOAuthEntry 识别 `oauth: true` / `oauth: {...}` / `scopes: [...]` 形态。
func mapOAuthEntry(name string, entry map[string]interface{}) (*config.MCPAuthConfig, []string) {
	warnings := make([]string, 0)
	raw, ok := entry["oauth"]
	if !ok {
		if scopes := stringSliceValue(entry, "scopes"); len(scopes) > 0 {
			return &config.MCPAuthConfig{Type: "oauth", Scopes: scopes}, warnings
		}
		return nil, warnings
	}
	switch value := raw.(type) {
	case bool:
		if !value {
			return nil, warnings
		}
		return &config.MCPAuthConfig{Type: "oauth"}, warnings
	case map[string]interface{}:
		auth := &config.MCPAuthConfig{Type: "oauth"}
		auth.ClientID = stringValue(value, "clientId", "client_id")
		auth.ClientSecret = stringValue(value, "clientSecret", "client_secret")
		auth.AuthorizationServer = stringValue(value, "authorizationServer", "authorization_server")
		auth.Scopes = stringSliceValue(value, "scopes")
		if port, ok := intValue(value, "callbackPort", "callback_port"); ok {
			auth.CallbackPort = port
		}
		if auth.ClientSecret != "" {
			warnings = append(warnings, fmt.Sprintf("%s: 来源包含 OAuth client secret，导入后请改为 ${VAR} 引用", name))
		}
		return auth, warnings
	default:
		warnings = append(warnings, fmt.Sprintf("%s: oauth 字段类型无法识别（已忽略）", name))
		return nil, warnings
	}
}

// normalizeTransportType 把外部写法映射到 aicli 的 type。
func normalizeTransportType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ""
	case "stdio", "local", "process":
		return "stdio"
	case "sse":
		return "sse"
	case "websocket", "ws", "wss":
		return "websocket"
	case "http", "streamable", "streamable-http", "streamable_http", "streamablehttp", "remote":
		return "streamable"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func entryTimeoutSeconds(entry map[string]interface{}) (float64, bool, []string) {
	warnings := make([]string, 0)
	if value, ok := floatValue(entry, "timeout", "timeout_sec", "timeoutSec"); ok && value > 0 {
		// 外部实现里 timeout 多为「秒」；有些工具用毫秒（>1000 且无小数）时给出提醒。
		if value >= 1000 && value == float64(int64(value)) {
			warnings = append(warnings, "timeout 数值较大，已按秒解析（若来源是毫秒请手工校准）")
		}
		return value, true, warnings
	}
	if ms, ok := floatValue(entry, "startup_timeout_ms", "startupTimeoutMs"); ok && ms > 0 {
		seconds := ms / 1000
		if seconds < 1 {
			seconds = 1
		}
		warnings = append(warnings, fmt.Sprintf("startup_timeout_ms=%v 已换算为 %v 秒", ms, seconds))
		return seconds, true, warnings
	}
	if seconds, ok := floatValue(entry, "tool_timeout_sec", "toolTimeoutSec"); ok && seconds > 0 {
		return seconds, true, warnings
	}
	return 0, false, warnings
}

// ---- 通用取值助手（JSON 数字/字符串/布尔都可能出现） ----

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringValue(entry map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		raw, ok := entry[key]
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case string:
			return strings.TrimSpace(value)
		case fmt.Stringer:
			return strings.TrimSpace(value.String())
		}
	}
	return ""
}

func boolValue(entry map[string]interface{}, keys ...string) (bool, bool) {
	for _, key := range keys {
		raw, ok := entry[key]
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case bool:
			return value, true
		case string:
			parsed, err := strconv.ParseBool(strings.TrimSpace(value))
			if err == nil {
				return parsed, true
			}
		}
	}
	return false, false
}

func intValue(entry map[string]interface{}, keys ...string) (int, bool) {
	value, ok := floatValue(entry, keys...)
	if !ok {
		return 0, false
	}
	return int(value), true
}

func floatValue(entry map[string]interface{}, keys ...string) (float64, bool) {
	for _, key := range keys {
		raw, ok := entry[key]
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case float64:
			return value, true
		case int:
			return float64(value), true
		case json.Number:
			parsed, err := value.Float64()
			if err == nil {
				return parsed, true
			}
		case string:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func stringSliceValue(entry map[string]interface{}, keys ...string) []string {
	for _, key := range keys {
		raw, ok := entry[key]
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case []interface{}:
			out := make([]string, 0, len(value))
			for _, item := range value {
				if text, ok := item.(string); ok {
					out = append(out, text)
				}
			}
			return out
		case []string:
			return append([]string(nil), value...)
		case string:
			if strings.TrimSpace(value) == "" {
				return nil
			}
			return []string{value}
		}
	}
	return nil
}

func stringMapValue(entry map[string]interface{}, keys ...string) (map[string]string, []string) {
	warnings := make([]string, 0)
	for _, key := range keys {
		raw, ok := entry[key]
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case map[string]interface{}:
			out := make(map[string]string, len(value))
			for itemKey, itemValue := range value {
				switch typed := itemValue.(type) {
				case string:
					out[itemKey] = typed
				case float64:
					out[itemKey] = strconv.FormatFloat(typed, 'f', -1, 64)
				case bool:
					out[itemKey] = strconv.FormatBool(typed)
				default:
					warnings = append(warnings, fmt.Sprintf("忽略 %s.%s（取值类型不支持）", key, itemKey))
				}
			}
			return out, warnings
		case map[string]string:
			return value, warnings
		case []interface{}:
			// VS Code 风格 `env: ["KEY=value"]`。
			out := make(map[string]string, len(value))
			for _, item := range value {
				text, ok := item.(string)
				if !ok {
					continue
				}
				if itemKey, itemValue, found := strings.Cut(text, "="); found {
					out[strings.TrimSpace(itemKey)] = itemValue
				}
			}
			if len(out) > 0 {
				return out, warnings
			}
		}
	}
	return nil, warnings
}
