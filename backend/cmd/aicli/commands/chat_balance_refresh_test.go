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
			name: "cached source",
			provider: config.Provider{Account: &config.ProviderAccountSnapshot{
				Source: string(siteaccount.SiteTypeSub2API),
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
	if got := *provider.Account.QuotaRemaining; got < 2 {
		t.Fatalf("expected latest periodic balance, got %v", got)
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
	plain := style.StatusLineDocument(model, 120).PlainText()
	if !strings.Contains(plain, "Bal 25.75 USD") {
		t.Fatalf("expected refreshed balance in cached footer, got %q", plain)
	}
	if strings.Contains(plain, "Bal 1.00 USD") {
		t.Fatalf("stale balance remained in cached footer: %q", plain)
	}
}
