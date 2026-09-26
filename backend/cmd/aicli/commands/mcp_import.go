package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/importers"
)

// ---- M4：跨 agent 导入 / 单 server 导出 / JSON 直填（§4.6、§4.7） ----

const (
	mcpImportConflictSkip      = "skip"
	mcpImportConflictOverwrite = "overwrite"
	mcpImportConflictRename    = "rename"

	mcpImportSecretsMask   = "mask"
	mcpImportSecretsReject = "reject"
	mcpImportSecretsKeep   = "keep"
)

var (
	importFrom       string
	importScope      string
	importDryRun     bool
	importOnConflict string
	importOnSecrets  string
	importOnly       []string
	getJSON          bool
	addJSONScope     string
)

func newMCPImportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "从其它 agent 工具导入 MCP 配置",
		Long: `把其它 agent 工具的 MCP 配置导入 aicli。

支持来源 (--from):
  claude    ~/.claude.json、<项目>/.mcp.json
  cursor    ~/.cursor/mcp.json、<项目>/.cursor/mcp.json
  gemini    ~/.gemini/settings.json、<项目>/.gemini/settings.json
  opencode  ~/.config/opencode/opencode.json、<项目>/opencode.json
  codex     ~/.codex/config.toml 的 [mcp_servers.*]
  all       扫描全部来源（默认）

写入层级 (--scope，默认 local，即 ~/.aicli/projects/<项目>/mcp.yaml)：
  user 个人全局；local 项目私有（不进版本库）；project 项目配置（可提交共享）。
  project 会拒绝明文凭证，除非显式 --on-secrets keep。

示例:
  aicli mcp import --dry-run                          # 预览将导入什么
  aicli mcp import --from claude --scope user          # 从 Claude 导入到个人全局
  aicli mcp import --on-conflict rename --only context7`,
		Args: cobra.NoArgs,
		Run:  importMCP,
	}
	cmd.Flags().StringVar(&importFrom, "from", importers.VendorAll, "来源: claude|cursor|gemini|opencode|codex|all")
	cmd.Flags().StringVar(&importScope, "scope", mcpWriteScopeLocal, "写入层级: user|local|project")
	cmd.Flags().BoolVar(&importDryRun, "dry-run", false, "只预览，不写配置")
	cmd.Flags().StringVar(&importOnConflict, "on-conflict", mcpImportConflictSkip, "同名冲突策略: skip|overwrite|rename")
	cmd.Flags().StringVar(&importOnSecrets, "on-secrets", mcpImportSecretsMask, "明文凭证处理（project 层生效）: mask|reject|keep")
	cmd.Flags().StringArrayVar(&importOnly, "only", nil, "只导入指定 server（可重复）")
	return cmd
}

func newMCPGetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <名称>",
		Short: "查看单个 MCP 的规范化配置（可直接复制/入库）",
		Long: `输出单个 MCP server 的规范化配置与来源文件。

--json 输出可直接被 ` + "`aicli mcp add-json`" + ` 使用（便于跨机复制）：
  aicli mcp get context7 --json
  aicli mcp add-json context7 "$(aicli mcp get context7 --json | jq -c .config)"`,
		Args: cobra.ExactArgs(1),
		Run:  getMCP,
	}
	cmd.Flags().BoolVar(&getJSON, "json", false, "以 JSON 输出（含 configSource/configPath）")
	return cmd
}

func newMCPAddJSONCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add-json <名称> <JSON>",
		Short: "用一段 JSON 直接添加/更新 MCP（脚本与跨机复制）",
		Long: `接受与配置文件同构的 JSON（type 可用 http/sse/ws 等别名）。

示例:
  aicli mcp add-json context7 '{"type":"http","url":"https://mcp.context7.com/mcp","headers":{"Authorization":"Bearer ${CONTEXT7_TOKEN}"}}'
  aicli mcp add-json local-fs '{"command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","/data"]}'
  aicli mcp add-json team '{"url":"https://team.example.com/mcp"}' --scope project`,
		Args: cobra.ExactArgs(2),
		Run:  addJSONMCP,
	}
	cmd.Flags().StringVar(&addJSONScope, "scope", "", "写入层级: user|local|project（缺省沿用既有写入路径解析）")
	return cmd
}

