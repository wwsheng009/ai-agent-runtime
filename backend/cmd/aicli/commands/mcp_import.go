package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
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
	importFile       string
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
  json      显式 JSON 文件（需给路径：位置参数或 --file）
  all       扫描全部来源（默认）

写入层级 (--scope，默认 local，即 ~/.aicli/projects/<项目>/mcp.yaml)：
  user 个人全局；local 项目私有（不进版本库）；project 项目配置（可提交共享）。
  project 会拒绝明文凭证，除非显式 --on-secrets keep。

--on-conflict 的判定覆盖目标文件与运行时分层链：链上更高优先级的同名 server
默认跳过（不写出不会生效的定义），rename 改为 <name>-imported；目标层更高时
照常导入并提示将覆盖。源文件本身属于自动发现层（如 ./.aicli/mcp.json）时，
会提示导入后移走/删除源文件，避免双份定义。

示例:
  aicli mcp import --dry-run                          # 预览将导入什么
  aicli mcp import --from claude --scope user          # 从 Claude 导入到个人全局
  aicli mcp import --from json ./.mcp.json             # 从任意 JSON 文件导入（可以粘多 server）
  aicli mcp import --on-conflict rename --only context7`,
		Args: cobra.MaximumNArgs(1),
		Run:  importMCP,
	}
	cmd.Flags().StringVar(&importFrom, "from", importers.VendorAll, "来源: claude|cursor|gemini|opencode|codex|json|all")
	cmd.Flags().StringVar(&importScope, "scope", mcpWriteScopeLocal, "写入层级: user|local|project")
	cmd.Flags().BoolVar(&importDryRun, "dry-run", false, "只预览，不写配置")
	cmd.Flags().StringVar(&importOnConflict, "on-conflict", mcpImportConflictSkip, "同名冲突策略（含分层链上更高优先级的同名定义）: skip|overwrite|rename")
	cmd.Flags().StringVar(&importOnSecrets, "on-secrets", mcpImportSecretsMask, "明文凭证处理（project 层生效）: mask|reject|keep")
	cmd.Flags().StringArrayVar(&importOnly, "only", nil, "只导入指定 server（可重复）")
	cmd.Flags().StringVar(&importFile, "file", "", "--from json 的 JSON 文件路径（同位置参数）")
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

输入可以是：
  直接 JSON 字符串、@文件路径（把整份 JSON 放文件里）、-（从 stdin 读，便于管道）。
  多 server 容器（{"mcpServers":{...}}）请用 aicli mcp import --from json <文件>。

示例:
  aicli mcp add-json context7 '{"type":"http","url":"https://mcp.context7.com/mcp","headers":{"Authorization":"Bearer ${CONTEXT7_TOKEN}"}}'
  aicli mcp add-json local-fs '{"command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","/data"]}'
  aicli mcp add-json team '@./notion.json' --scope project
  aicli mcp get context7 --json | jq -c .config | aicli mcp add-json context7 -`,
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

	vendor := normalizedImportVendor(opts.From)
	jsonFile := strings.TrimSpace(opts.File)
	if vendor != importers.VendorJSON && jsonFile != "" {
		return nil, fmt.Errorf("--file 仅适用于 --from json（当前 --from %s）", vendor)
	}
	if vendor == importers.VendorJSON {
		// 显式指定的文件必须存在：与厂商扫描「缺失即跳过」不同，
		// 用户既然点名了这份文件，找不到就该报错。
		if jsonFile == "" {
			return nil, fmt.Errorf("--from json 需要指定 JSON 文件（位置参数或 --file <路径>）")
		}
		info, err := os.Stat(jsonFile)
		if err != nil {
			return nil, fmt.Errorf("读取 JSON 文件失败: %w", err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("JSON 路径是目录: %s", jsonFile)
		}
	}

	targetPath, err := resolveMCPWritePathForScope(scope)
	if err != nil {
		return nil, err
	}

	results, err := importers.Import(opts.From, importers.Options{Names: opts.Only, JSONFile: jsonFile})
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

	// 导入与运行时分层链的交互保护：识别「源文件本身就是自动发现层文件」
	// 与「链上更高优先级的同名定义」，避免写出双份定义或不生效的「死配置」。
	chain := loadMCPImportChainContext(targetPath)
	plan.Warnings = append(plan.Warnings, chain.warnings(targetPath)...)
	plan.Warnings = append(plan.Warnings, mcpImportSourceWarnings(variants, targetPath)...)

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

		// 分层链上的同名定义：目标层更高 → 提示本次导入将覆盖；目标层更低
		// （或被真实覆盖接管）→ 导入不会生效，按 --on-conflict 处理。
		if origin, exists := chain.lookup(finalName); exists {
			if chain.targetOverrides(origin) {
				item.Warnings = append(item.Warnings, fmt.Sprintf(
					"同名 server 已定义于 %s 层（%s）；本次导入写入 %s 将在其之上覆盖",
					origin.Source, origin.Path, targetPath))
			} else {
				switch onConflict {
				case mcpImportConflictSkip:
					item.Action = "skipped"
					item.Reason = fmt.Sprintf("已存在于更高优先级层（%s %s），导入后不会生效", origin.Source, origin.Path)
					plan.Skipped++
					plan.Items = append(plan.Items, item)
					continue
				case mcpImportConflictRename:
					finalName = uniqueImportName(finalName, taken)
					action = "renamed"
					item.Reason = "同名冲突（更高优先级层），已改名"
				default:
					item.Warnings = append(item.Warnings, fmt.Sprintf(
						"已存在于更高优先级层（%s %s），该定义仍会被遮蔽、不会生效（--on-conflict overwrite）",
						origin.Source, origin.Path))
				}
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

// ---- 导入与运行时分层链的交互保护 ----

// mcpNormalizedPath 归一化用于比较的路径（绝对化 + Clean；失败时退化为 Clean）。
func mcpNormalizedPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

// mcpPathKey 在 mcpNormalizedPath 之上生成用于相等比较的键：Windows 下
// 文件系统大小写不敏感（与 runtimeapi.workspaceMCPKey 同口径）。
func mcpPathKey(path string) string {
	key := mcpNormalizedPath(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(key)
	}
	return key
}

// mcpImportChainContext 描述导入目标在运行时分层链中的位置（best-effort：
// 链加载失败时退化为空上下文，导入仍按目标文件语义执行）。
type mcpImportChainContext struct {
	origins    map[string]config.ServerOrigin // key = 小写 server 名
	ranks      map[string]int                 // 路径键 -> Files 下标（低→高）
	target     string                         // 导入目标路径键
	targetRank int                            // -1 = 目标不在链上（如被真实覆盖接管）
	override   string                         // 真实覆盖文件路径（链退化为单文件时）
}

// loadMCPImportChainContext 按运行时同一套发现链构建导入上下文。
func loadMCPImportChainContext(targetPath string) *mcpImportChainContext {
	ctx := &mcpImportChainContext{target: mcpPathKey(targetPath), targetRank: -1}
	result, err := loadMCPConfigLayered()
	if err != nil || result == nil {
		return ctx
	}
	ctx.origins = make(map[string]config.ServerOrigin, len(result.Origins))
	for name, origin := range result.Origins {
		ctx.origins[strings.ToLower(strings.TrimSpace(name))] = origin
	}
	ctx.ranks = make(map[string]int, len(result.Files))
	for index, file := range result.Files {
		key := mcpPathKey(file.Path)
		ctx.ranks[key] = index
		if key == ctx.target {
			ctx.targetRank = index
		}
		if file.Source == "explicit" {
			ctx.override = mcpNormalizedPath(file.Path)
		}
	}
	return ctx
}

// lookup 返回链上定义该名字的来源（大小写不敏感）；目标文件自身的同名
// 由目标冲突分支处理，这里显式排除，避免重复判定。
func (c *mcpImportChainContext) lookup(name string) (config.ServerOrigin, bool) {
	if c == nil || c.origins == nil {
		return config.ServerOrigin{}, false
	}
	origin, ok := c.origins[strings.ToLower(strings.TrimSpace(name))]
	if !ok || mcpPathKey(origin.Path) == c.target {
		return config.ServerOrigin{}, false
	}
	return origin, true
}

// targetOverrides 判断目标层是否高于来源层（导入将覆盖该定义）。
// 目标不在链上（被真实覆盖接管）时恒为 false。
func (c *mcpImportChainContext) targetOverrides(origin config.ServerOrigin) bool {
	if c == nil || c.targetRank < 0 || c.ranks == nil {
		return false
	}
	originRank, ok := c.ranks[mcpPathKey(origin.Path)]
	if !ok {
		return false
	}
	return originRank < c.targetRank
}

// warnings 返回链级提示（真实覆盖接管分层链等）。
func (c *mcpImportChainContext) warnings(targetPath string) []string {
	if c == nil || c.override == "" || mcpPathKey(c.override) == c.target {
		return nil
	}
	return []string{fmt.Sprintf(
		"检测到显式覆盖配置 %s（--config-file / MCP_CONFIG_FILE / aicli.mcp.config_file），运行时只加载该文件；导入到 %s 的定义不会生效",
		c.override, targetPath)}
}

// mcpImportSourceWarnings 识别「源文件同时是运行时自动发现的配置层文件」：
// 导入不会移动源文件，源文件会继续参与分层合并，形成双份定义。
func mcpImportSourceWarnings(servers []importers.Server, targetPath string) []string {
	if len(servers) == 0 {
		return nil
	}
	resolution := aiclipaths.ResolveMCPConfigPathDetailedFrom("", mcpConfigOverride())
	candidates := make(map[string]string, len(resolution.Candidates))
	for _, candidate := range resolution.Candidates {
		candidates[mcpPathKey(candidate.Path)] = candidate.Source
	}
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, server := range servers {
		key := mcpPathKey(server.Source)
		source, ok := candidates[key]
		if !ok || key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, fmt.Sprintf(
			"源文件 %s 属于运行时自动发现的配置层（%s）：导入后源文件仍会被自动加载，与目标 %s 形成双份定义；建议导入后移走或删除源文件",
			server.Source, source, targetPath))
	}
	sort.Strings(out)
	return out
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
	// 分层链级提示（源文件属于自动发现层、显式覆盖接管等）：与 item 无关，
	// 但直接决定导入是否有效，必须在摘要里可见而不是只留在 JSON 里。
	for _, warning := range payload.Warnings {
		fmt.Printf("  ! %s\n", warning)
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
		pathArg := ""
		if len(args) > 0 {
			pathArg = strings.TrimSpace(args[0])
		}
		// `--from json` 的文件可以写成位置参数或 --file；两者都给时必须一致。
		file, resolveErr := resolveMCPImportFilePath(pathArg, importFile)
		if resolveErr != nil {
			exitCommandError("mcp", options.OutputFormat, resolveErr,
				map[string]interface{}{"subcommand": "import"})
		}
		payload, err := runMCPImportCommand(mcpImportOptions{
			From:       importFrom,
			Scope:      importScope,
			DryRun:     importDryRun,
			OnConflict: importOnConflict,
			OnSecrets:  importOnSecrets,
			Only:       importOnly,
			File:       file,
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
		input, err := resolveMCPAddJSONInput(raw, true)
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "add-json", "mcpName": name})
		}
		request, warnings, err := parseMCPAddJSONRequest(name, input)
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "add-json", "mcpName": name})
		}
		payload, err := runMCPAddJSONCommand(name, request, addJSONScope)
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "add-json", "mcpName": name})
		}
		payload.Warnings = append(payload.Warnings, warnings...)
		renderMCPAddResult("add-json", "", payload, options)
	})
}

