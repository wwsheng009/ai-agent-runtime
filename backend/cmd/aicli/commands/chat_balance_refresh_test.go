package commands

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/siteaccount"
)

func TestPrepareProviderForPeriodicBalanceRefresh(t *testing.T) {
	tests := []struct {
		name     string
		provider config.Provider
		want     bool
	}{
		{
			name:     "sub2api",
			provider: config.Provider{SiteType: string(siteaccount.SiteTypeSub2API)},
			want:     true,
		},
		{
			name:     "new-api",
			provider: config.Provider{SiteType: string(siteaccount.SiteTypeNewAPI)},
			want:     true,
		},
		{
			name: "deepseek",
			provider: config.Provider{
				Protocol: "codex",
				SiteType: string(siteaccount.SiteTypeDeepSeek),
			},
			want: true,
		},
		{
			name: "cached source",
			provider: config.Provider{Account: &config.ProviderAccountSnapshot{
				Source: string(siteaccount.SiteTypeSub2API),
			}},
			want: true,
		},
		{
			name: "cached deepseek source",
			provider: config.Provider{Account: &config.ProviderAccountSnapshot{
				Source: "deepseek_user_balance",
			}},
			want: true,
		},
		{
			name:     "ordinary openai provider",
			provider: config.Provider{Protocol: "openai"},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := tt.provider
			if got := prepareProviderForPeriodicBalanceRefresh(&provider); got != tt.want {
				t.Fatalf("eligible = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestChatAccountBalanceRefresherRefreshesImmediatelyAndPeriodically(t *testing.T) {
	initial := 1.0
	session := &ChatSession{}
	session.setAccountBalanceProvider("sub2", config.Provider{
		SiteType: string(siteaccount.SiteTypeSub2API),
		Account: &config.ProviderAccountSnapshot{
			Source:           string(siteaccount.SiteTypeSub2API),
			QuotaRemaining:   &initial,
			QuotaDisplayUnit: "USD",
		},
	})

	var calls atomic.Int32
	refresh := func(
		_ context.Context,
		_ *siteaccount.Client,
		_ string,
		_ *config.Provider,
		_ time.Duration,
	) (liveBalanceOutcome, error) {
		value := float64(calls.Add(1))
		return liveBalanceOutcome{
			Status:   "ok",
			SiteType: string(siteaccount.SiteTypeSub2API),
			Account: &config.ProviderAccountSnapshot{
				Source:           string(siteaccount.SiteTypeSub2API),
				QuotaRemaining:   &value,
				QuotaDisplayUnit: "USD",
				FetchedAt:        time.Now().UTC().Format(time.RFC3339),
			},
		}, nil
	}

	refresher := newChatAccountBalanceRefresher(session, 10*time.Millisecond, refresh)
	refresher.setTarget("sub2", config.Provider{SiteType: string(siteaccount.SiteTypeSub2API)}, false)
	session.accountBalanceMu.Lock()
	session.accountBalanceRefresher = refresher
	session.accountBalanceMu.Unlock()
	go refresher.run()
	t.Cleanup(refresher.Stop)

	deadline := time.Now().Add(time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() < 2 {
		t.Fatalf("expected immediate and periodic refreshes, got %d call(s)", calls.Load())
	}

	_, provider, ok := session.accountBalanceSnapshot()
	if !ok || provider.Account == nil || provider.Account.QuotaRemaining == nil {
		t.Fatalf("expected refreshed account snapshot, got %+v", provider.Account)
	}
	if got := *provider.Account.QuotaRemaining; got < 1 {
		t.Fatalf("expected latest periodic balance, got %v", got)
	}
}

func TestChatAccountBalanceRefresherRefreshesDeepSeekCodexProvider(t *testing.T) {
	session := &ChatSession{
		Model:        "deepseek-v4-flash",
		ProviderName: "deepseek_codex",
	}
	balance := 110.0
	available := true
	initialProvider := config.Provider{
		Protocol: "codex",
		SiteType: string(siteaccount.SiteTypeDeepSeek),
	}
	session.setAccountBalanceProvider("deepseek_codex", initialProvider)
	refresher := newChatAccountBalanceRefresher(session, time.Minute, func(
		_ context.Context,
		_ *siteaccount.Client,
		providerName string,
		provider *config.Provider,
		_ time.Duration,
	) (liveBalanceOutcome, error) {
		if providerName != "deepseek_codex" {
			t.Fatalf("provider name = %q", providerName)
		}
		if provider.GetProtocol() != "codex" {
			t.Fatalf("protocol = %q", provider.GetProtocol())
		}
		return liveBalanceOutcome{
			Status:             "ok",
			SiteType:           string(siteaccount.SiteTypeDeepSeek),
			SiteTypeConfidence: string(siteaccount.ConfidenceHigh),
			Account: &config.ProviderAccountSnapshot{
				Source:        "deepseek_user_balance",
				Mode:          "wallet",
				Currency:      "CNY",
				WalletBalance: &balance,
				IsAvailable:   &available,
			},
		}, nil
	})
	t.Cleanup(func() { refresher.cancel() })
	refresher.setTarget("deepseek_codex", initialProvider, false)
	session.accountBalanceMu.Lock()
	session.accountBalanceRefresher = refresher
	session.accountBalanceMu.Unlock()

	refresher.refreshOnce()

	_, provider, ok := session.accountBalanceSnapshot()
	if !ok || provider.Account == nil || provider.Account.WalletBalance == nil {
		t.Fatalf("expected refreshed DeepSeek account snapshot, got %+v", provider.Account)
	}
	if got := *provider.Account.WalletBalance; got != balance {
		t.Fatalf("wallet balance = %v, want %v", got, balance)
	}
	if provider.SiteType != string(siteaccount.SiteTypeDeepSeek) {
		t.Fatalf("site type = %q", provider.SiteType)
	}
	line := buildChatSurfaceStatusLineForWidth(session, "Ready", 160)
	if !strings.Contains(line, "Balance 110.00 CNY") {
		t.Fatalf("expected DeepSeek balance in footer, got %q", line)
	}
}

func TestChatAccountBalanceRefresherDiscardsStaleProviderResult(t *testing.T) {
	oldValue := 1.0
	newValue := 20.0
	session := &ChatSession{}
	oldProvider := config.Provider{
		SiteType: string(siteaccount.SiteTypeSub2API),
		Account: &config.ProviderAccountSnapshot{
			Source:           string(siteaccount.SiteTypeSub2API),
			QuotaRemaining:   &oldValue,
			QuotaDisplayUnit: "USD",
		},
	}
	newProvider := config.Provider{
		SiteType: string(siteaccount.SiteTypeNewAPI),
		Account: &config.ProviderAccountSnapshot{
			Source:           string(siteaccount.SiteTypeNewAPI),
			QuotaRemaining:   &newValue,
			QuotaDisplayUnit: "USD",
		},
	}
	session.setAccountBalanceProvider("old", oldProvider)

	started := make(chan struct{})
	release := make(chan struct{})
	refresh := func(
		_ context.Context,
		_ *siteaccount.Client,
		_ string,
		_ *config.Provider,
		_ time.Duration,
	) (liveBalanceOutcome, error) {
		close(started)
		<-release
		staleValue := 99.0
		return liveBalanceOutcome{
			Status:   "ok",
			SiteType: string(siteaccount.SiteTypeSub2API),
			Account: &config.ProviderAccountSnapshot{
				Source:           string(siteaccount.SiteTypeSub2API),
				QuotaRemaining:   &staleValue,
				QuotaDisplayUnit: "USD",
			},
		}, nil
	}
	refresher := newChatAccountBalanceRefresher(session, time.Minute, refresh)
	refresher.setTarget("old", oldProvider, false)
	session.accountBalanceMu.Lock()
	session.accountBalanceRefresher = refresher
	session.accountBalanceMu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		refresher.refreshOnce()
	}()
	<-started
	updateChatAccountBalanceProvider(session, "new", newProvider)
	close(release)
	<-done
	refresher.cancel()

	name, provider, ok := session.accountBalanceSnapshot()
	if !ok || name != "new" || provider.Account == nil || provider.Account.QuotaRemaining == nil {
		t.Fatalf("expected new provider snapshot, got name=%q account=%+v", name, provider.Account)
	}
	if got := *provider.Account.QuotaRemaining; got != newValue {
		t.Fatalf("stale refresh overwrote new provider balance: got %v, want %v", got, newValue)
	}
}

func TestChatSurfaceStatusIncludesAccountBalance(t *testing.T) {
	// Pin an empty git branch: the status line embeds the real branch name,
	// and a long branch (e.g. feat/win7-go120-compat) can crowd out the
	// balance segment at the fixed render width.
	previousLookup := chatStatusGitBranchLookup
	chatStatusGitBranchLookup = func(string) string { return "" }
	defer func() { chatStatusGitBranchLookup = previousLookup }()
	resetChatStatusGitBranchCacheForTest()

	remaining := 12.34
	session := &ChatSession{
		Model:        "gpt-5.6-sol",
		ProviderName: "sub2",
		Provider: config.Provider{
			SiteType: string(siteaccount.SiteTypeSub2API),
			Account: &config.ProviderAccountSnapshot{
				Source:           string(siteaccount.SiteTypeSub2API),
				QuotaRemaining:   &remaining,
				QuotaDisplayUnit: "USD",
			},
		},
	}

	line := buildChatSurfaceStatusLineForWidth(session, "Ready", 120)
	if !strings.Contains(line, "Balance 12.34 USD") {
		t.Fatalf("expected balance in bottom status line, got %q", line)
	}
}

func TestUpdateChatAccountBalanceProviderKeepsLiveBalanceForSameProvider(t *testing.T) {
	liveValue := 42.5
	staleValue := 1.0
	session := &ChatSession{}
	session.setAccountBalanceProvider("sub2", config.Provider{
		SiteType: string(siteaccount.SiteTypeSub2API),
		Account: &config.ProviderAccountSnapshot{
			Source:           string(siteaccount.SiteTypeSub2API),
			QuotaRemaining:   &liveValue,
			QuotaDisplayUnit: "USD",
		},
	})

	updateChatAccountBalanceProvider(session, "SUB2", config.Provider{
		SiteType: string(siteaccount.SiteTypeSub2API),
		Account: &config.ProviderAccountSnapshot{
			Source:           string(siteaccount.SiteTypeSub2API),
			QuotaRemaining:   &staleValue,
			QuotaDisplayUnit: "USD",
		},
	})

	_, provider, ok := session.accountBalanceSnapshot()
	if !ok || provider.Account == nil || provider.Account.QuotaRemaining == nil {
		t.Fatalf("expected account snapshot, got %+v", provider.Account)
	}
	if got := *provider.Account.QuotaRemaining; got != liveValue {
		t.Fatalf("same-provider update rolled balance back to %v, want %v", got, liveValue)
	}
}

func TestRefreshAccountBalanceStatusReplacesCachedBalance(t *testing.T) {
	oldValue := 1.0
	newValue := 25.75
	session := &ChatSession{
		Model:        "gpt-5.6-sol",
		ProviderName: "sub2",
	}
	session.setAccountBalanceProvider("sub2", config.Provider{
		SiteType: string(siteaccount.SiteTypeSub2API),
		Account: &config.ProviderAccountSnapshot{
			Source:           string(siteaccount.SiteTypeSub2API),
			QuotaRemaining:   &oldValue,
			QuotaDisplayUnit: "USD",
		},
	})
	surface := ui.NewFixedBottomSurface(nil)
	surface.EnableForTest(120, 24)
	interaction := newChatInteractionCoordinator(session)
	interaction.SetSurface(surface)
	session.Interaction = interaction
	interaction.RefreshStatus("")

	interaction.mu.Lock()
	beforeModel := cloneChatStatusLineModel(interaction.persistentStatusModel)
	interaction.mu.Unlock()
	before := style.StatusLineDocument(beforeModel, 0).PlainText()
	balancePrefix := "Bal "
	if strings.Contains(before, "Balance 1.00 USD") {
		balancePrefix = "Balance "
	} else if !strings.Contains(before, "Bal 1.00 USD") {
		t.Fatalf("expected initial balance in cached footer, got %q", before)
	}

	session.setAccountBalanceProvider("sub2", config.Provider{
		SiteType: string(siteaccount.SiteTypeSub2API),
		Account: &config.ProviderAccountSnapshot{
			Source:           string(siteaccount.SiteTypeSub2API),
			QuotaRemaining:   &newValue,
			QuotaDisplayUnit: "USD",
		},
	})
	interaction.RefreshAccountBalanceStatus()

	interaction.mu.Lock()
	model := cloneChatStatusLineModel(interaction.persistentStatusModel)
	interaction.mu.Unlock()
	plain := style.StatusLineDocument(model, 0).PlainText()
	refreshedBalance := balancePrefix + "25.75 USD"
	if !strings.Contains(plain, refreshedBalance) {
		t.Fatalf("expected refreshed balance in cached footer, got %q", plain)
	}
	if strings.Contains(plain, "1.00 USD") {
		t.Fatalf("stale balance remained in cached footer: %q", plain)
	}
	providerAt := strings.Index(plain, "sub2")
	balanceAt := strings.Index(plain, refreshedBalance)
	contextAt := strings.Index(plain, "Ctx ")
	if contextAt < 0 {
		contextAt = strings.Index(plain, "Context ")
	}
	if providerAt < 0 || balanceAt <= providerAt || (contextAt >= 0 && balanceAt >= contextAt) {
		t.Fatalf("expected provider → balance → context order after refresh, got %q", plain)
	}
}

func TestRefreshChatAccountBalanceStatusModelUsesCanonicalOrderAndResponsiveLabel(t *testing.T) {
	base := style.StatusLineModel{
		HideState: true,
		Segments: []style.StatusSegment{
			{Kind: style.StatusSegMode, Text: "Plan OFF"},
			{Kind: style.StatusSegModel, Text: "gpt-5.6-sol xhigh"},
			{Kind: style.StatusSegProvider, Text: "mdkj"},
			{Kind: style.StatusSegUsage, Text: "Context 42% used"},
		},
	}
	balance := chatStatusSegment{
		full:    "Balance 170.52 USD",
		compact: "Bal 170.52 USD",
	}

	wide := refreshChatAccountBalanceStatusModel(base, balance, 200)
	if got, want := style.StatusLineDocument(wide, 0).PlainText(),
		"Plan OFF · gpt-5.6-sol xhigh · mdkj · Balance 170.52 USD · Context 42% used"; got != want {
		t.Fatalf("unexpected wide refreshed footer:\n got: %q\nwant: %q", got, want)
	}

	narrow := refreshChatAccountBalanceStatusModel(base, balance, 10)
	if got, want := style.StatusLineDocument(narrow, 0).PlainText(),
		"Plan OFF · gpt-5.6-sol xhigh · mdkj · Bal 170.52 USD · Context 42% used"; got != want {
		t.Fatalf("unexpected narrow refreshed footer:\n got: %q\nwant: %q", got, want)
	}

	existingFull := cloneChatStatusLineModel(base)
	existingFull.Segments = append(existingFull.Segments, style.StatusSegment{})
	copy(existingFull.Segments[4:], existingFull.Segments[3:])
	existingFull.Segments[3] = style.StatusSegment{
		Kind: style.StatusSegBalance,
		Text: "Balance 171.91 USD",
	}
	refreshed := refreshChatAccountBalanceStatusModel(existingFull, balance, 10)
	if got, want := style.StatusLineDocument(refreshed, 0).PlainText(),
		"Plan OFF · gpt-5.6-sol xhigh · mdkj · Balance 170.52 USD · Context 42% used"; got != want {
		t.Fatalf("incremental refresh changed the existing label or order:\n got: %q\nwant: %q", got, want)
	}
}
