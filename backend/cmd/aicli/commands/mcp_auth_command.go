package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/auth"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// M2：`aicli mcp auth` / `aicli mcp logout` 的 CLI 参数。
var (
	mcpAuthList         bool
	mcpAuthStatus       bool
	mcpAuthClear        bool
	mcpAuthAll          bool
	mcpAuthNoBrowser    bool
	mcpAuthScopes       []string
	mcpAuthClientID     string
	mcpAuthClientSecret string
	mcpAuthCallbackPort int
	mcpAuthServer       string
	mcpAuthTimeout      time.Duration

	mcpLogoutAll bool
)

// newMCPAuthCommand 构造 `mcp auth` 子命令。
func newMCPAuthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth [名称]",
		Short: "完成 MCP OAuth 授权或查看令牌状态",
		Long: `MCP OAuth 授权（Authorization Code + PKCE）

常规用法:
  aicli mcp auth <名称>             # 打开浏览器完成授权，令牌存入 ~/.aicli/mcp-tokens.json
  aicli mcp auth <名称> --no-browser # 打印授权链接，手动粘贴回调地址/code（无浏览器/Win7 兜底）
  aicli mcp auth --status           # 查看各 server 的授权状态
  aicli mcp auth --list             # 列出已保存的令牌（不含明文）
  aicli mcp auth --clear <名称>     # 清除单个 server 的令牌
  aicli mcp auth --clear --all      # 清除全部令牌

前置条件：目标 server 需配置 auth: oauth（` + "`aicli mcp add <名称> <URL> --auth oauth`" + `）。
自动 OAuth 不可用时，可在配置的 headers 中手动设置 Authorization。`,
		Args: cobra.MaximumNArgs(1),
		Run:  runMCPAuth,
	}
	cmd.Flags().BoolVar(&mcpAuthList, "list", false, "列出已保存的令牌（不含明文）")
	cmd.Flags().BoolVar(&mcpAuthStatus, "status", false, "查看各 server 的授权状态")
	cmd.Flags().BoolVar(&mcpAuthClear, "clear", false, "清除令牌")
	cmd.Flags().BoolVar(&mcpAuthAll, "all", false, "与 --clear 联用：清除全部 server 的令牌")
	cmd.Flags().BoolVar(&mcpAuthNoBrowser, "no-browser", false, "不自动打开浏览器，改为手动粘贴回调地址/code")
	cmd.Flags().StringArrayVar(&mcpAuthScopes, "scope", nil, "覆盖请求的 scope（可重复）")
	cmd.Flags().StringVar(&mcpAuthClientID, "client-id", "", "覆盖 OAuth client_id")
	cmd.Flags().StringVar(&mcpAuthClientSecret, "client-secret", "", "覆盖 OAuth client_secret")
	cmd.Flags().IntVar(&mcpAuthCallbackPort, "callback-port", 0, "本地回调端口（0=随机）")
	cmd.Flags().StringVar(&mcpAuthServer, "auth-server", "", "覆盖授权服务器地址")
	cmd.Flags().DurationVar(&mcpAuthTimeout, "timeout", 0, "等待授权回调的超时（默认 5m）")
	return cmd
}

// newMCPLogoutCommand 构造 `mcp logout` 子命令（auth --clear 的别名）。
func newMCPLogoutCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logout [名称]",
		Short: "清除 MCP OAuth 令牌",
		Args:  cobra.MaximumNArgs(1),
		Run:   runMCPLogout,
	}
	cmd.Flags().BoolVar(&mcpLogoutAll, "all", false, "清除全部 server 的令牌")
	return cmd
}

func runMCPAuth(cmd *cobra.Command, args []string) {
	withMCPCommand(cmd, func(options mcpCommandOptions) {
		switch {
		case mcpAuthList:
			renderMCPAuthList(options)
			return
		case mcpAuthClear:
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			runMCPAuthClear(name, mcpAuthAll, options)
			return
		case mcpAuthStatus:
			runMCPAuthStatus(args, options)
			return
		}
		if len(args) == 0 {
			runMCPAuthStatus(nil, options)
			fmt.Println("\n用法: aicli mcp auth <名称> 完成授权；--list / --status / --clear 查看或清除令牌。")
			return
		}
		runMCPAuthLogin(args[0], options)
	})
}

func runMCPLogout(cmd *cobra.Command, args []string) {
	withMCPCommand(cmd, func(options mcpCommandOptions) {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		if name == "" && !mcpLogoutAll {
			exitCommandError("mcp", options.OutputFormat,
				fmt.Errorf("需要指定 MCP 名称，或使用 --all 清除全部令牌"),
				map[string]interface{}{"subcommand": "logout"})
		}
		runMCPAuthClear(name, mcpLogoutAll, options)
	})
}