// resolveMCPAddJSONInput 解析 add-json 的输入：
//   - 空串 → 报错；
//   - `@<路径>` → 读文件（UTF-8 BOM 会被剥掉）；
//   - `-`（仅 CLI，allowStdin=true）→ 从 stdin 读，便于 `... | aicli mcp add-json x -`；
//   - 其它 → 原样当作 JSON 文本。
//
// 文件与 stdin 都有 1MB 上限：MCP server 配置不该这么大，防止误传大文件把会话拖死。
func resolveMCPAddJSONInput(raw string, allowStdin bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	switch {
	case trimmed == "":
		return "", fmt.Errorf("JSON 不能为空")
	case trimmed == "-":
		if !allowStdin {
			return "", fmt.Errorf("不支持从 stdin 读取（此上下文）")
		}
		data, err := readLimited(os.Stdin, mcpAddJSONMaxBytes)
		if err != nil {
			return "", fmt.Errorf("读取 stdin 失败: %w", err)
		}
		return normalizeJSONInput(data)
	case strings.HasPrefix(trimmed, "@"):
		path := strings.TrimSpace(strings.TrimPrefix(trimmed, "@"))
		if path == "" {
			return "", fmt.Errorf("@ 后面需要文件路径（示例: aicli mcp add-json notion '@notion.json'）")
		}
		file, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("读取 JSON 文件失败: %w", err)
		}
		defer func() { _ = file.Close() }()
		data, err := readLimited(file, mcpAddJSONMaxBytes)
		if err != nil {
			return "", fmt.Errorf("读取 JSON 文件失败: %w", err)
		}
		return normalizeJSONInput(data)
	default:
		return trimmed, nil
	}
}

