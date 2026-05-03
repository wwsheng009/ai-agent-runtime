package commands

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"golang.org/x/term"
)

// NewLoginCommand creates the provider login command.
func NewLoginCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "新增或更新 provider 登录凭证",
		Long:  "新增 provider 或修改现有 provider 的 base_url/API key/OAuth 凭证，校验 models endpoint 后写回 config.yaml。",
		Example: `  aicli login --provider openai --protocol openai --base-url https://api.openai.com --api-key sk-...
  aicli login --provider local --protocol openai --base-url http://127.0.0.1:4000 --models-path /v1/models
  aicli login --provider codex --protocol codex-apikey --base-url https://api.openai.com --api-key sk-...
  aicli login --provider openai --base-url https://new.example.com --output json`,
		Run: func(cmd *cobra.Command, args []string) {
			HandleLogin(cmd, configProvider)
		},
	}
	cmd.Flags().StringP("provider", "p", "", "provider 名称；不存在时新建，存在时更新")
	cmd.Flags().String("protocol", "", "登录协议（openai|anthropic|gemini|codex-apikey|codex-oauth）")
	cmd.Flags().String("mode", "", "认证模式（apikey|oauth，可由 protocol 推断）")
	cmd.Flags().String("base-url", "", "provider base URL")
	cmd.Flags().String("api-key", "", "API key；交互模式下建议留空后隐藏输入")
	cmd.Flags().String("models-path", "", "覆盖模型列表 endpoint")
	cmd.Flags().String("default-model", "", "登录成功后写入的默认模型")
	cmd.Flags().Bool("set-default", false, "登录成功后同步更新 providers.default_provider")
	cmd.Flags().Bool("dry-run", false, "只校验凭证和 models，不写配置")
	cmd.Flags().BoolP("yes", "y", false, "跳过确认；不会跳过 models 校验")
	cmd.Flags().String("auth-ref", "", "OAuth auth store 引用名")
	cmd.Flags().String("oauth-issuer", "", "Codex OAuth issuer（默认 https://auth.openai.com）")
	cmd.Flags().String("oauth-client-id", "", "Codex OAuth client id")
	cmd.Flags().Int("oauth-timeout", 900, "Codex OAuth device-code 等待秒数")
	cmd.Flags().Int("timeout", 60, "models 校验请求超时秒数")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func HandleLogin(cmd *cobra.Command, configProvider func() *config.Config) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("login", "json", err, nil)
	}
	cfg := (*config.Config)(nil)
	if configProvider != nil {
		cfg = configProvider()
	}
	timeoutSec, _ := cmd.Flags().GetInt("timeout")
	oauthTimeoutSec, _ := cmd.Flags().GetInt("oauth-timeout")
	req := providerLoginRequest{
		Config:        cfg,
		ProviderName:  stringFlag(cmd, "provider"),
		LoginProtocol: stringFlag(cmd, "protocol"),
		AuthMode:      stringFlag(cmd, "mode"),
		BaseURL:       stringFlag(cmd, "base-url"),
		APIKey:        stringFlag(cmd, "api-key"),
		ModelsPath:    stringFlag(cmd, "models-path"),
		DefaultModel:  stringFlag(cmd, "default-model"),
		SetDefault:    boolFlag(cmd, "set-default"),
		DryRun:        boolFlag(cmd, "dry-run"),
		Yes:           boolFlag(cmd, "yes"),
		Interactive:   !isJSONOutputFormat(outputOptions.Format),
		Timeout:       time.Duration(timeoutSec) * time.Second,
		AuthRef:       stringFlag(cmd, "auth-ref"),
		OAuthIssuer:   stringFlag(cmd, "oauth-issuer"),
		OAuthClientID: stringFlag(cmd, "oauth-client-id"),
		OAuthTimeout:  time.Duration(oauthTimeoutSec) * time.Second,
	}
	if req.Interactive {
		req.Prompter = newCLILoginPrompter()
	}

	executeCommand("login", outputOptions, func() (*providerLoginResult, map[string]interface{}, error) {
		result, err := runProviderLogin(req)
		return result, nil, err
	}, renderLoginCommandResult)
}

