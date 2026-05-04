package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

const (
	defaultCodexOAuthIssuer   = "https://auth.openai.com"
	defaultCodexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
)

type codexDeviceCode struct {
	VerificationURL string
	UserCode        string
	DeviceAuthID    string
	Interval        time.Duration
}

type codexDeviceTokenCode struct {
	AuthorizationCode string
	CodeChallenge     string
	CodeVerifier      string
}

type codexOAuthTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func runCodexOAuthDeviceLogin(ctx context.Context, req providerLoginRequest, providerName string) (*config.ProviderAuthRecord, string, error) {
	issuer := strings.TrimRight(strings.TrimSpace(req.OAuthIssuer), "/")
	if issuer == "" {
		issuer = defaultCodexOAuthIssuer
	}
	clientID := strings.TrimSpace(req.OAuthClientID)
	if clientID == "" {
		clientID = defaultCodexOAuthClientID
	}
	authRef := strings.TrimSpace(req.AuthRef)
	if authRef == "" {
		authRef = providerName
	}
	timeout := req.OAuthTimeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}

	deviceCode, err := requestCodexDeviceCode(ctx, issuer, clientID)
	if err != nil {
		return nil, "", err
	}
	if req.Prompter != nil {
		req.Prompter.PrintLine("Codex OAuth device-code 登录:")
		req.Prompter.PrintLine("  打开: " + deviceCode.VerificationURL)
		req.Prompter.PrintLine("  输入代码: " + deviceCode.UserCode)
	}
	tokenCode, err := pollCodexDeviceToken(ctx, issuer, deviceCode, timeout)
	if err != nil {
		return nil, "", err
	}
	tokens, err := exchangeCodexDeviceToken(ctx, issuer, clientID, tokenCode)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(tokens.AccessToken) == "" {
		return nil, "", fmt.Errorf("oauth token response did not include access_token")
	}

	record := config.ProviderAuthRecord{
		KeyType:      config.AuthKeyTypeOAuth,
		AuthMode:     providerAuthModeOAuth,
		Issuer:       issuer,
		ClientID:     clientID,
		IDToken:      tokens.IDToken,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	return &record, authRef, nil
}

func requestCodexDeviceCode(ctx context.Context, issuer, clientID string) (*codexDeviceCode, error) {
	endpoint := issuer + "/api/accounts/deviceauth/usercode"
	body, _ := json.Marshal(map[string]string{"client_id": clientID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create device-code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request device code: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read device-code response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("device code login is not enabled for this Codex server")
		}
		return nil, fmt.Errorf("device-code request returned HTTP %d: %s", resp.StatusCode, responsePreview(raw, 400))
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("parse device-code response: %w", err)
	}
	deviceAuthID := stringFromAny(decoded["device_auth_id"])
	userCode := stringFromAny(decoded["user_code"])
	if userCode == "" {
		userCode = stringFromAny(decoded["usercode"])
	}
	if deviceAuthID == "" || userCode == "" {
		return nil, fmt.Errorf("device-code response missing device_auth_id or user_code")
	}
	interval := durationSecondsFromAny(decoded["interval"])
	if interval <= 0 {
		interval = time.Second
	}
	return &codexDeviceCode{
		VerificationURL: issuer + "/codex/device",
		UserCode:        userCode,
		DeviceAuthID:    deviceAuthID,
		Interval:        interval,
	}, nil
}

func pollCodexDeviceToken(ctx context.Context, issuer string, deviceCode *codexDeviceCode, maxWait time.Duration) (*codexDeviceTokenCode, error) {
	if deviceCode == nil {
		return nil, fmt.Errorf("device code is nil")
	}
	endpoint := issuer + "/api/accounts/deviceauth/token"
	deadline := time.Now().Add(maxWait)
	for {
		body, _ := json.Marshal(map[string]string{
			"device_auth_id": deviceCode.DeviceAuthID,
			"user_code":      deviceCode.UserCode,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create device-token poll request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("poll device token: %w", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read device-token response: %w", readErr)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var decoded map[string]interface{}
			if err := json.Unmarshal(raw, &decoded); err != nil {
				return nil, fmt.Errorf("parse device-token response: %w", err)
			}
			tokenCode := &codexDeviceTokenCode{
				AuthorizationCode: stringFromAny(decoded["authorization_code"]),
				CodeChallenge:     stringFromAny(decoded["code_challenge"]),
				CodeVerifier:      stringFromAny(decoded["code_verifier"]),
			}
			if tokenCode.AuthorizationCode == "" || tokenCode.CodeVerifier == "" {
				return nil, fmt.Errorf("device-token response missing authorization_code or code_verifier")
			}
			return tokenCode, nil
		}
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			return nil, fmt.Errorf("device-token poll returned HTTP %d: %s", resp.StatusCode, responsePreview(raw, 400))
		}
		if time.Now().Add(deviceCode.Interval).After(deadline) {
			return nil, fmt.Errorf("device auth timed out after %s", maxWait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(deviceCode.Interval):
		}
	}
}

func exchangeCodexDeviceToken(ctx context.Context, issuer, clientID string, tokenCode *codexDeviceTokenCode) (*codexOAuthTokens, error) {
	if tokenCode == nil {
		return nil, fmt.Errorf("device token code is nil")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", tokenCode.AuthorizationCode)
	form.Set("redirect_uri", issuer+"/deviceauth/callback")
	form.Set("client_id", clientID)
	form.Set("code_verifier", tokenCode.CodeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create oauth token exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exchange oauth token: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read oauth token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oauth token endpoint returned HTTP %d: %s", resp.StatusCode, responsePreview(raw, 400))
	}
	tokens := &codexOAuthTokens{}
	if err := json.Unmarshal(raw, tokens); err != nil {
		return nil, fmt.Errorf("parse oauth token response: %w", err)
	}
	return tokens, nil
}

func stringFromAny(value interface{}) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func durationSecondsFromAny(value interface{}) time.Duration {
	switch typed := value.(type) {
	case float64:
		return time.Duration(typed) * time.Second
	case int:
		return time.Duration(typed) * time.Second
	case string:
		var seconds int
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &seconds); err == nil {
			return time.Duration(seconds) * time.Second
		}
	}
	return 0
}