// loadMCPAuthServer 读取配置并返回目标 server（requireOAuth 时校验 auth: oauth）。
func loadMCPAuthServer(name string, requireOAuth bool) (config.MCPConfig, string, error) {
	path := resolveMCPConfigPathForWrite()
	name = strings.TrimSpace(name)
	if name == "" {
		return config.MCPConfig{}, path, fmt.Errorf("需要指定 MCP server 名称")
	}
	cfg, err := config.NewLoader(path).Load()
	if err != nil {
		return config.MCPConfig{}, path, fmt.Errorf("加载 MCP 配置失败: %w", err)
	}
	server, ok := cfg.MCPServers[name]
	if !ok {
		return config.MCPConfig{}, path, fmt.Errorf("未找到 MCP server: %s（配置文件: %s）", name, path)
	}
	if requireOAuth && !server.IsOAuth() {
		return server, path, fmt.Errorf("MCP server '%s' 未启用 oauth。可用 `aicli mcp add <名称> <URL> --auth oauth` 启用，或在配置中加 auth: oauth", name)
	}
	return server, path, nil
}

// oauthServersFromConfig 返回配置中所有启用 oauth 的 server（按名称排序）。
func oauthServersFromConfig() (map[string]config.MCPConfig, string, error) {
	path := resolveMCPConfigPathForWrite()
	cfg, err := config.NewLoader(path).Load()
	if err != nil {
		return nil, path, fmt.Errorf("加载 MCP 配置失败: %w", err)
	}
	out := make(map[string]config.MCPConfig)
	for name, server := range cfg.MCPServers {
		if server.IsOAuth() {
			out[name] = server
		}
	}
	return out, path, nil
}

func newAuthSession(server config.MCPConfig) (*auth.Session, *auth.TokenStore, error) {
	store, err := auth.NewTokenStore("")
	if err != nil {
		return nil, nil, err
	}
	if server.Auth == nil {
		return nil, store, fmt.Errorf("MCP server '%s' 未配置 auth", server.Name)
	}
	session, err := auth.NewSession(server.Name, server.URL, *server.Auth, auth.SessionOptions{Store: store})
	if err != nil {
		return nil, store, err
	}
	return session, store, nil
}

func runMCPAuthLogin(name string, options mcpCommandOptions) {
	server, configPath, err := loadMCPAuthServer(name, true)
	if err != nil {
		exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "auth", "mcpName": name})
	}
	session, _, err := newAuthSession(server)
	if err != nil {
		exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "auth", "mcpName": name})
	}

	ctx := context.Background()
	cancel := func() {}
	if mcpAuthTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, mcpAuthTimeout)
	}
	defer cancel()

	token, err := session.Authenticate(ctx, auth.AuthorizeOptions{
		NoBrowser:    mcpAuthNoBrowser,
		Port:         mcpAuthCallbackPort,
		Timeout:      mcpAuthTimeout,
		Scopes:       mcpAuthScopes,
		ClientID:     mcpAuthClientID,
		ClientSecret: mcpAuthClientSecret,
		AuthServer:   mcpAuthServer,
	})
	if err != nil {
		exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "auth", "mcpName": name})
	}
	if options.OutputFormat == "json" {
		printCommandJSONOutput("mcp", options.JSONEnvelope, map[string]interface{}{
			"action":              "auth",
			"mcpName":             name,
			"configPath":          configPath,
			"authorizationServer": token.AuthServer,
			"scope":               token.Scope,
			"expiresAt":           token.ExpiresAt,
			"message":             "授权成功",
		})
	}
}

func runMCPAuthStatus(args []string, options mcpCommandOptions) {
	servers, configPath, err := oauthServersFromConfig()
	if err != nil {
		exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "auth", "mode": "status"})
	}
	if len(args) > 0 {
		name := strings.TrimSpace(args[0])
		server, ok := servers[name]
		if !ok {
			exitCommandError("mcp", options.OutputFormat,
				fmt.Errorf("MCP server '%s' 未启用 oauth（或不存在）。配置文件: %s", name, configPath),
				map[string]interface{}{"subcommand": "auth", "mode": "status", "mcpName": name})
		}
		servers = map[string]config.MCPConfig{name: server}
	}

	statuses := make([]auth.SessionStatus, 0, len(servers))
	for _, server := range servers {
		session, _, sessionErr := newAuthSession(server)
		if sessionErr != nil {
			statuses = append(statuses, auth.SessionStatus{
				ServerName: server.Name, ServerURL: server.URL,
				NeedsAuth: true, Reason: sessionErr.Error(),
			})
			continue
		}
		statuses = append(statuses, session.Status())
	}
	sortSessionStatuses(statuses)

	if options.OutputFormat == "json" {
		printCommandJSONOutput("mcp", options.JSONEnvelope, statuses)
		return
	}
	if len(statuses) == 0 {
		fmt.Printf("没有配置 OAuth 的 MCP server（配置文件: %s）\n", configPath)
		return
	}
	fmt.Println("MCP OAuth 状态:")
	fmt.Println("─────────────────────────────────────────")
	for _, status := range statuses {
		state := "未登录"
		switch {
		case status.Authenticated && !status.NeedsAuth:
			state = "已授权"
		case status.NeedsAuth:
			state = "需认证"
		}
		fmt.Printf("  %s\n", status.ServerName)
		fmt.Printf("    地址: %s\n", status.ServerURL)
		fmt.Printf("    状态: %s\n", state)
		if status.Scope != "" {
			fmt.Printf("    scope: %s\n", status.Scope)
		}
		if !status.ExpiresAt.IsZero() {
			fmt.Printf("    过期时间: %s\n", status.ExpiresAt.Format(time.RFC3339))
		}
		if status.HasRefreshToken {
			fmt.Printf("    可自动刷新: true\n")
		}
		if status.Reason != "" {
			fmt.Printf("    说明: %s\n", status.Reason)
		}
		fmt.Println()
	}
}

