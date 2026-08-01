package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/siteaccount"
)

type providerLoginSiteAccountOutcome struct {
	SiteType           string
	SiteTypeConfidence string
	SiteTypeDetectedAt string
	SiteTypeScores     map[string]int
	Account            *config.ProviderAccountSnapshot
	AccountView        *siteaccount.AccountView
	AccountAuthRef     string
	AccountAuthRecord  *config.ProviderAuthRecord
	BalanceLine        string
	Warnings           []string
	SkippedDetect      bool
	SkippedAccount     bool
}

func enrichProviderLoginSiteAccount(
	ctx context.Context,
	req providerLoginRequest,
	providerName string,
	candidate *config.Provider,
	apiKey string,
	authMode string,
) (providerLoginSiteAccountOutcome, error) {
	out := providerLoginSiteAccountOutcome{}
	if candidate == nil {
		return out, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(providerName) == "" {
		providerName = strings.TrimSpace(req.ProviderName)
	}

	forcedType, autoDetect, err := siteaccount.ParseSiteTypeFlag(req.SiteType)
	if err != nil {
		return out, err
	}

	client := siteaccount.NewClient(nil)
	siteType := forcedType
	confidence := siteaccount.ConfidenceLow
	if req.SkipSiteDetect {
		out.SkippedDetect = true
		if autoDetect {
			// Keep any existing configured type when detect is skipped and no force flag.
			siteType = siteaccount.NormalizeSiteType(candidate.SiteType)
		}
	} else if autoDetect {
		detectTimeout := req.Timeout
		if detectTimeout <= 0 {
			detectTimeout = 8 * time.Second
		}
		detectResult, detectErr := client.DetectSiteType(ctx, siteaccount.DetectInput{
			BaseURL: candidate.BaseURL,
			Timeout: detectTimeout,
		})
		if detectErr != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("site detect failed: %v", detectErr))
			siteType = siteaccount.SiteTypeUnknown
		} else {
			siteType = detectResult.SiteType
			confidence = detectResult.Confidence
			out.SiteTypeScores = cloneStringIntMap(detectResult.Score)
			if !detectResult.DetectedAt.IsZero() {
				out.SiteTypeDetectedAt = detectResult.DetectedAt.UTC().Format(time.RFC3339)
			}
			out.Warnings = append(out.Warnings, detectResult.Warnings...)
		}
	} else {
		confidence = siteaccount.ConfidenceHigh
		out.SiteTypeDetectedAt = time.Now().UTC().Format(time.RFC3339)
	}

	if siteType == "" {
		siteType = siteaccount.SiteTypeUnknown
	}
	out.SiteType = string(siteType)
	if confidence == "" {
		confidence = siteaccount.ConfidenceLow
	}
	out.SiteTypeConfidence = string(confidence)
	if out.SiteTypeDetectedAt == "" && out.SiteType != "" && out.SiteType != string(siteaccount.SiteTypeUnknown) {
		out.SiteTypeDetectedAt = time.Now().UTC().Format(time.RFC3339)
	}

	candidate.SiteType = out.SiteType
	candidate.SiteTypeConfidence = out.SiteTypeConfidence
	candidate.SiteTypeDetectedAt = out.SiteTypeDetectedAt
	if len(out.SiteTypeScores) > 0 {
		candidate.SiteTypeScores = cloneStringIntMap(out.SiteTypeScores)
	}

	if req.SkipAccount {
		out.SkippedAccount = true
		return out, nil
	}
	if authMode != providerAuthModeAPIKey {
		out.SkippedAccount = true
		out.Warnings = append(out.Warnings, "account sync skipped: only api_key auth is supported for account snapshot")
		return out, nil
	}

	switch siteType {
	case siteaccount.SiteTypeSub2API, siteaccount.SiteTypeDeepSeek:
		return enrichAPIKeyAccount(ctx, req, candidate, client, siteType, apiKey, confidence, out)
	case siteaccount.SiteTypeNewAPI:
		return enrichNewAPIAccount(ctx, req, providerName, candidate, client, confidence, out)
	default:
		out.SkippedAccount = true
		if req.RequireAccount {
			return out, fmt.Errorf("account sync required but site type is %q", siteType)
		}
		return out, nil
	}
}

