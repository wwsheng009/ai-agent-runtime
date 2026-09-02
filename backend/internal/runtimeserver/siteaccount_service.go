package runtimeserver

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
	"github.com/wwsheng009/ai-agent-runtime/internal/siteaccount"
)

// LocalSiteAccountService implements runtime Web/CLI-shared site detect + account sync.
type LocalSiteAccountService struct {
	configPath      string
	authStorePath   string
	client          *siteaccount.Client
	loadConfig      func(path string) (*agentconfig.Config, error)
	now             func() time.Time
	providerReloader func(cfg *agentconfig.Config) error
}

// NewLocalSiteAccountService builds a service bound to the runtime config + auth store.
func NewLocalSiteAccountService(configPath, authStorePath string) *LocalSiteAccountService {
	configPath = strings.TrimSpace(configPath)
	authStorePath = strings.TrimSpace(authStorePath)
	if authStorePath == "" {
		authStorePath = agentconfig.DefaultAuthStorePath()
	}
	return &LocalSiteAccountService{
		configPath:    resolveAbsolutePath(configPath),
		authStorePath: resolveAbsolutePath(authStorePath),
		client:        siteaccount.NewClient(nil),
		loadConfig: func(path string) (*agentconfig.Config, error) {
			cfg, _, err := LoadRuntimeAgentConfig(path)
			return cfg, err
		},
		now: time.Now,
	}
}

// SetClient overrides the shared siteaccount client (tests).
func (s *LocalSiteAccountService) SetClient(client *siteaccount.Client) {
	if s == nil {
		return
	}
	if client == nil {
		client = siteaccount.NewClient(nil)
	}
	s.client = client
}

// SetProviderReloader installs a callback that atomically refreshes the live
// in-memory provider registry after provider config changes are persisted.
// This makes api_key / provider edits take effect immediately without a restart.
func (s *LocalSiteAccountService) SetProviderReloader(reloader func(cfg *agentconfig.Config) error) {
	if s == nil {
		return
	}
	s.providerReloader = reloader
}

// reloadRuntimeProviders reloads the latest config from disk and refreshes the
// in-memory provider registry. Errors are surfaced as warnings on the result
// instead of failing the underlying account refresh operation.
func (s *LocalSiteAccountService) reloadRuntimeProviders(out *skillsapi.SiteAccountRefreshResult) error {
	if s == nil || s.providerReloader == nil {
		return nil
	}
	cfg, err := s.loadConfigOrDefault(s.configPath)
	if err != nil {
		return fmt.Errorf("reload runtime config: %w", err)
	}
	if err := s.providerReloader(cfg); err != nil {
		return fmt.Errorf("refresh runtime provider registry: %w", err)
	}
	return nil
}

// Detect probes base_url and returns a normalized DetectResult.
func (s *LocalSiteAccountService) Detect(
	ctx context.Context,
	req skillsapi.SiteAccountDetectRequest,
) (*skillsapi.SiteAccountDetectResult, error) {
	if s == nil {
		return nil, fmt.Errorf("siteaccount service is not configured")
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("base_url is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := s.clientOrDefault().DetectSiteType(ctx, siteaccount.DetectInput{
		BaseURL: baseURL,
		Timeout: timeoutFromMillis(req.TimeoutMs),
	})
	if err != nil {
		return nil, err
	}
	return &skillsapi.SiteAccountDetectResult{Detect: cloneDetectResult(&result)}, nil
}