func renderMCPAuthList(options mcpCommandOptions) {
	store, err := auth.NewTokenStore("")
	if err != nil {
		exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "auth", "mode": "list"})
	}
	tokens := store.List()
	if options.OutputFormat == "json" {
		printCommandJSONOutput("mcp", options.JSONEnvelope, authListPayload(tokens))
		return
	}
	if len(tokens) == 0 {
		fmt.Printf("没有已保存的 OAuth 令牌（%s）\n", store.Path())
		return
	}
	fmt.Printf("MCP OAuth 令牌（%s）:\n", store.Path())
	fmt.Println("─────────────────────────────────────────")
	for _, token := range tokens {
		expiry := "(无过期信息)"
		if !token.ExpiresAt.IsZero() {
			expiry = token.ExpiresAt.Format(time.RFC3339)
		}
		refresh := "否"
		if strings.TrimSpace(token.RefreshToken) != "" {
			refresh = "是"
		}
		fmt.Printf("  %s\n", token.ServerName)
		fmt.Printf("    地址: %s\n", token.ServerURL)
		fmt.Printf("    过期时间: %s\n", expiry)
		fmt.Printf("    可自动刷新: %s\n", refresh)
		if token.Scope != "" {
			fmt.Printf("    scope: %s\n", token.Scope)
		}
		fmt.Println()
	}
}

// authListPayload 构造令牌清单输出：只含元数据，绝不包含 access/refresh token 明文。
func authListPayload(tokens []*auth.Token) []map[string]interface{} {
	safe := make([]map[string]interface{}, 0, len(tokens))
	for _, token := range tokens {
		if token == nil {
			continue
		}
		safe = append(safe, map[string]interface{}{
			"serverName":          token.ServerName,
			"serverUrl":           token.ServerURL,
			"authorizationServer": token.AuthServer,
			"scope":               token.Scope,
			"expiresAt":           token.ExpiresAt,
			"obtainedAt":          token.ObtainedAt,
			"hasRefreshToken":     strings.TrimSpace(token.RefreshToken) != "",
			"clientId":            token.ClientID,
		})
	}
	return safe
}

func runMCPAuthClear(name string, all bool, options mcpCommandOptions) {
	store, err := auth.NewTokenStore("")
	if err != nil {
		exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "auth", "mode": "clear"})
	}
	name = strings.TrimSpace(name)
	if name == "" && !all {
		exitCommandError("mcp", options.OutputFormat,
			fmt.Errorf("需要指定 MCP 名称，或使用 --all 清除全部令牌"),
			map[string]interface{}{"subcommand": "auth", "mode": "clear"})
	}

	if all {
		count, err := store.DeleteAll()
		if err != nil {
			exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "auth", "mode": "clear"})
		}
		if options.OutputFormat == "json" {
			printCommandJSONOutput("mcp", options.JSONEnvelope, map[string]interface{}{"action": "logout", "cleared": count})
			return
		}
		fmt.Printf("已清除 %d 个 server 的 OAuth 令牌\n", count)
		return
	}

	deleted, err := store.Delete(name)
	if err != nil {
		exitCommandError("mcp", options.OutputFormat, err, map[string]interface{}{"subcommand": "auth", "mode": "clear", "mcpName": name})
	}
	if options.OutputFormat == "json" {
		printCommandJSONOutput("mcp", options.JSONEnvelope, map[string]interface{}{"action": "logout", "mcpName": name, "cleared": deleted})
		return
	}
	if deleted {
		fmt.Printf("已清除 MCP '%s' 的 OAuth 令牌\n", name)
		return
	}
	fmt.Printf("MCP '%s' 没有已保存的 OAuth 令牌\n", name)
}

func sortSessionStatuses(statuses []auth.SessionStatus) {
	for i := 1; i < len(statuses); i++ {
		for j := i; j > 0 && statuses[j-1].ServerName > statuses[j].ServerName; j-- {
			statuses[j-1], statuses[j] = statuses[j], statuses[j-1]
		}
	}
}
