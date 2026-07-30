package commands

import (
	"context"
	"strings"
	"sync"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/siteaccount"
)

const chatAccountBalanceRefreshTimeout = 15 * time.Second

type chatAccountBalanceRefreshFunc func(
	context.Context,
	*siteaccount.Client,
	string,
	*config.Provider,
	time.Duration,
) (liveBalanceOutcome, error)

// chatAccountBalanceRefresher owns the periodic refresh lifecycle for one chat
// session. Refreshed account data stays in a session-local snapshot: background
// I/O never mutates the shared config provider map.
type chatAccountBalanceRefresher struct {
	session  *ChatSession
	interval time.Duration
	client   *siteaccount.Client
	refresh  chatAccountBalanceRefreshFunc

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{}

	mu           sync.Mutex
	providerName string
	provider     config.Provider
	generation   uint64
	stopOnce     sync.Once
}

func newChatAccountBalanceRefresher(
	session *ChatSession,
	interval time.Duration,
	refresh chatAccountBalanceRefreshFunc,
) *chatAccountBalanceRefresher {
	if interval <= 0 {
		interval = config.DefaultAICLIBalanceRefreshInterval
	}
	if refresh == nil {
		refresh = refreshProviderAccountBalance
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &chatAccountBalanceRefresher{
		session:  session,
		interval: interval,
		client:   siteaccount.NewClient(nil),
		refresh:  refresh,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
		wake:     make(chan struct{}, 1),
	}
}

func initializeChatAccountBalanceRefresh(session *ChatSession) {
	if session == nil {
		return
	}
	session.setAccountBalanceProvider(session.ProviderName, session.Provider)
	if session.NoInteractive || session.JSONOutput || session.Surface == nil {
		return
	}

	refresher := newChatAccountBalanceRefresher(
		session,
		config.EffectiveAICLIBalanceRefreshInterval(session.Config),
		nil,
	)
	refresher.setTarget(session.ProviderName, session.Provider, false)

	session.accountBalanceMu.Lock()
	session.accountBalanceRefresher = refresher
	session.accountBalanceMu.Unlock()
	go refresher.run()
}

func stopChatAccountBalanceRefresh(session *ChatSession) {
	if session == nil {
		return
	}
	session.accountBalanceMu.Lock()
	refresher := session.accountBalanceRefresher
	session.accountBalanceRefresher = nil
	session.accountBalanceMu.Unlock()
	if refresher != nil {
		refresher.Stop()
	}
}

func updateChatAccountBalanceProvider(session *ChatSession, providerName string, provider config.Provider) {
	if session == nil {
		return
	}
	if currentName, currentProvider, ok := session.accountBalanceSnapshot(); ok &&
		strings.EqualFold(currentName, providerName) && currentProvider.Account != nil {
		// A /model change within the same provider must not roll a newer live
		// balance back to the older config snapshot while the next refresh runs.
		provider.Account = currentProvider.Account
		if strings.TrimSpace(provider.SiteType) == "" {
			provider.SiteType = currentProvider.SiteType
		}
		if strings.TrimSpace(provider.SiteTypeConfidence) == "" {
			provider.SiteTypeConfidence = currentProvider.SiteTypeConfidence
		}
	}
	session.setAccountBalanceProvider(providerName, provider)
	if session.Interaction != nil {
		session.Interaction.RefreshAccountBalanceStatus()
	}
	session.accountBalanceMu.RLock()
	refresher := session.accountBalanceRefresher
	session.accountBalanceMu.RUnlock()
	if refresher != nil {
		refresher.setTarget(providerName, provider, true)
	}
}

func (s *ChatSession) setAccountBalanceProvider(providerName string, provider config.Provider) {
	if s == nil {
		return
	}
	provider.Account = cloneProviderAccountSnapshot(provider.Account)
	s.accountBalanceMu.Lock()
	s.accountBalanceProviderName = strings.TrimSpace(providerName)
	s.accountBalanceProvider = provider
	s.accountBalanceInitialized = true
	s.accountBalanceMu.Unlock()
}

func (s *ChatSession) accountBalanceSnapshot() (string, config.Provider, bool) {
	if s == nil {
		return "", config.Provider{}, false
	}
	s.accountBalanceMu.RLock()
	defer s.accountBalanceMu.RUnlock()
	if !s.accountBalanceInitialized {
		return "", config.Provider{}, false
	}
	provider := s.accountBalanceProvider
	provider.Account = cloneProviderAccountSnapshot(provider.Account)
	return s.accountBalanceProviderName, provider, true
}

func currentChatAccountBalance(session *ChatSession) (*config.ProviderAccountSnapshot, string, string) {
	if session == nil {
		return nil, "", ""
	}
	if _, provider, ok := session.accountBalanceSnapshot(); ok {
		return provider.Account, strings.TrimSpace(provider.SiteType), strings.TrimSpace(provider.SiteTypeConfidence)
	}

	// Unit/headless callers may construct ChatSession directly without running
	// the interactive bootstrap. Preserve the existing session/config fallback.
	account := cloneProviderAccountSnapshot(session.Provider.Account)
	siteType := strings.TrimSpace(session.Provider.SiteType)
	confidence := strings.TrimSpace(session.Provider.SiteTypeConfidence)
	if account == nil && session.Config != nil && session.Config.Providers.Items != nil {
		name := strings.TrimSpace(session.ProviderName)
		if provider, ok := session.Config.Providers.Items[name]; ok {
			account = cloneProviderAccountSnapshot(provider.Account)
			if siteType == "" {
				siteType = strings.TrimSpace(provider.SiteType)
			}
			if confidence == "" {
				confidence = strings.TrimSpace(provider.SiteTypeConfidence)
			}
		}
	}
	return account, siteType, confidence
}

func (s *ChatSession) applyAccountBalanceRefresh(
	providerName string,
	provider config.Provider,
	generation uint64,
	refresher *chatAccountBalanceRefresher,
) bool {
	if s == nil || refresher == nil {
		return false
	}
	if !refresher.targetIsCurrent(providerName, generation) {
		return false
	}

	provider.Account = cloneProviderAccountSnapshot(provider.Account)
	s.accountBalanceMu.Lock()
	if s.accountBalanceRefresher != refresher ||
		!strings.EqualFold(s.accountBalanceProviderName, providerName) {
		s.accountBalanceMu.Unlock()
		return false
	}
	s.accountBalanceProvider = provider
	s.accountBalanceInitialized = true
	s.accountBalanceMu.Unlock()

	if s.Interaction != nil {
		s.Interaction.RefreshAccountBalanceStatus()
	}
	return true
}

func (r *chatAccountBalanceRefresher) setTarget(providerName string, provider config.Provider, wake bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.providerName = strings.TrimSpace(providerName)
	r.provider = provider
	r.provider.Account = cloneProviderAccountSnapshot(provider.Account)
	r.generation++
	r.mu.Unlock()
	if wake {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

func (r *chatAccountBalanceRefresher) target() (string, config.Provider, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	provider := r.provider
	provider.Account = cloneProviderAccountSnapshot(provider.Account)
	return r.providerName, provider, r.generation
}

func (r *chatAccountBalanceRefresher) targetIsCurrent(providerName string, generation uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return generation == r.generation && strings.EqualFold(providerName, r.providerName)
}

func (r *chatAccountBalanceRefresher) run() {
	defer close(r.done)
	r.refreshOnce()

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.refreshOnce()
		case <-r.wake:
			r.refreshOnce()
		}
	}
}

func (r *chatAccountBalanceRefresher) refreshOnce() {
	if r == nil || r.session == nil {
		return
	}
	providerName, provider, generation := r.target()
	if !prepareProviderForPeriodicBalanceRefresh(&provider) {
		return
	}

	outcome, err := r.refresh(
		r.ctx,
		r.client,
		providerName,
		&provider,
		chatAccountBalanceRefreshTimeout,
	)
	if err != nil || outcome.Account == nil {
		// Keep the last successful/cached value on transient refresh failures.
		return
	}
	provider.Account = cloneProviderAccountSnapshot(outcome.Account)
	if outcome.SiteType != "" {
		provider.SiteType = outcome.SiteType
	}
	if outcome.SiteTypeConfidence != "" {
		provider.SiteTypeConfidence = outcome.SiteTypeConfidence
	}
	if outcome.SiteTypeDetectedAt != "" {
		provider.SiteTypeDetectedAt = outcome.SiteTypeDetectedAt
	}
	if outcome.AccountAuthRef != "" {
		provider.AccountAuthRef = outcome.AccountAuthRef
	}
	r.session.applyAccountBalanceRefresh(providerName, provider, generation, r)
}

func prepareProviderForPeriodicBalanceRefresh(provider *config.Provider) bool {
	if provider == nil {
		return false
	}
	siteType := siteaccount.NormalizeSiteType(provider.SiteType)
	if (siteType == "" || siteType == siteaccount.SiteTypeUnknown) && provider.Account != nil {
		siteType = siteaccount.NormalizeSiteType(provider.Account.Source)
		if siteType == siteaccount.SiteTypeSub2API || siteType == siteaccount.SiteTypeNewAPI {
			provider.SiteType = string(siteType)
		}
	}
	return siteType == siteaccount.SiteTypeSub2API || siteType == siteaccount.SiteTypeNewAPI
}

func (r *chatAccountBalanceRefresher) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		r.cancel()
		<-r.done
	})
}