// Fetch runs optional detect + account snapshot for an ad-hoc base_url (no provider persistence).
func (s *LocalSiteAccountService) Fetch(
	ctx context.Context,
	req skillsapi.SiteAccountFetchRequest,
) (*skillsapi.SiteAccountFetchResult, error) {
	if s == nil {
		return nil, fmt.Errorf("siteaccount service is not configured")
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("base_url is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	out := &skillsapi.SiteAccountFetchResult{}
	siteType, autoDetect, err := siteaccount.ParseSiteTypeFlag(req.SiteType)
	if err != nil {
		return nil, err
	}

	confidence := siteaccount.ConfidenceLow
	if autoDetect {
		detectResult, detectErr := s.clientOrDefault().DetectSiteType(ctx, siteaccount.DetectInput{
			BaseURL: baseURL,
			Timeout: timeoutFromMillis(req.TimeoutMs),
		})
		if detectErr != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("site detect failed: %v", detectErr))
			siteType = siteaccount.SiteTypeUnknown
		} else {
			siteType = detectResult.SiteType
			confidence = detectResult.Confidence
			out.Detect = cloneDetectResult(&detectResult)
			out.Warnings = append(out.Warnings, detectResult.Warnings...)
		}
	} else {
		confidence = siteaccount.ConfidenceHigh
		out.Detect = &siteaccount.DetectResult{
			SiteType:   siteType,
			Confidence: confidence,
			DetectedAt: s.nowOrDefault().UTC(),
		}
	}
	if siteType == "" {
		siteType = siteaccount.SiteTypeUnknown
	}

	cred, credWarnings, credErr := resolveFetchCredential(req, siteType)
	out.Warnings = append(out.Warnings, credWarnings...)
	if credErr != nil {
		return out, credErr
	}
	if siteType == siteaccount.SiteTypeUnknown {
		out.Warnings = append(out.Warnings, "account fetch skipped: site type is unknown")
		return out, nil
	}

	snapshot, fetchErr := s.clientOrDefault().FetchAccountSnapshot(ctx, siteaccount.FetchInput{
		BaseURL:    baseURL,
		SiteType:   siteType,
		Credential: cred,
		Timeout:    timeoutFromMillis(req.TimeoutMs),
		Days:       req.Days,
	})
	if fetchErr != nil {
		return out, fetchErr
	}
	view := siteaccount.NormalizeAccountView(snapshot, confidence)
	out.Account = snapshot
	out.AccountView = &view
	out.BalanceLine = siteaccount.FormatBalanceLine(view)
	return out, nil
}