func renderLoginCommandResult(result *providerLoginResult, outputOptions structuredOutputOptions) {
	if result == nil {
		return
	}
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("login", outputOptions.Envelope, result)
		return
	}
	fmt.Println("Provider 登录成功")
	fmt.Printf("  Provider:        %s\n", result.ProviderName)
	fmt.Printf("  Protocol:        %s (%s)\n", result.Protocol, result.LoginProtocol)
	fmt.Printf("  Auth mode:       %s\n", result.AuthMode)
	if result.AuthRef != "" {
		fmt.Printf("  Auth ref:        %s\n", result.AuthRef)
	}
	if result.APIKeyMasked != "" && result.AuthMode != providerAuthModeOAuth {
		fmt.Printf("  API key:         %s\n", result.APIKeyMasked)
	}
	fmt.Printf("  Base URL:        %s\n", result.BaseURL)
	fmt.Printf("  Models endpoint: %s\n", result.ModelsEndpoint)
	fmt.Printf("  Default model:   %s\n", result.DefaultModel)
	fmt.Printf("  Models:          %d\n", len(result.SupportedModels))
	for _, model := range previewModelList(result.SupportedModels, 20) {
		fmt.Printf("    - %s\n", model)
	}
	if result.DryRun {
		fmt.Println("  Config:          dry-run，未写入")
	} else if result.ConfigPath != "" {
		fmt.Printf("  Config:          %s\n", result.ConfigPath)
	}
	if result.AuthStorePath != "" {
		fmt.Printf("  Auth store:      %s\n", result.AuthStorePath)
	}
}

type cliLoginPrompter struct {
	reader *bufio.Reader
}

func newCLILoginPrompter() *cliLoginPrompter {
	return &cliLoginPrompter{reader: bufio.NewReader(os.Stdin)}
}

func (p *cliLoginPrompter) PrintLine(line string) {
	fmt.Println(line)
}

func (p *cliLoginPrompter) PromptText(label, current string, required bool) (string, error) {
	for {
		prompt := label + ": "
		if strings.TrimSpace(current) != "" {
			prompt = fmt.Sprintf("%s [%s]: ", label, current)
		}
		fmt.Print(prompt)
		line, err := p.reader.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return "", err
		}
		value := strings.TrimSpace(line)
		if value == "" {
			value = strings.TrimSpace(current)
		}
		if value != "" || !required {
			return value, nil
		}
		fmt.Println("该字段不能为空")
	}
}

func (p *cliLoginPrompter) PromptSecret(label, currentMasked string, required bool) (string, error) {
	for {
		prompt := label + ": "
		if strings.TrimSpace(currentMasked) != "" {
			prompt = fmt.Sprintf("%s [%s，回车保持不变]: ", label, currentMasked)
		}
		fmt.Print(prompt)
		var raw []byte
		var err error
		if term.IsTerminal(int(os.Stdin.Fd())) {
			raw, err = term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
		} else {
			line, readErr := p.reader.ReadString('\n')
			raw = []byte(line)
			err = readErr
		}
		value := strings.TrimSpace(string(raw))
		if err != nil && value == "" {
			return "", err
		}
		if value != "" || !required {
			return value, nil
		}
		fmt.Println("该字段不能为空")
	}
}

func stringFlag(cmd *cobra.Command, name string) string {
	value, _ := cmd.Flags().GetString(name)
	return strings.TrimSpace(value)
}

func boolFlag(cmd *cobra.Command, name string) bool {
	value, _ := cmd.Flags().GetBool(name)
	return value
}

func previewModelList(models []string, limit int) []string {
	if limit <= 0 || len(models) <= limit {
		return models
	}
	out := append([]string(nil), models[:limit]...)
	out = append(out, fmt.Sprintf("... (%d more)", len(models)-limit))
	return out
}
