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