// RefreshProvider detects/fetches using a saved provider and optionally persists the non-sensitive cache.
func (s *LocalSiteAccountService) RefreshProvider(
	ctx context.Context,
	providerName string,
	req skillsapi.SiteAccountRefreshRequest,
) (*skillsapi.SiteAccountRefreshResult, error) {
	if s == nil {
		return nil, fmt.Errorf("siteaccount service is not configured")
	}
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return nil, fmt.Errorf("provider name is required")
	}
	if strings.TrimSpace(s.configPath) == "" {
		return nil, fmt.Errorf("config path is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	cfg, err := s.loadConfigOrDefault(s.configPath)
	if err != nil {
		return nil, err
	}
	if cfg == nil || cfg.Providers.Items == nil {
		return nil, fmt.Errorf("provider %q not found", providerName)
	}
	provider, ok := cfg.Providers.Items[providerName]
	if !ok {
		return nil, fmt.Errorf("provider %q not found", providerName)
	}

	out := &skillsapi.SiteAccountRefreshResult{
		Provider: providerName,
	}
	persist := true
	if req.Persist != nil {
		persist = *req.Persist
	}
	saveAccountAuth := true
	if req.SaveAccountAuth != nil {
		saveAccountAuth = *req.SaveAccountAuth
	}

	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("provider %q has empty base_url", providerName)
	}

	siteType, autoDetect, err := siteaccount.ParseSiteTypeFlag(req.SiteType)
	if err != nil {
		return nil, err
	}
	confidence := siteaccount.ConfidenceLow
	if req.SkipDetect {
		if autoDetect {
			siteType = siteaccount.NormalizeSiteType(provider.SiteType)
			confidence = normalizeSiteConfidence(provider.SiteTypeConfidence)
			if confidence == "" {
				confidence = siteaccount.ConfidenceLow
			}
			out.SiteTypeScores = cloneStringIntMap(provider.SiteTypeScores)
			out.SiteTypeDetectedAt = strings.TrimSpace(provider.SiteTypeDetectedAt)
		} else {
			confidence = siteaccount.ConfidenceHigh
			out.SiteTypeDetectedAt = s.nowOrDefault().UTC().Format(time.RFC3339)
		}
	} else if autoDetect {
		detectResult, detectErr := s.clientOrDefault().DetectSiteType(ctx, siteaccount.DetectInput{
			BaseURL: baseURL,
			Timeout: timeoutFromMillis(req.TimeoutMs),
		})
		if detectErr != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("site detect failed: %v", detectErr))
			// Fall back to configured type when detect fails.
			if configured := siteaccount.NormalizeSiteType(provider.SiteType); configured != "" && configured != siteaccount.SiteTypeUnknown {
				siteType = configured
				confidence = normalizeSiteConfidence(provider.SiteTypeConfidence)
				if confidence == "" {
					confidence = siteaccount.ConfidenceLow
				}
				out.SiteTypeScores = cloneStringIntMap(provider.SiteTypeScores)
				out.SiteTypeDetectedAt = strings.TrimSpace(provider.SiteTypeDetectedAt)
			} else {
				siteType = siteaccount.SiteTypeUnknown
			}
		} else {
			siteType = detectResult.SiteType
			confidence = detectResult.Confidence
			out.Detect = cloneDetectResult(&detectResult)
			out.SiteTypeScores = cloneStringIntMap(detectResult.Score)
			if !detectResult.DetectedAt.IsZero() {
				out.SiteTypeDetectedAt = detectResult.DetectedAt.UTC().Format(time.RFC3339)
			}
			out.Warnings = append(out.Warnings, detectResult.Warnings...)
		}
	} else {
		confidence = siteaccount.ConfidenceHigh
		out.SiteTypeDetectedAt = s.nowOrDefault().UTC().Format(time.RFC3339)
	}
	if siteType == "" {
		siteType = siteaccount.SiteTypeUnknown
	}
	if confidence == "" {
		confidence = siteaccount.ConfidenceLow
	}
	out.SiteType = string(siteType)
	out.SiteTypeConfidence = string(confidence)
	if out.SiteTypeDetectedAt == "" && siteType != siteaccount.SiteTypeUnknown {
		out.SiteTypeDetectedAt = s.nowOrDefault().UTC().Format(time.RFC3339)
	}

	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(provider.GetAPIKey())
	}

	cred, authRef, authRecord, credWarnings, credErr := resolveProviderRefreshCredential(
		req,
		providerName,
		&provider,
		siteType,
		apiKey,
		s.authStorePath,
	)
	out.Warnings = append(out.Warnings, credWarnings...)
	out.AccountAuthRef = authRef
	if credErr != nil {
		if persist {
			_ = s.persistSiteTypeOnly(providerName, out)
		}
		return out, credErr
	}

	if siteType == siteaccount.SiteTypeUnknown {
		out.Warnings = append(out.Warnings, "account fetch skipped: site type is unknown")
		if persist {
			if err := s.persistSiteTypeOnly(providerName, out); err != nil {
				return out, err
			}
			out.Persisted = true
		}
		return out, nil
	}

	snapshot, fetchErr := s.clientOrDefault().FetchAccountSnapshot(ctx, siteaccount.FetchInput{
		BaseURL:    baseURL,
		SiteType:   siteType,
		Credential: cred,
		Timeout:    timeoutFromMillis(req.TimeoutMs),
		Days:       req.Days,
	})
	if fetchErr != nil {
		if persist {
			_ = s.persistSiteTypeOnly(providerName, out)
		}
		return out, fetchErr
	}

	view := siteaccount.NormalizeAccountView(snapshot, confidence)
	out.Account = snapshot
	out.AccountView = &view
	out.BalanceLine = siteaccount.FormatBalanceLine(view)
	out.AccountCache = providerAccountSnapshotFromSiteAccount(snapshot)

	if saveAccountAuth && authRecord != nil && strings.TrimSpace(authRef) != "" {
		if err := agentconfig.SaveProviderAuthToPath(s.authStorePath, authRef, *authRecord); err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("save account auth failed: %v", err))
		} else {
			out.AccountAuthRef = authRef
			if !persist {
				appendProviderReloadWarning(out, s.reloadRuntimeProviders(out))
			}
		}
	}

	if persist {
		update := agentconfig.ProviderConfigUpdate{
			Name:               providerName,
			SiteType:           stringPtr(out.SiteType),
			SiteTypeConfidence: stringPtr(out.SiteTypeConfidence),
			SiteTypeDetectedAt: stringPtr(out.SiteTypeDetectedAt),
			Account:            out.AccountCache,
		}
		if len(out.SiteTypeScores) > 0 {
			scores := cloneStringIntMap(out.SiteTypeScores)
			update.SiteTypeScores = &scores
		}
		if strings.TrimSpace(out.AccountAuthRef) != "" {
			update.AccountAuthRef = stringPtr(out.AccountAuthRef)
		}
		if _, err := agentconfig.UpdateProviderConfig(s.configPath, update); err != nil {
			return out, fmt.Errorf("persist provider account: %w", err)
		}
		out.Persisted = true
		appendProviderReloadWarning(out, s.reloadRuntimeProviders(out))
	}
	return out, nil
}