// mcpImportPlanItem 描述单个 server 的导入决策（便于 --dry-run 与 JSON 输出）。
type mcpImportPlanItem struct {
	Name           string   `json:"name"`
	Action         string   `json:"action"`
	Reason         string   `json:"reason,omitempty"`
	FinalName      string   `json:"finalName,omitempty"`
	Type           string   `json:"type,omitempty"`
	Source         string   `json:"source"`
	OriginalType   string   `json:"originalType,omitempty"`
	MaskedVars     []string `json:"maskedVars,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
}

type mcpImportResult struct {
	From        string                  `json:"from"`
	Scope       string                  `json:"scope"`
	TargetPath  string                  `json:"targetPath"`
	DryRun      bool                    `json:"dryRun"`
	Imported    int                     `json:"imported"`
	Renamed     int                     `json:"renamed"`
	Skipped     int                     `json:"skipped"`
	Rejected    int                     `json:"rejected"`
	Failed      int                     `json:"failed"`
	Items       []mcpImportPlanItem     `json:"items"`
	Scanned     []importers.ScannedFile `json:"scanned"`
	Warnings    []string                `json:"warnings,omitempty"`
	EnvHintVars []string                `json:"envHintVars,omitempty"`
}

func runMCPImportCommand(opts mcpImportOptions) (*mcpImportResult, error) {
	scope, err := parseMCPWriteScope(opts.Scope)
	if err != nil {
		return nil, err
	}
	if scope == "" {
		scope = mcpWriteScopeLocal
	}
	onConflict, err := parseMCPImportConflict(opts.OnConflict)
	if err != nil {
		return nil, err
	}
	onSecrets, err := parseMCPImportSecrets(opts.OnSecrets)
	if err != nil {
		return nil, err
	}

	targetPath, err := resolveMCPWritePathForScope(scope)
	if err != nil {
		return nil, err
	}

	results, err := importers.Import(opts.From, importers.Options{Names: opts.Only})
	if err != nil {
		return nil, err
	}

	// 只读探测目标文件：规划阶段（含 --dry-run）不能创建文件，
	// 因此不用 admin.LoadFile（那会 EnsureFile）。
	existing, err := loadMCPTargetFile(targetPath)
	if err != nil {
		return nil, err
	}
	taken := map[string]struct{}{}
	for name := range existing.MCPServers {
		taken[strings.ToLower(name)] = struct{}{}
	}

	plan := &mcpImportResult{
		From:       normalizedImportVendor(opts.From),
		Scope:      scope,
		TargetPath: targetPath,
		DryRun:     opts.DryRun,
	}
	masked := newSecretMasker()
	envVars := make([]string, 0)
	variants := make([]importers.Server, 0)

	for _, result := range results {
		plan.Scanned = append(plan.Scanned, result.Scanned...)
		plan.Warnings = append(plan.Warnings, result.Warnings...)
		for _, server := range result.Servers {
			variants = append(variants, server)
		}
	}
	sort.Slice(variants, func(i, j int) bool {
		if variants[i].Name != variants[j].Name {
			return variants[i].Name < variants[j].Name
		}
		return variants[i].Source < variants[j].Source
	})

	for _, server := range variants {
		item := mcpImportPlanItem{
			Name:         server.Name,
			Source:       server.Source,
			OriginalType: server.OriginalType,
			Type:         server.Config.Type,
			Warnings:     append([]string(nil), server.Warnings...),
		}
		if server.Config.Type == "" {
			item.Action = "skipped"
			item.Reason = "无法判定传输类型"
			plan.Skipped++
			plan.Items = append(plan.Items, item)
			continue
		}

		finalName := server.Name
		action := "imported"
		// overwriting 只在「同名覆盖」时为真：改名后是一个全新名字，必须走 Add。
		overwriting := false
		if _, exists := taken[strings.ToLower(finalName)]; exists {
			switch onConflict {
			case mcpImportConflictSkip:
				item.Action = "skipped"
				item.Reason = fmt.Sprintf("目标已存在同名 server（%s）", targetPath)
				plan.Skipped++
				plan.Items = append(plan.Items, item)
				continue
			case mcpImportConflictRename:
				finalName = uniqueImportName(server.Name, taken)
				action = "renamed"
				item.Reason = "同名冲突，已改名"
			case mcpImportConflictOverwrite:
				action = "overwritten"
				overwriting = true
				item.Reason = "同名冲突，已覆盖"
			}
		}

		// 秘密策略：只在会进入版本库的 project 层强制（user/local 是个人配置）。
		if scope == mcpWriteScopeProject && onSecrets != mcpImportSecretsKeep {
			findings := scanSensitiveFindings(upsertRequestFromServer(finalName, server.Config))
			if len(findings) > 0 {
				if onSecrets == mcpImportSecretsReject {
					item.Action = "rejected"
					item.Reason = "含明文凭证（--on-secrets reject）"
					plan.Rejected++
					plan.Items = append(plan.Items, item)
					continue
				}
				maskedVars := masked.mask(finalName, &server.Config)
				item.MaskedVars = maskedVars
				envVars = append(envVars, maskedVars...)
				item.Warnings = append(item.Warnings, "明文凭证已替换为 ${VAR} 引用")
			}
		}

		item.Action = action
		item.FinalName = finalName
		item.Type = server.Config.Type
		if server.Config.Timeout.Duration > 0 {
			item.TimeoutSeconds = int(server.Config.Timeout.Duration.Seconds())
		}

		if opts.DryRun {
			taken[strings.ToLower(finalName)] = struct{}{}
			plan.countAction(action)
			plan.Items = append(plan.Items, item)
			continue
		}
		service := mcpadmin.NewService(targetPath, mcpadmin.WithApplyOnMutate(false))
		request := upsertRequestFromServer(finalName, server.Config)
		var writeErr error
		if overwriting {
			// 覆盖场景必须走 Update：admin.Add 对已存在的名字会直接报错。
			_, writeErr = service.Update(context.Background(), finalName, request)
		} else {
			_, writeErr = service.Add(context.Background(), request)
		}
		if writeErr != nil {
			item.Action = "failed"
			item.Reason = writeErr.Error()
			plan.Failed++
			plan.Items = append(plan.Items, item)
			continue
		}
		taken[strings.ToLower(finalName)] = struct{}{}
		plan.countAction(action)
		plan.Items = append(plan.Items, item)
	}

	sort.Strings(envVars)
	plan.EnvHintVars = dedupeStrings(envVars)
	return plan, nil
}

// countAction 累计各类动作计数（dry-run 与真实写入共用，保证两者摘要一致）。
func (p *mcpImportResult) countAction(action string) {
	switch action {
	case "renamed":
		p.Renamed++
	case "overwritten":
		// 覆盖也算「导入成功」，单独用 Items[].Action 区分，无需额外计数列。
		p.Imported++
	default:
		p.Imported++
	}
}

// upsertRequestFromServer 把导入的 server 配置转成 admin 的写入请求。

// loadMCPTargetFile 以只读方式加载导入目标文件：文件不存在时返回空配置，
// 不创建任何文件（--dry-run 必须无副作用）。复用分层加载器保证解析/默认值一致。
func loadMCPTargetFile(path string) (*config.Config, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("MCP 写入路径为空")
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return &config.Config{MCPServers: map[string]config.MCPConfig{}}, nil
	}
	result, err := config.LoadLayered([]config.SourceFile{{Path: path, Source: "target", Exists: true}})
	if err != nil {
		return nil, fmt.Errorf("读取目标配置失败: %w", err)
	}
	return result.Config, nil
}

func upsertRequestFromServer(name string, server config.MCPConfig) mcpadmin.UpsertRequest {
	enabled := server.Enabled
	request := mcpadmin.UpsertRequest{
		Name: name,
		Type: mcpadmin.NormalizeTransportType(server.Type),
		Args: append([]string(nil), server.Args...),
		Env:  cloneStringMapForImport(server.Env),
	}
	if strings.TrimSpace(server.URL) != "" {
		request.URL = server.URL
	}
	if strings.TrimSpace(server.Command) != "" {
		request.Command = server.Command
	}
	if strings.TrimSpace(server.Description) != "" {
		description := server.Description
		request.Description = &description
	}
	if len(server.Headers) > 0 {
		request.Headers = cloneStringMapForImport(server.Headers)
	}
	if server.Timeout.Duration > 0 {
		seconds := int(server.Timeout.Duration.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		request.TimeoutSeconds = &seconds
	}
	if server.MaxParallelCalls > 0 {
		maxParallel := server.MaxParallelCalls
		request.MaxParallelCalls = &maxParallel
	}
	request.Enabled = &enabled
	if server.Auth != nil {
		auth := *server.Auth
		auth.Scopes = append([]string(nil), server.Auth.Scopes...)
		request.Auth = &auth
	}
	return request
}

func cloneStringMapForImport(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

// secretMasker 把明文凭证替换成 ${VAR} 引用，并对相同明文复用同一个变量名。
type secretMasker struct {
	byValue map[string]string
	used    map[string]struct{}
}

func newSecretMasker() *secretMasker {
	return &secretMasker{byValue: map[string]string{}, used: map[string]struct{}{}}
}

func (m *secretMasker) mask(serverName string, server *config.MCPConfig) []string {
	if server == nil {
		return nil
	}
	vars := make([]string, 0, 2)
	vars = append(vars, m.maskMap(serverName, server.Headers)...)
	vars = append(vars, m.maskMap(serverName, server.Env)...)
	if server.Auth != nil && strings.TrimSpace(server.Auth.ClientSecret) != "" {
		name := m.variableName(serverName, "CLIENT_SECRET")
		server.Auth.ClientSecret = "${" + name + "}"
		vars = append(vars, name)
	}
	return dedupeStrings(vars)
}

func (m *secretMasker) maskMap(serverName string, values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	vars := make([]string, 0, len(values))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := strings.TrimSpace(values[key])
		if value == "" || !looksSensitiveKey(key) || hasEnvReference(value) {
			continue
		}
		name, ok := m.byValue[value]
		if !ok {
			name = m.variableName(serverName, key)
			m.byValue[value] = name
		}
		values[key] = "${" + name + "}"
		vars = append(vars, name)
	}
	return vars
}

func (m *secretMasker) variableName(serverName, key string) string {
	base := strings.ToUpper(sanitizeEnvName(serverName) + "_" + sanitizeEnvName(key))
	if base == "_" {
		base = "MCP_SECRET"
	}
	name := base
	for index := 2; ; index++ {
		if _, exists := m.used[name]; !exists {
			m.used[name] = struct{}{}
			return name
		}
		name = fmt.Sprintf("%s_%d", base, index)
	}
}

// sanitizeEnvName 把标识符收敛成 [A-Z0-9_]，供环境变量名使用。
func sanitizeEnvName(value string) string {
	var builder strings.Builder
	pendingUnderscore := false
	for _, r := range strings.ToUpper(strings.TrimSpace(value)) {
		switch {
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			builder.WriteRune(r)
			pendingUnderscore = false
		default:
			if builder.Len() > 0 && !pendingUnderscore {
				builder.WriteByte('_')
				pendingUnderscore = true
			}
		}
	}
	return strings.Trim(builder.String(), "_")
}

func uniqueImportName(name string, taken map[string]struct{}) string {
	for index := 1; ; index++ {
		candidate := fmt.Sprintf("%s-imported", name)
		if index > 1 {
			candidate = fmt.Sprintf("%s-imported-%d", name, index)
		}
		if _, exists := taken[strings.ToLower(candidate)]; !exists {
			return candidate
		}
	}
}

func normalizedImportVendor(vendor string) string {
	normalized := strings.ToLower(strings.TrimSpace(vendor))
	if normalized == "" {
		return importers.VendorAll
	}
	return normalized
}

func parseMCPImportConflict(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", mcpImportConflictSkip:
		return mcpImportConflictSkip, nil
	case mcpImportConflictOverwrite:
		return mcpImportConflictOverwrite, nil
	case mcpImportConflictRename:
		return mcpImportConflictRename, nil
	default:
		return "", fmt.Errorf("不支持的 --on-conflict %q，可选: skip, overwrite, rename", value)
	}
}

func parseMCPImportSecrets(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", mcpImportSecretsMask:
		return mcpImportSecretsMask, nil
	case mcpImportSecretsReject:
		return mcpImportSecretsReject, nil
	case mcpImportSecretsKeep:
		return mcpImportSecretsKeep, nil
	default:
		return "", fmt.Errorf("不支持的 --on-secrets %q，可选: mask, reject, keep", value)
	}
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// renderMCPImportResult 输出导入摘要（文本）；--output json 走 envelope。
func renderMCPImportResult(payload *mcpImportResult, options mcpCommandOptions) {
	if options.OutputFormat == "json" {
		printCommandJSONOutput("mcp", options.JSONEnvelope, payload)
		return
	}
	mode := "导入"
	if payload.DryRun {
		mode = "预览（--dry-run）"
	}
	fmt.Printf("MCP %s · 来源=%s · 目标=%s (%s)\n", mode, payload.From, payload.TargetPath, payload.Scope)
	for _, item := range payload.Items {
		label := item.Name
		if item.FinalName != "" && item.FinalName != item.Name {
			label = fmt.Sprintf("%s → %s", item.Name, item.FinalName)
		}
		line := fmt.Sprintf("  [%s] %s (%s)", item.Action, label, item.Type)
		if item.Reason != "" {
			line += " · " + item.Reason
		}
		fmt.Println(line)
		for _, warning := range item.Warnings {
			fmt.Printf("        - %s\n", warning)
		}
		if len(item.MaskedVars) > 0 {
			fmt.Printf("        - 已改为引用: %s\n", strings.Join(item.MaskedVars, ", "))
		}
	}
	fmt.Printf("合计: 导入 %d、改名 %d、跳过 %d、拒绝 %d、失败 %d\n",
		payload.Imported, payload.Renamed, payload.Skipped, payload.Rejected, payload.Failed)
	for _, scanned := range payload.Scanned {
		if scanned.Err != "" {
			fmt.Printf("  ! %s: %s\n", scanned.Path, scanned.Err)
		}
	}
	if len(payload.EnvHintVars) > 0 {
		fmt.Printf("请在运行环境里设置这些变量（值不出现在配置文件中）:\n  %s\n",
			strings.Join(payload.EnvHintVars, "\n  "))
	}
	if payload.DryRun {
		fmt.Println("（--dry-run：未写入任何文件）")
	}
}

// renderMCPGetResult 输出单个 server 的规范化配置。
func renderMCPGetResult(name string, server config.MCPConfig, origin config.ServerOrigin, asJSON bool, options mcpCommandOptions) {
	if asJSON || options.OutputFormat == "json" {
		payload := map[string]interface{}{
			"name":         name,
			"config":       server,
			"configSource": origin.Source,
			"configPath":   origin.Path,
		}
		printCommandJSONOutput("mcp", options.JSONEnvelope, payload)
		return
	}
	fmt.Printf("MCP: %s\n", name)
	fmt.Printf("  类型: %s\n", server.Type)
	if server.URL != "" {
		fmt.Printf("  URL: %s\n", server.URL)
	}
	if server.Command != "" {
		fmt.Printf("  命令: %s %s\n", server.Command, strings.Join(server.Args, " "))
	}
	if len(server.Headers) > 0 {
		fmt.Printf("  Headers: %s\n", formatKeyValuePairs(server.Headers))
	}
	if len(server.Env) > 0 {
		fmt.Printf("  Env: %s\n", formatKeyValuePairs(server.Env))
	}
	if server.Timeout.Duration > 0 {
		fmt.Printf("  超时: %s\n", server.Timeout.Duration.String())
	}
	fmt.Printf("  启用: %t\n", server.IsEnabled())
	if origin.Source != "" {
		fmt.Printf("  来源: %s (%s)\n", origin.Source, origin.Path)
	}
}

func formatKeyValuePairs(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", key, values[key]))
	}
	return strings.Join(parts, ", ")
}

// ---- cobra 入口 ----

func importMCP(cmd *cobra.Command, args []string) {
	withMCPCommand(cmd, func(options mcpCommandOptions) {
		payload, err := runMCPImportCommand(mcpImportOptions{
			From:       importFrom,
			Scope:      importScope,
			DryRun:     importDryRun,
			OnConflict: importOnConflict,
			OnSecrets:  importOnSecrets,
			Only:       importOnly,
		})
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "import"})
		}
		renderMCPImportResult(payload, options)
	})
}

func getMCP(cmd *cobra.Command, args []string) {
	withMCPCommand(cmd, func(options mcpCommandOptions) {
		name := strings.TrimSpace(args[0])
		result, err := loadMCPConfigLayered()
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "get", "mcpName": name})
		}
		server, ok := result.Config.MCPServers[name]
		if !ok {
			exitCommandError("mcp", options.OutputFormat,
				fmt.Errorf("未找到 MCP %q（已加载 %d 个 server）", name, len(result.Config.MCPServers)),
				map[string]interface{}{"subcommand": "get", "mcpName": name})
		}
		origin, _ := result.Origin(name)
		renderMCPGetResult(name, server, origin, getJSON, options)
	})
}

func addJSONMCP(cmd *cobra.Command, args []string) {
	withMCPCommand(cmd, func(options mcpCommandOptions) {
		name := strings.TrimSpace(args[0])
		raw := strings.TrimSpace(args[1])
		request, err := parseMCPAddJSONRequest(name, raw)
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "add-json", "mcpName": name})
		}
		payload, err := runMCPAddJSONCommand(name, request, addJSONScope)
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "add-json", "mcpName": name})
		}
		renderMCPAddResult("add-json", "", payload, options)
	})
}

// parseMCPAddJSONRequest 解析 add-json 的 JSON 输入（宽松：接受 type 别名与 transport）。
func parseMCPAddJSONRequest(name, raw string) (mcpadmin.UpsertRequest, error) {
	if raw == "" {
		return mcpadmin.UpsertRequest{}, fmt.Errorf("JSON 不能为空")
	}
	var fields map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return mcpadmin.UpsertRequest{}, fmt.Errorf("解析 JSON 失败: %w", err)
	}
	if inner, ok := fields["config"].(map[string]interface{}); ok {
		fields = inner // 接受 `aicli mcp get --json` 的 .config 片段
	}
	transport := mcpJSONNonEmpty(stringField(fields, "type"), stringField(fields, "transport"))
	request := mcpadmin.UpsertRequest{
		Name:        name,
		Type:        mcpadmin.NormalizeTransportType(transport),
		Command:     stringField(fields, "command"),
		URL:         stringField(fields, "url"),
		Args:        stringSliceField(fields, "args"),
		Env:         stringMapField(fields, "env"),
		Headers:     stringMapField(fields, "headers", "http_headers", "httpHeaders"),
		TrustLevel:  stringField(fields, "trustLevel", "trust"),
		Description: optionalStringField(fields, "description"),
	}
	if request.Type == "" {
		// 未声明 type 时按 url/command 推断（与 importers 的规则一致），
		// 否则 admin 会把纯 url 的请求当成 stdio 并要求 command。
		switch {
		case strings.TrimSpace(request.URL) != "":
			request.Type = "streamable"
		case strings.TrimSpace(request.Command) != "":
			request.Type = "stdio"
		}
	}
	if enabled, ok := boolField(fields, "enabled"); ok {
		request.Enabled = &enabled
	}
	if disabled, ok := boolField(fields, "disabled"); ok && disabled {
		disabledValue := false
		request.Enabled = &disabledValue
	}
	if seconds, ok := intField(fields, "timeoutSeconds", "timeout_sec"); ok {
		request.TimeoutSeconds = &seconds
	}
	if maxParallel, ok := intField(fields, "maxParallelCalls", "max_parallel_calls"); ok {
		request.MaxParallelCalls = &maxParallel
	}
	if request.Command == "" && request.URL == "" {
		return mcpadmin.UpsertRequest{}, fmt.Errorf("JSON 需要提供 command（stdio）或 url（远程）")
	}
	return request, nil
}

func runMCPAddJSONCommand(name string, request mcpadmin.UpsertRequest, scope string) (*mcpActionCommandResult, error) {
	request.Name = name
	configPath, err := resolveMCPWritePathForScope(scope)
	if err != nil {
		return nil, err
	}
	if normalized, _ := parseMCPWriteScope(scope); normalized == mcpWriteScopeProject {
		if err := validateProjectScopeSecrets(request, configPath); err != nil {
			return nil, err
		}
	}
	emitMCPShadowNotice(name, configPath)
	service := mcpadmin.NewService(configPath, mcpadmin.WithApplyOnMutate(false))
	mcpCfg, err := service.Add(context.Background(), request)
	if err != nil {
		return nil, err
	}
	if normalized, _ := parseMCPWriteScope(scope); normalized == mcpWriteScopeProject {
		emitMCPProjectScopeNotice(configPath)
	}
	return &mcpActionCommandResult{
		MCPName:    name,
		ConfigPath: configPath,
		Config:     mcpCfg,
	}, nil
}

// ---- 宽松取值助手（JSON 字段可能是各种类型） ----

// mcpJSONNonEmpty 返回第一个非空值（包内已有 firstNonEmptyString，避免重名）。
func mcpJSONNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringField(fields map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if raw, ok := fields[key]; ok {
			switch value := raw.(type) {
			case string:
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func optionalStringField(fields map[string]interface{}, keys ...string) *string {
	for _, key := range keys {
		if raw, ok := fields[key]; ok {
			if value, isString := raw.(string); isString {
				trimmed := strings.TrimSpace(value)
				return &trimmed
			}
		}
	}
	return nil
}

func boolField(fields map[string]interface{}, keys ...string) (bool, bool) {
	for _, key := range keys {
		if raw, ok := fields[key]; ok {
			if value, isBool := raw.(bool); isBool {
				return value, true
			}
		}
	}
	return false, false
}

func intField(fields map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		if raw, ok := fields[key]; ok {
			switch value := raw.(type) {
			case float64:
				return int(value), true
			case int:
				return value, true
			}
		}
	}
	return 0, false
}

func stringSliceField(fields map[string]interface{}, keys ...string) []string {
	for _, key := range keys {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case []interface{}:
			out := make([]string, 0, len(value))
			for _, item := range value {
				if text, isString := item.(string); isString {
					out = append(out, text)
				}
			}
			return out
		case []string:
			return append([]string(nil), value...)
		}
	}
	return nil
}

func stringMapField(fields map[string]interface{}, keys ...string) map[string]string {
	for _, key := range keys {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case map[string]interface{}:
			out := make(map[string]string, len(value))
			for itemKey, itemValue := range value {
				if text, isString := itemValue.(string); isString {
					out[itemKey] = text
				}
			}
			return out
		case map[string]string:
			return value
		}
	}
	return nil
}

// mcpImportOptions 是 runMCPImportCommand 的输入（便于单测直接调用）。
type mcpImportOptions struct {
	From       string
	Scope      string
	DryRun     bool
	OnConflict string
	OnSecrets  string
	Only       []string
}
