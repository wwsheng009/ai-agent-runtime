package agent

import (
	"fmt"
	"os"
	"strings"
)

// Environment variable names for gateway configuration, shared by every host
// (docs/gateway/12-connection-control.md §6.2).
const (
	EnvURL         = "AICLI_GATEWAY_URL"
	EnvToken       = "AICLI_GATEWAY_TOKEN"
	EnvName        = "AICLI_GATEWAY_NAME"
	EnvAutoConnect = "AICLI_GATEWAY_AUTO_CONNECT"
	EnvProxyURL    = "AICLI_GATEWAY_PROXY_URL"
)

// ConfigFromEnv resolves the gateway configuration from environment variables.
// Enabled is derived from URL+token presence; AutoConnect defaults to true
// unless explicitly disabled ("false"/"0"). getenv may be nil (os.Getenv).
func ConfigFromEnv(getenv func(string) string) Config {
	if getenv == nil {
		getenv = os.Getenv
	}
	auto := true
	if raw := strings.TrimSpace(getenv(EnvAutoConnect)); raw != "" {
		auto = !strings.EqualFold(raw, "false") && raw != "0"
	}
	url := strings.TrimSpace(getenv(EnvURL))
	token := strings.TrimSpace(getenv(EnvToken))
	return Config{
		Enabled:     url != "" && token != "",
		URL:         url,
		Token:       token,
		Name:        strings.TrimSpace(getenv(EnvName)),
		AutoConnect: auto,
		ProxyURL:    strings.TrimSpace(getenv(EnvProxyURL)),
	}
}

// Config is the gateway connection configuration shared by both hosts. It maps
// one-to-one onto the layered-config keys documented in
// docs/gateway/12-connection-control.md (gateway.enabled/url/token/name/
// auto_connect/routes) and onto the aicli --gateway* flags.
type Config struct {
	// Enabled is the persisted switch: whether this process may register at all.
	Enabled bool `json:"enabled"`
	// URL is the gateway endpoint, e.g. wss://gw.example.com (or https://).
	URL string `json:"url"`
	// Token is the device registration token (secret). It is never exposed
	// through Status or any control API; only TokenHint is.
	Token string `json:"token,omitempty"`
	// Name is the human-readable node/device name.
	Name string `json:"name,omitempty"`
	// AutoConnect mirrors the persisted "connect at startup" preference.
	AutoConnect bool `json:"auto_connect"`
	// Routes overrides the default reverse-proxy route allowlist (advanced).
	Routes []string `json:"routes,omitempty"`
	// ProxyURL, when set, tunnels the agent→gateway connection through an HTTP
	// CONNECT or SOCKS5 proxy (e.g. socks5://127.0.0.1:1080). Applies only to
	// the tunnel; reverse-proxy traffic to the local loopback is unaffected.
	ProxyURL string `json:"proxy_url,omitempty"`
}

// Mode values reported by Status.Mode.
const (
	// ModeAuto means the current connection is config-driven (startup / reconnect).
	ModeAuto = "auto"
	// ModeManual means the current connection was triggered by an explicit
	// connect command.
	ModeManual = "manual"
)

// Validate reports whether the configuration is usable for a connect attempt.
// A disabled configuration needs nothing else; an enabled one requires a URL
// and a token.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.URL) == "" {
		return fmt.Errorf("gateway url is empty")
	}
	if strings.TrimSpace(c.Token) == "" {
		return fmt.Errorf("gateway token is empty")
	}
	return nil
}

// TokenHint returns a non-secret fingerprint of the token for display. The full
// token is never returned; short tokens are fully masked so the hint can never
// reconstruct a small secret.
func (c Config) TokenHint() string {
	token := strings.TrimSpace(c.Token)
	if token == "" {
		return ""
	}
	if len(token) <= 4 {
		return strings.Repeat("•", len([]rune(token)))
	}
	return token[:4] + "…"
}