// mcpAddJSONMaxBytes 是 @文件 / stdin 输入的大小上限（1MB）。
const mcpAddJSONMaxBytes = 1 << 20

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("内容超过 %d 字节上限", limit)
	}
	return data, nil
}

func normalizeJSONInput(data []byte) (string, error) {
	trimmed := strings.TrimSpace(string(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})))
	if trimmed == "" {
		return "", fmt.Errorf("JSON 不能为空")
	}
	return trimmed, nil
}

// mcpAddJSONKnownKeys 是 add-json 已映射的字段（小写、去分隔符）；其余字段回告警，
// 避免用户把 headers 写成 header 之类时静默丢失。
var mcpAddJSONKnownKeys = map[string]struct{}{
	"config": {}, "name": {},
	"type": {}, "transport": {}, "url": {}, "command": {}, "args": {}, "env": {},
	"headers": {}, "httpheaders": {}, "trustlevel": {}, "trust": {}, "description": {},
	"enabled": {}, "disabled": {}, "timeoutseconds": {}, "timeoutsec": {},
	"maxparallelcalls": {},
}

// mcpAddJSONMultiServerKeys 是多 server 容器的顶层键：add-json 只收单 server，
// 命中时给出改用 import --from json 的提示。
var mcpAddJSONMultiServerKeys = []string{"mcpServers", "servers", "projects"}