func enrichAPIKeyAccount(
	ctx context.Context,
	req providerLoginRequest,
	candidate *config.Provider,
	client *siteaccount.Client,
	siteType siteaccount.SiteType,
	apiKey string,
	confidence siteaccount.Confidence,
	out providerLoginSiteAccountOutcome,
) (providerLoginSiteAccountOutcome, error) {
	if strings.TrimSpace(apiKey) == "" {
		out.SkippedAccount = true
		out.Warnings = append(out.Warnings, "account sync skipped: api key missing")
		if req.RequireAccount {
			return out, fmt.Errorf("account sync required but api key is missing")
		}
		return out, nil
	}

	snapshot, fetchErr := client.FetchAccountSnapshot(ctx, siteaccount.FetchInput{
		BaseURL:  candidate.BaseURL,
		SiteType: siteType,
		Credential: siteaccount.AccountCredential{
			APIKey: apiKey,
		},
		Timeout: req.Timeout,
	})
	if fetchErr != nil {
		out.Warnings = append(out.Warnings, fmt.Sprintf("account sync failed: %v", fetchErr))
		if req.RequireAccount {
			return out, fmt.Errorf("account sync required: %w", fetchErr)
		}
		return out, nil
	}

	view := siteaccount.NormalizeAccountView(snapshot, confidence)
	out.AccountView = &view
	out.BalanceLine = siteaccount.FormatBalanceLine(view)
	out.Account = providerAccountSnapshotFromSiteAccount(snapshot)
	candidate.Account = out.Account
	return out, nil
}

func enrichNewAPIAccount(
	ctx context.Context,
	req providerLoginRequest,
	providerName string,
	candidate *config.Provider,
	client *siteaccount.Client,
	confidence siteaccount.Confidence,
	out providerLoginSiteAccountOutcome,
) (providerLoginSiteAccountOutcome, error) {
	token, userID, authRef, warnings, err := resolveNewAPIAccountCredentials(req, providerName, candidate)
	out.Warnings = append(out.Warnings, warnings...)
	if err != nil {
		if req.RequireAccount {
			return out, err
		}
		out.SkippedAccount = true
		out.Warnings = append(out.Warnings, err.Error())
		return out, nil
	}
	if strings.TrimSpace(token) == "" || userID <= 0 {
		out.SkippedAccount = true
		out.Warnings = append(out.Warnings, "account sync skipped: new-api system access token or user id not provided")
		if req.RequireAccount {
			return out, fmt.Errorf("account sync required but new-api system access token/user id is missing")
		}
		return out, nil
	}

	snapshot, fetchErr := client.FetchAccountSnapshot(ctx, siteaccount.FetchInput{
		BaseURL:  candidate.BaseURL,
		SiteType: siteaccount.SiteTypeNewAPI,
		Credential: siteaccount.AccountCredential{
			SystemAccessToken: token,
			SubjectUserID:     userID,
		},
		Timeout: req.Timeout,
	})
	if fetchErr != nil {
		out.Warnings = append(out.Warnings, fmt.Sprintf("account sync failed: %v", fetchErr))
		if req.RequireAccount {
			return out, fmt.Errorf("account sync required: %w", fetchErr)
		}
		return out, nil
	}

	view := siteaccount.NormalizeAccountView(snapshot, confidence)
	out.AccountView = &view
	out.BalanceLine = siteaccount.FormatBalanceLine(view)
	out.Account = providerAccountSnapshotFromSiteAccount(snapshot)
	candidate.Account = out.Account

	if authRef == "" {
		authRef = resolveLoginAccountAuthRef(candidate, providerName)
	}
	out.AccountAuthRef = authRef
	out.AccountAuthRecord = &config.ProviderAuthRecord{
		KeyType:       config.AuthKeyTypeNewAPISystemAccessToken,
		AuthMode:      config.AuthKeyTypeNewAPISystemAccessToken,
		AccessToken:   token,
		SubjectUserID: strconv.FormatInt(userID, 10),
	}
	candidate.AccountAuthRef = authRef
	return out, nil
}

