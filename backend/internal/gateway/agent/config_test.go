package agent

import "testing"

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"disabled needs nothing", Config{Enabled: false}, false},
		{"enabled requires url", Config{Enabled: true, Token: "agw_x"}, true},
		{"enabled requires token", Config{Enabled: true, URL: "wss://gw"}, true},
		{"enabled complete", Config{Enabled: true, URL: "wss://gw", Token: "agw_x"}, false},
		{"whitespace only is empty", Config{Enabled: true, URL: "  ", Token: "  "}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestConfigTokenHint(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  string
	}{
		{"empty", "", ""},
		{"whitespace", "   ", ""},
		{"long", "agw_abcdef123456", "agw_…"},
		{"short fully masked", "abc", "•••"},
		{"exactly four masked", "abcd", "••••"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Config{Token: tc.token}.TokenHint()
			if got != tc.want {
				t.Fatalf("TokenHint(%q) = %q, want %q", tc.token, got, tc.want)
			}
		})
	}
}

func TestConfigFromEnvDefaults(t *testing.T) {
	cfg := ConfigFromEnv(func(string) string { return "" })
	if cfg.Enabled {
		t.Fatal("empty env must not be enabled")
	}
	if !cfg.AutoConnect {
		t.Fatal("auto-connect must default to true")
	}
}

func TestConfigFromEnvConfigured(t *testing.T) {
	env := map[string]string{
		EnvURL:         " wss://gw.example.com ",
		EnvToken:       "agw_secret",
		EnvName:        "laptop",
		EnvAutoConnect: "false",
	}
	cfg := ConfigFromEnv(func(key string) string { return env[key] })
	if !cfg.Enabled {
		t.Fatal("url+token present must be enabled")
	}
	if cfg.URL != "wss://gw.example.com" || cfg.Token != "agw_secret" || cfg.Name != "laptop" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.AutoConnect {
		t.Fatal("auto-connect must be disabled for false")
	}
}

func TestConfigFromEnvAutoConnectVariants(t *testing.T) {
	base := map[string]string{EnvURL: "wss://gw", EnvToken: "agw_x"}
	for _, raw := range []string{"0", "false", "FALSE"} {
		env := map[string]string{EnvURL: base[EnvURL], EnvToken: base[EnvToken], EnvAutoConnect: raw}
		if ConfigFromEnv(func(k string) string { return env[k] }).AutoConnect {
			t.Fatalf("auto-connect must be false for %q", raw)
		}
	}
	for _, raw := range []string{"1", "true", "yes"} {
		env := map[string]string{EnvURL: base[EnvURL], EnvToken: base[EnvToken], EnvAutoConnect: raw}
		if !ConfigFromEnv(func(k string) string { return env[k] }).AutoConnect {
			t.Fatalf("auto-connect must be true for %q", raw)
		}
	}
}