func (s *LocalSiteAccountService) persistSiteTypeOnly(providerName string, out *skillsapi.SiteAccountRefreshResult) error {
	if out == nil || strings.TrimSpace(out.SiteType) == "" {
		return nil
	}
	update := agentconfig.ProviderConfigUpdate{
		Name:               providerName,
		SiteType:           stringPtr(out.SiteType),
		SiteTypeConfidence: stringPtr(out.SiteTypeConfidence),
		SiteTypeDetectedAt: stringPtr(out.SiteTypeDetectedAt),
	}
	if len(out.SiteTypeScores) > 0 {
		scores := cloneStringIntMap(out.SiteTypeScores)
		update.SiteTypeScores = &scores
	}
	if strings.TrimSpace(out.AccountAuthRef) != "" {
		update.AccountAuthRef = stringPtr(out.AccountAuthRef)
	}
	if _, err := agentconfig.UpdateProviderConfig(s.configPath, update); err != nil {
		return err
	}
	appendProviderReloadWarning(out, s.reloadRuntimeProviders(out))
	return nil
}

func appendProviderReloadWarning(out *skillsapi.SiteAccountRefreshResult, err error) {
	if err == nil {
		return
	}
	if out == nil {
		return
	}
	out.Warnings = append(out.Warnings, fmt.Sprintf("provider 配置已保存，但刷新运行中 provider 注册表失败: %v", err))
}

func (s *LocalSiteAccountService) clientOrDefault() *siteaccount.Client {
	if s != nil && s.client != nil {
		return s.client
	}
	return siteaccount.NewClient(nil)
}

func (s *LocalSiteAccountService) loadConfigOrDefault(path string) (*agentconfig.Config, error) {
	if s != nil && s.loadConfig != nil {
		return s.loadConfig(path)
	}
	cfg, _, err := LoadRuntimeAgentConfig(path)
	return cfg, err
}

func (s *LocalSiteAccountService) nowOrDefault() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func resolveFetchCredential(
	req skillsapi.SiteAccountFetchRequest,
	siteType siteaccount.SiteType,
) (siteaccount.AccountCredential, []string, error) {
	var warnings []string
	switch siteType {
	case siteaccount.SiteTypeSub2API, siteaccount.SiteTypeDeepSeek:
		apiKey := strings.TrimSpace(req.APIKey)
		if apiKey == "" {
			return siteaccount.AccountCredential{}, warnings, fmt.Errorf("api_key is required for %s account fetch", siteType)
		}
		return siteaccount.AccountCredential{APIKey: apiKey}, warnings, nil
	case siteaccount.SiteTypeNewAPI:
		token := strings.TrimSpace(req.SystemAccessToken)
		userID, err := parseSubjectUserID(req.SubjectUserID)
		if err != nil {
			return siteaccount.AccountCredential{}, warnings, err
		}
		if token == "" || userID <= 0 {
			return siteaccount.AccountCredential{}, warnings, fmt.Errorf("system_access_token and subject_user_id are required for new-api account fetch")
		}
		return siteaccount.AccountCredential{
			SystemAccessToken: token,
			SubjectUserID:     userID,
		}, warnings, nil
	default:
		return siteaccount.AccountCredential{}, warnings, nil
	}
}