// parseMCPAddJSONRequest 解析 add-json 的 JSON 输入（宽松：接受 type 别名与 transport）。
// 第二个返回值是告警（未映射字段、疑似多 server 容器等），供 CLI/chat 回显。
func parseMCPAddJSONRequest(name, raw string) (mcpadmin.UpsertRequest, []string, error) {
	if raw == "" {
		return mcpadmin.UpsertRequest{}, nil, fmt.Errorf("JSON 不能为空")
	}
	var fields map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return mcpadmin.UpsertRequest{}, nil, fmt.Errorf("解析 JSON 失败: %w", err)
	}
	if container := mcpAddJSONContainerKey(fields); container != "" {
		return mcpadmin.UpsertRequest{}, nil, fmt.Errorf(
			"这是多 server 容器（含 %q）；add-json 一次只收一个 server。"+
				"整份导入请用: aicli mcp import --from json <文件>（chat 内可先写到文件再 reload）", container)
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
		return mcpadmin.UpsertRequest{}, nil, fmt.Errorf("JSON 需要提供 command（stdio）或 url（远程）")
	}
	return request, mcpAddJSONUnknownKeys(fields), nil
}

func mcpAddJSONContainerKey(fields map[string]interface{}) string {
	for _, key := range mcpAddJSONMultiServerKeys {
		if _, ok := fields[key]; ok {
			return key
		}
	}
	return ""
}

// mcpAddJSONUnknownKeys 返回未映射字段（保持输入顺序无关的排序，输出稳定）。
func mcpAddJSONUnknownKeys(fields map[string]interface{}) []string {
	unknown := make([]string, 0)
	for key := range fields {
		normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
		if _, known := mcpAddJSONKnownKeys[normalized]; known {
			continue
		}
		unknown = append(unknown, key)
	}
	sort.Strings(unknown)
	warnings := make([]string, 0, len(unknown))
	for _, key := range unknown {
		warnings = append(warnings, fmt.Sprintf("忽略未映射字段 %q", key))
	}
	return warnings
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
	// File 是 --from json 的输入文件（位置参数或 --file）。
	File string
}

// resolveMCPImportFilePath 合并 `--from json` 的位置参数与 --file：
// 只给一个时用它；两个都给且不一致时直接报错（不猜用户意图）。
func resolveMCPImportFilePath(pathArg, file string) (string, error) {
	pathArg = strings.TrimSpace(pathArg)
	file = strings.TrimSpace(file)
	switch {
	case pathArg != "" && file != "" && pathArg != file:
		return "", fmt.Errorf("位置参数 %q 与 --file %q 不一致（--from json 只能指定一份文件）", pathArg, file)
	case pathArg != "":
		return pathArg, nil
	default:
		return file, nil
	}
}
