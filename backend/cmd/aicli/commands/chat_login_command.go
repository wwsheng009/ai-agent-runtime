package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

type chatLoginCommandRequest struct {
	Provider      string
	Protocol      string
	Mode          string
	BaseURL       string
	APIKey        string
	ModelsPath    string
	DefaultModel  string
	AuthRef       string
	OAuthIssuer   string
	OAuthClientID string
	SetDefault    bool
	DryRun        bool
	Switch        bool
	TimeoutSec    int
	OAuthTimeout  int
}

func handleLoginCommand(session *ChatSession, command string, noInteractive bool) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	parsed, err := parseChatLoginCommandRequest(command)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	timeout := time.Duration(parsed.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	oauthTimeout := time.Duration(parsed.OAuthTimeout) * time.Second
	if oauthTimeout <= 0 {
		oauthTimeout = 15 * time.Minute
	}
	req := providerLoginRequest{
		Context:       context.Background(),
		Config:        session.Config,
		ProviderName:  parsed.Provider,
		LoginProtocol: parsed.Protocol,
		AuthMode:      parsed.Mode,
		BaseURL:       parsed.BaseURL,
		APIKey:        parsed.APIKey,
		ModelsPath:    parsed.ModelsPath,
		DefaultModel:  parsed.DefaultModel,
		SetDefault:    parsed.SetDefault,
		DryRun:        parsed.DryRun,
		Interactive:   !noInteractive,
		Timeout:       timeout,
		AuthRef:       parsed.AuthRef,
		OAuthIssuer:   parsed.OAuthIssuer,
		OAuthClientID: parsed.OAuthClientID,
		OAuthTimeout:  oauthTimeout,
	}
	if req.Interactive {
		req.Prompter = chatLoginPrompter{session: session}
	}
	beginDirectInteractiveOutput(session)
	result, err := runProviderLogin(req)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	renderLoginCommandResult(result, structuredOutputOptions{Format: "text"})
	refreshLoginSessionIfNeeded(session, result, parsed.Switch)
	return false
}

func parseChatLoginCommandRequest(command string) (chatLoginCommandRequest, error) {
	tokens := runtimeexecutor.SplitCommandTokens(command)
	req := chatLoginCommandRequest{}
	for i := 1; i < len(tokens); i++ {
		token := strings.TrimSpace(tokens[i])
		if token == "" {
			continue
		}
		switch {
		case token == "--provider" || token == "-p":
			value, next, err := consumeChatLoginValue(tokens, i)
			if err != nil {
				return req, err
			}
			req.Provider = value
			i = next
		case strings.HasPrefix(token, "--provider="):
			req.Provider = strings.TrimSpace(strings.TrimPrefix(token, "--provider="))
		case token == "--protocol":
			value, next, err := consumeChatLoginValue(tokens, i)
			if err != nil {
				return req, err
			}
			req.Protocol = value
			i = next
		case strings.HasPrefix(token, "--protocol="):
			req.Protocol = strings.TrimSpace(strings.TrimPrefix(token, "--protocol="))
		case token == "--mode":
			value, next, err := consumeChatLoginValue(tokens, i)
			if err != nil {
				return req, err
			}
			req.Mode = value
			i = next
		case strings.HasPrefix(token, "--mode="):
			req.Mode = strings.TrimSpace(strings.TrimPrefix(token, "--mode="))
		case token == "--base-url":
			value, next, err := consumeChatLoginValue(tokens, i)
			if err != nil {
				return req, err
			}
			req.BaseURL = value
			i = next
		case strings.HasPrefix(token, "--base-url="):
			req.BaseURL = strings.TrimSpace(strings.TrimPrefix(token, "--base-url="))
		case token == "--api-key":
			value, next, err := consumeChatLoginValue(tokens, i)
			if err != nil {
				return req, err
			}
			req.APIKey = value
			i = next
		case strings.HasPrefix(token, "--api-key="):
			req.APIKey = strings.TrimSpace(strings.TrimPrefix(token, "--api-key="))
		case token == "--models-path":
			value, next, err := consumeChatLoginValue(tokens, i)
			if err != nil {
				return req, err
			}
			req.ModelsPath = value
			i = next
		case strings.HasPrefix(token, "--models-path="):
			req.ModelsPath = strings.TrimSpace(strings.TrimPrefix(token, "--models-path="))
		case token == "--default-model":
			value, next, err := consumeChatLoginValue(tokens, i)
			if err != nil {
				return req, err
			}
			req.DefaultModel = value
			i = next
		case strings.HasPrefix(token, "--default-model="):
			req.DefaultModel = strings.TrimSpace(strings.TrimPrefix(token, "--default-model="))
		case token == "--auth-ref":
			value, next, err := consumeChatLoginValue(tokens, i)
			if err != nil {
				return req, err
			}
			req.AuthRef = value
			i = next
		case strings.HasPrefix(token, "--auth-ref="):
			req.AuthRef = strings.TrimSpace(strings.TrimPrefix(token, "--auth-ref="))
		case token == "--oauth-issuer":
			value, next, err := consumeChatLoginValue(tokens, i)
			if err != nil {
				return req, err
			}
			req.OAuthIssuer = value
			i = next
		case strings.HasPrefix(token, "--oauth-issuer="):
			req.OAuthIssuer = strings.TrimSpace(strings.TrimPrefix(token, "--oauth-issuer="))
		case token == "--oauth-client-id":
			value, next, err := consumeChatLoginValue(tokens, i)
			if err != nil {
				return req, err
			}
			req.OAuthClientID = value
			i = next
		case strings.HasPrefix(token, "--oauth-client-id="):
			req.OAuthClientID = strings.TrimSpace(strings.TrimPrefix(token, "--oauth-client-id="))
		case token == "--timeout":
			value, next, err := consumeChatLoginValue(tokens, i)
			if err != nil {
				return req, err
			}
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return req, fmt.Errorf("--timeout 需要正整数秒数")
			}
			req.TimeoutSec = parsed
			i = next
		case strings.HasPrefix(token, "--timeout="):
			parsed, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(token, "--timeout=")))
			if err != nil || parsed <= 0 {
				return req, fmt.Errorf("--timeout 需要正整数秒数")
			}
			req.TimeoutSec = parsed
		case token == "--oauth-timeout":
			value, next, err := consumeChatLoginValue(tokens, i)
			if err != nil {
				return req, err
			}
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return req, fmt.Errorf("--oauth-timeout 需要正整数秒数")
			}
			req.OAuthTimeout = parsed
			i = next
		case strings.HasPrefix(token, "--oauth-timeout="):
			parsed, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(token, "--oauth-timeout=")))
			if err != nil || parsed <= 0 {
				return req, fmt.Errorf("--oauth-timeout 需要正整数秒数")
			}
			req.OAuthTimeout = parsed
		case token == "--set-default":
			req.SetDefault = true
		case token == "--dry-run":
			req.DryRun = true
		case token == "--switch":
			req.Switch = true
		default:
			if strings.HasPrefix(token, "-") {
				return req, fmt.Errorf("未知 /login 参数: %s", token)
			}
			if req.Provider == "" {
				req.Provider = token
				continue
			}
			return req, fmt.Errorf("无法解析 /login 参数: %s", token)
		}
	}
	return req, nil
}