func normalizeSiteConfidence(raw string) siteaccount.Confidence {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(siteaccount.ConfidenceHigh):
		return siteaccount.ConfidenceHigh
	case string(siteaccount.ConfidenceMedium):
		return siteaccount.ConfidenceMedium
	case string(siteaccount.ConfidenceLow):
		return siteaccount.ConfidenceLow
	default:
		return ""
	}
}

func resolveProviderRefreshCredential(
	req skillsapi.SiteAccountRefreshRequest,
	providerName string,
	provider *agentconfig.Provider,
	siteType siteaccount.SiteType,
	apiKey string,
	authStorePath string,
) (siteaccount.AccountCredential, string, *agentconfig.ProviderAuthRecord, []string, error) {
	var warnings []string
	authRef := ""
	if provider != nil {
		authRef = strings.TrimSpace(provider.AccountAuthRef)
	}

	switch siteType {
	case siteaccount.SiteTypeSub2API, siteaccount.SiteTypeDeepSeek:
		if strings.TrimSpace(apiKey) == "" {
			return siteaccount.AccountCredential{}, authRef, nil, warnings, fmt.Errorf("api key is missing for provider %q", providerName)
		}
		return siteaccount.AccountCredential{APIKey: apiKey}, authRef, nil, warnings, nil
	case siteaccount.SiteTypeNewAPI:
		token := strings.TrimSpace(req.SystemAccessToken)
		userIDText := strings.TrimSpace(string(req.SubjectUserID))
		if (token == "" || userIDText == "") && authRef != "" && strings.TrimSpace(authStorePath) != "" {
			if record, loadErr := agentconfig.LoadProviderAuthFromPath(authStorePath, authRef); loadErr != nil {
				warnings = append(warnings, fmt.Sprintf("load account auth ref %q failed: %v", authRef, loadErr))
			} else if record != nil {
				if token == "" {
					token = strings.TrimSpace(record.AccessToken)
				}
				if userIDText == "" {
					userIDText = strings.TrimSpace(record.SubjectUserID)
				}
			}
		}
		if token == "" || userIDText == "" {
			return siteaccount.AccountCredential{}, authRef, nil, warnings, fmt.Errorf("new-api system access token or user id is missing for provider %q", providerName)
		}
		userID, err := parseSubjectUserID(skillsapi.FlexibleString(userIDText))
		if err != nil {
			return siteaccount.AccountCredential{}, authRef, nil, warnings, err
		}
		if authRef == "" {
			authRef = providerName + "-account"
		}
		record := &agentconfig.ProviderAuthRecord{
			KeyType:       agentconfig.AuthKeyTypeNewAPISystemAccessToken,
			AuthMode:      agentconfig.AuthKeyTypeNewAPISystemAccessToken,
			AccessToken:   token,
			SubjectUserID: strconv.FormatInt(userID, 10),
		}
		return siteaccount.AccountCredential{
			SystemAccessToken: token,
			SubjectUserID:     userID,
		}, authRef, record, warnings, nil
	default:
		return siteaccount.AccountCredential{}, authRef, nil, warnings, nil
	}
}

