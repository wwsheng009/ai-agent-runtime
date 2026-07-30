package commands

import (
	"strings"
	"testing"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/siteaccount"
)

func TestFormatProviderAccountBalanceLine_FromCachedSnapshot(t *testing.T) {
	remaining := 12.34
	account := &config.ProviderAccountSnapshot{
		Source:           "sub2api",
		Mode:             "subscription",
		Currency:         "USD",
		QuotaRemaining:   &remaining,
		QuotaDisplayUnit: "USD",
		FetchedAt:        "2026-07-29T12:00:00Z",
	}
	line := formatProviderAccountBalanceLine(account, "sub2api", "high")
	if line == "" {
		t.Fatal("expected non-empty balance line")
	}
	if !strings.Contains(line, "12.34") {
		t.Fatalf("expected balance value in line, got %q", line)
	}
	if !strings.Contains(line, "source=sub2api") {
		t.Fatalf("expected source in line, got %q", line)
	}
}

func TestRunBalanceCommand_UsesCachedAccount(t *testing.T) {
	remaining := 5.5
	cfg := &config.Config{
		ConfigFilePath: "mem://test-config.yaml",
		Providers: config.ProvidersConfig{
			DefaultProvider: "alpha",
			Items: map[string]config.Provider{
				"alpha": {
					Enabled:  true,
					Protocol: "openai",
					BaseURL:  "https://example.test",
					SiteType: string(siteaccount.SiteTypeSub2API),
					Account: &config.ProviderAccountSnapshot{
						Source:           "sub2api",
						Mode:             "subscription",
						QuotaRemaining:   &remaining,
						QuotaDisplayUnit: "USD",
						FetchedAt:        "2026-07-29T01:02:03Z",
					},
				},
				"beta": {
					Enabled:  true,
					Protocol: "openai",
					BaseURL:  "https://other.test",
				},
			},
		},
	}

	result, _, err := runBalanceCommand(cfg, balanceCommandOptions{})
	if err != nil {
		t.Fatalf("runBalanceCommand: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("expected 2 providers, got %d", result.Total)
	}
	if result.WithAccount != 1 {
		t.Fatalf("expected 1 with account, got %d", result.WithAccount)
	}
	var alpha *balanceProviderRow
	for i := range result.Providers {
		if result.Providers[i].Name == "alpha" {
			alpha = &result.Providers[i]
			break
		}
	}
	if alpha == nil {
		t.Fatal("alpha provider missing")
	}
	if alpha.Status != "cached" {
		t.Fatalf("expected cached status, got %q", alpha.Status)
	}
	if !strings.Contains(alpha.BalanceLine, "5.5") {
		t.Fatalf("expected cached balance line, got %q", alpha.BalanceLine)
	}
}

func TestRunBalanceCommand_ProviderFilter(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.Provider{
				"alpha": {Enabled: true, Protocol: "openai"},
				"beta":  {Enabled: true, Protocol: "openai"},
			},
		},
	}
	result, _, err := runBalanceCommand(cfg, balanceCommandOptions{Provider: "beta"})
	if err != nil {
		t.Fatalf("runBalanceCommand: %v", err)
	}
	if result.Total != 1 || result.Providers[0].Name != "beta" {
		t.Fatalf("expected only beta, got %+v", result.Providers)
	}
}

func TestBuildChatStatusBalanceValue_FromSessionAccount(t *testing.T) {
	oldLocal := time.Local
	time.Local = time.FixedZone("CST", 8*60*60)
	t.Cleanup(func() { time.Local = oldLocal })

	remaining := 9.0
	session := &ChatSession{
		ProviderName: "sub2",
		Provider: config.Provider{
			SiteType: string(siteaccount.SiteTypeSub2API),
			Account: &config.ProviderAccountSnapshot{
				Source:           "sub2api",
				Mode:             "subscription",
				QuotaRemaining:   &remaining,
				QuotaDisplayUnit: "USD",
				FetchedAt:        "2026-07-29T08:00:00Z",
			},
		},
	}
	got := buildChatStatusBalanceValue(session)
	if !strings.Contains(got, "9") {
		t.Fatalf("expected balance value, got %q", got)
	}
	if !strings.Contains(got, "fetched") {
		t.Fatalf("expected fetched timestamp annotation, got %q", got)
	}
	if !strings.Contains(got, "2026-07-29 16:00:00 +08:00") {
		t.Fatalf("expected fetched timestamp in local time, got %q", got)
	}
	if strings.Contains(got, "2026-07-29T08:00:00Z") {
		t.Fatalf("expected raw UTC timestamp to be formatted, got %q", got)
	}
}

func TestFormatChatStatusLocalTime_PreservesInvalidValue(t *testing.T) {
	if got := formatChatStatusLocalTime(" legacy timestamp "); got != "legacy timestamp" {
		t.Fatalf("expected invalid legacy timestamp to be preserved, got %q", got)
	}
}

func TestBuildChatStatusBalanceValue_None(t *testing.T) {
	session := &ChatSession{
		ProviderName: "plain",
		Provider:     config.Provider{Protocol: "openai"},
	}
	if got := buildChatStatusBalanceValue(session); got != "<none>" {
		t.Fatalf("expected <none>, got %q", got)
	}
}