func consumeChatLoginValue(tokens []string, index int) (string, int, error) {
	if index+1 >= len(tokens) {
		return "", index, fmt.Errorf("参数 %s 缺少值", tokens[index])
	}
	value := strings.TrimSpace(tokens[index+1])
	if value == "" {
		return "", index, fmt.Errorf("参数 %s 缺少值", tokens[index])
	}
	return value, index + 1, nil
}

type chatLoginPrompter struct {
	session *ChatSession
}

func (p chatLoginPrompter) PrintLine(line string) {
	beginDirectInteractiveOutput(p.session)
	fmt.Println(line)
}

func (p chatLoginPrompter) PromptText(label, current string, required bool) (string, error) {
	for {
		prompt := label + ": "
		if strings.TrimSpace(current) != "" {
			prompt = fmt.Sprintf("%s [%s]: ", label, current)
		}
		beginDirectInteractiveOutput(p.session)
		fmt.Print(prompt)
		text, err := chatInteractiveReadPriorityLineWithPrompt(p.session, context.Background(), prompt)
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(normalizeQueuedInputLine(text))
		if value == "" {
			value = strings.TrimSpace(current)
		}
		if value != "" || !required {
			return value, nil
		}
		fmt.Println("该字段不能为空")
	}
}

func (p chatLoginPrompter) PromptSecret(label, currentMasked string, required bool) (string, error) {
	for {
		prompt := label + "（输入不会写入会话历史）: "
		if strings.TrimSpace(currentMasked) != "" {
			prompt = fmt.Sprintf("%s [%s，回车保持不变]: ", label, currentMasked)
		}
		beginDirectInteractiveOutput(p.session)
		text, err := chatInteractiveReadPrioritySecretWithPrompt(p.session, context.Background(), prompt)
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(normalizeQueuedInputLine(text))
		if value != "" || !required {
			return value, nil
		}
		fmt.Println("该字段不能为空")
	}
}

func refreshLoginSessionIfNeeded(session *ChatSession, result *providerLoginResult, switchProvider bool) {
	if session == nil || result == nil || result.DryRun {
		return
	}
	shouldRefresh := switchProvider || strings.EqualFold(strings.TrimSpace(session.ProviderName), strings.TrimSpace(result.ProviderName))
	if !shouldRefresh {
		return
	}
	providerCtx, _, err := resolveModelCommandExecutionContext(session, result.ProviderName, result.DefaultModel)
	if err != nil {
		fmt.Printf("Warning: 登录成功，但刷新当前会话失败: %v\n", err)
		return
	}
	if err := applyModelCommandSelection(session, providerCtx, providerCtx.RequestedModel, session.ReasoningEffort); err != nil {
		fmt.Printf("Warning: 登录成功，但刷新当前会话失败: %v\n", err)
		return
	}
	fmt.Println("当前 chat 会话已刷新到最新 provider 配置")
}