func providerAccountSnapshotFromSiteAccount(snapshot *siteaccount.AccountSnapshot) *agentconfig.ProviderAccountSnapshot {
	if snapshot == nil {
		return nil
	}
	out := &agentconfig.ProviderAccountSnapshot{
		Source:            snapshot.Source,
		Mode:              snapshot.Mode,
		Currency:          firstNonEmptySiteText(snapshot.Currency, snapshot.QuotaDisplayUnit),
		WalletBalance:     siteaccount.CloneFloat64(snapshot.WalletBalance),
		IsAvailable:       siteaccount.CloneBool(snapshot.IsAvailable),
		QuotaBalance:      siteaccount.CloneFloat64(snapshot.QuotaBalance),
		QuotaRemaining:    siteaccount.CloneFloat64(snapshot.QuotaRemaining),
		QuotaUsed:         siteaccount.CloneFloat64(snapshot.UsedQuota),
		QuotaLimit:        siteaccount.CloneFloat64(snapshot.QuotaLimit),
		QuotaDisplayType:  snapshot.QuotaDisplayType,
		QuotaDisplayUnit:  firstNonEmptySiteText(snapshot.QuotaDisplayUnit, snapshot.Currency),
		QuotaDisplayScale: siteaccount.CloneFloat64(snapshot.QuotaDisplayScale),
		PlanName:          snapshot.PlanName,
		Partial:           snapshot.Partial,
	}
	if len(snapshot.BalanceDetails) > 0 {
		out.BalanceDetails = make([]agentconfig.ProviderBalanceDetail, 0, len(snapshot.BalanceDetails))
		for _, detail := range snapshot.BalanceDetails {
			out.BalanceDetails = append(out.BalanceDetails, agentconfig.ProviderBalanceDetail{
				Currency:        detail.Currency,
				TotalBalance:    detail.TotalBalance,
				GrantedBalance:  detail.GrantedBalance,
				ToppedUpBalance: detail.ToppedUpBalance,
			})
		}
	}
	if snapshot.ExternalUser != nil {
		out.ExternalUserID = strings.TrimSpace(snapshot.ExternalUser.ID)
		out.ExternalUsernameMasked = maskUsername(snapshot.ExternalUser.Username)
	}
	if !snapshot.FetchedAt.IsZero() {
		out.FetchedAt = snapshot.FetchedAt.UTC().Format(time.RFC3339)
	}
	if len(snapshot.Errors) > 0 {
		out.LastError = strings.Join(snapshot.Errors, "; ")
	}
	if len(snapshot.Subscriptions) > 0 {
		out.Subscriptions = make([]agentconfig.ProviderAccountSubscription, 0, len(snapshot.Subscriptions))
		for _, sub := range snapshot.Subscriptions {
			item := agentconfig.ProviderAccountSubscription{
				Name:      sub.Name,
				Status:    sub.Status,
				Remaining: siteaccount.CloneFloat64(sub.Remaining),
			}
			if sub.PeriodEnd != nil && !sub.PeriodEnd.IsZero() {
				item.PeriodEnd = sub.PeriodEnd.UTC().Format(time.RFC3339)
			}
			out.Subscriptions = append(out.Subscriptions, item)
		}
	}
	if snapshot.Usage != nil {
		out.Usage = &agentconfig.ProviderAccountUsage{
			TotalRequests: cloneInt64Ptr(snapshot.Usage.TotalRequests),
			TotalCost:     siteaccount.CloneFloat64(snapshot.Usage.TotalCost),
			TodayRequests: cloneInt64Ptr(snapshot.Usage.TodayRequests),
			TodayCost:     siteaccount.CloneFloat64(snapshot.Usage.TodayCost),
		}
	}
	return out
}

func cloneDetectResult(in *siteaccount.DetectResult) *siteaccount.DetectResult {
	if in == nil {
		return nil
	}
	out := *in
	if len(in.Score) > 0 {
		out.Score = cloneStringIntMap(in.Score)
	}
	if len(in.Hits) > 0 {
		out.Hits = append([]siteaccount.EndpointHit(nil), in.Hits...)
	}
	if len(in.Warnings) > 0 {
		out.Warnings = append([]string(nil), in.Warnings...)
	}
	if len(in.PlatformHints) > 0 {
		out.PlatformHints = make(map[string]any, len(in.PlatformHints))
		for key, value := range in.PlatformHints {
			out.PlatformHints[key] = value
		}
	}
	return &out
}

func cloneStringIntMap(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneInt64Ptr(v *int64) *int64 {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}

func timeoutFromMillis(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func parseSubjectUserID(raw skillsapi.FlexibleString) (int64, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid subject_user_id %q", text)
	}
	return parsed, nil
}

func stringPtr(v string) *string {
	v = strings.TrimSpace(v)
	return &v
}

func firstNonEmptySiteText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func maskUsername(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= 2 {
		return string(runes[0]) + "*"
	}
	if len(runes) <= 4 {
		return string(runes[0]) + strings.Repeat("*", len(runes)-2) + string(runes[len(runes)-1])
	}
	return string(runes[:2]) + strings.Repeat("*", len(runes)-4) + string(runes[len(runes)-2:])
}