func resolveNewAPIAccountCredentials(
	req providerLoginRequest,
	providerName string,
	candidate *config.Provider,
) (token string, userID int64, authRef string, warnings []string, err error) {
	token = strings.TrimSpace(req.NewAPIAccessToken)
	userIDText := strings.TrimSpace(req.NewAPIUserID)
	if candidate != nil {
		authRef = strings.TrimSpace(candidate.AccountAuthRef)
	}

	authStorePath := strings.TrimSpace(req.AuthStorePath)
	if authStorePath == "" {
		authStorePath = config.DefaultAuthStorePath()
	}

	// Prefer existing account auth store when flags are incomplete.
	if (token == "" || userIDText == "") && authRef != "" && authStorePath != "" {
		if record, loadErr := config.LoadProviderAuthFromPath(authStorePath, authRef); loadErr != nil {
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

	// Optional interactive collection when still incomplete.
	if req.Interactive && req.Prompter != nil {
		if token == "" {
			prompted, promptErr := req.Prompter.PromptSecret("New-API system access token (optional, Enter to skip)", "", false)
			if promptErr != nil {
				return "", 0, authRef, warnings, fmt.Errorf("prompt new-api access token: %w", promptErr)
			}
			token = strings.TrimSpace(prompted)
		}
		if token != "" && userIDText == "" {
			prompted, promptErr := req.Prompter.PromptText("New-API user id", "", true)
			if promptErr != nil {
				return "", 0, authRef, warnings, fmt.Errorf("prompt new-api user id: %w", promptErr)
			}
			userIDText = strings.TrimSpace(prompted)
		}
	}

	if token == "" {
		return "", 0, authRef, warnings, nil
	}
	if userIDText == "" {
		return "", 0, authRef, warnings, nil
	}
	parsed, parseErr := strconv.ParseInt(userIDText, 10, 64)
	if parseErr != nil || parsed <= 0 {
		return "", 0, authRef, warnings, fmt.Errorf("invalid new-api user id %q", userIDText)
	}
	if authRef == "" {
		authRef = resolveLoginAccountAuthRef(candidate, providerName)
	}
	return token, parsed, authRef, warnings, nil
}

func resolveLoginAccountAuthRef(existing *config.Provider, providerName string) string {
	if existing != nil {
		if ref := strings.TrimSpace(existing.AccountAuthRef); ref != "" {
			return ref
		}
	}
	name := strings.TrimSpace(providerName)
	if name == "" {
		return "newapi-account"
	}
	return name + "-account"
}

func providerAccountSnapshotFromSiteAccount(snapshot *siteaccount.AccountSnapshot) *config.ProviderAccountSnapshot {
	if snapshot == nil {
		return nil
	}
	out := &config.ProviderAccountSnapshot{
		Source:            snapshot.Source,
		Mode:              snapshot.Mode,
		Currency:          firstNonEmptyText(snapshot.Currency, snapshot.QuotaDisplayUnit),
		WalletBalance:     siteaccount.CloneFloat64(snapshot.WalletBalance),
		IsAvailable:       siteaccount.CloneBool(snapshot.IsAvailable),
		QuotaBalance:      siteaccount.CloneFloat64(snapshot.QuotaBalance),
		QuotaRemaining:    siteaccount.CloneFloat64(snapshot.QuotaRemaining),
		QuotaUsed:         siteaccount.CloneFloat64(snapshot.UsedQuota),
		QuotaLimit:        siteaccount.CloneFloat64(snapshot.QuotaLimit),
		QuotaDisplayType:  snapshot.QuotaDisplayType,
		QuotaDisplayUnit:  firstNonEmptyText(snapshot.QuotaDisplayUnit, snapshot.Currency),
		QuotaDisplayScale: siteaccount.CloneFloat64(snapshot.QuotaDisplayScale),
		PlanName:          snapshot.PlanName,
		Partial:           snapshot.Partial,
	}
	if len(snapshot.BalanceDetails) > 0 {
		out.BalanceDetails = make([]config.ProviderBalanceDetail, 0, len(snapshot.BalanceDetails))
		for _, detail := range snapshot.BalanceDetails {
			out.BalanceDetails = append(out.BalanceDetails, config.ProviderBalanceDetail{
				Currency:        detail.Currency,
				TotalBalance:    detail.TotalBalance,
				GrantedBalance:  detail.GrantedBalance,
				ToppedUpBalance: detail.ToppedUpBalance,
			})
		}
	}
	if snapshot.ExternalUser != nil {
		out.ExternalUserID = strings.TrimSpace(snapshot.ExternalUser.ID)
		out.ExternalUsernameMasked = maskSecretForDisplay(snapshot.ExternalUser.Username)
	}
	if !snapshot.FetchedAt.IsZero() {
		out.FetchedAt = snapshot.FetchedAt.UTC().Format(time.RFC3339)
	}
	if len(snapshot.Errors) > 0 {
		out.LastError = strings.Join(snapshot.Errors, "; ")
	}
	if len(snapshot.Subscriptions) > 0 {
		out.Subscriptions = make([]config.ProviderAccountSubscription, 0, len(snapshot.Subscriptions))
		for _, sub := range snapshot.Subscriptions {
			item := config.ProviderAccountSubscription{
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
		out.Usage = &config.ProviderAccountUsage{
			TotalRequests: cloneInt64(snapshot.Usage.TotalRequests),
			TotalCost:     siteaccount.CloneFloat64(snapshot.Usage.TotalCost),
			TodayRequests: cloneInt64(snapshot.Usage.TodayRequests),
			TodayCost:     siteaccount.CloneFloat64(snapshot.Usage.TodayCost),
		}
	}
	return out
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

func cloneInt64(v *int64) *int64 {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
