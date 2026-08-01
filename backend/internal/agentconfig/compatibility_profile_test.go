package agentconfig

import "testing"

func TestValidateCompatibilityProfile(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		profile  string
		wantErr  bool
	}{
		{name: "empty profile is standard", protocol: "openai", profile: "", wantErr: false},
		{name: "empty profile any protocol", protocol: "anthropic", profile: "", wantErr: false},
		{name: "explicit standard openai", protocol: "openai", profile: CompatibilityProfileStandard, wantErr: false},
		{name: "explicit standard anthropic", protocol: "anthropic", profile: "standard", wantErr: false},
		{name: "standard is case insensitive", protocol: "openai", profile: "STANDARD", wantErr: false},
		{name: "opencode with openai", protocol: "openai", profile: CompatibilityProfileOpenCodeConsoleGo, wantErr: false},
		{name: "opencode with codex", protocol: "codex", profile: CompatibilityProfileOpenCodeConsoleGo, wantErr: false},
		{name: "opencode normalizes case and whitespace", protocol: " OpenAI ", profile: "  Opencode-Console-Go-2026-07 ", wantErr: false},
		{name: "opencode rejects anthropic", protocol: "anthropic", profile: CompatibilityProfileOpenCodeConsoleGo, wantErr: true},
		{name: "opencode rejects empty protocol", protocol: "", profile: CompatibilityProfileOpenCodeConsoleGo, wantErr: true},
		{name: "unknown profile", protocol: "openai", profile: "totally-unknown", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCompatibilityProfile(tc.protocol, tc.profile)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for protocol=%q profile=%q, got nil", tc.protocol, tc.profile)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for protocol=%q profile=%q: %v", tc.protocol, tc.profile, err)
			}
		})
	}
}
