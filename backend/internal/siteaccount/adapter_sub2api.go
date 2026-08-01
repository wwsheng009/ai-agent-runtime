package siteaccount

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	sub2APIUsageSource = "v1_usage"
	sub2APIUsagePath   = "/v1/usage"
)

type sub2APIUsageResponse struct {
	Mode         string             `json:"mode"`
	IsValid      *bool              `json:"isValid"`
	Status       string             `json:"status"`
	PlanName     string             `json:"planName"`
	Unit         string             `json:"unit"`
	Remaining    *float64           `json:"remaining"`
	Balance      *float64           `json:"balance"`
	Quota        *sub2APIQuota      `json:"quota"`
	Subscription json.RawMessage    `json:"subscription"`
	Usage        *sub2APIUsageBlock `json:"usage"`
}

type sub2APIQuota struct {
	Limit     *float64 `json:"limit"`
	Used      *float64 `json:"used"`
	Remaining *float64 `json:"remaining"`
	Unit      string   `json:"unit"`
}

type sub2APIUsageBlock struct {
	Today *sub2APIUsageTotals `json:"today"`
	Total *sub2APIUsageTotals `json:"total"`
}

type sub2APIUsageTotals struct {
	Requests *int64   `json:"requests"`
	Cost     *float64 `json:"cost"`
}

type sub2APISubscription struct {
	DailyUsageUSD   *float64   `json:"daily_usage_usd"`
	WeeklyUsageUSD  *float64   `json:"weekly_usage_usd"`
	MonthlyUsageUSD *float64   `json:"monthly_usage_usd"`
	DailyLimitUSD   *float64   `json:"daily_limit_usd"`
	WeeklyLimitUSD  *float64   `json:"weekly_limit_usd"`
	MonthlyLimitUSD *float64   `json:"monthly_limit_usd"`
	ExpiresAt       *time.Time `json:"expires_at"`
}

// FetchAccountSnapshot loads a normalized account snapshot for the given site type.
func (c *Client) FetchAccountSnapshot(ctx context.Context, input FetchInput) (*AccountSnapshot, error) {
	if c == nil {
		c = NewClient(nil)
	}
	registry := c.Registry
	if registry == nil {
		registry = DefaultAdapterRegistry()
	}
	return registry.Fetch(ctx, c, input)
}

// FetchAccountSnapshot is a package-level convenience using the default client.
func FetchAccountSnapshot(ctx context.Context, input FetchInput) (*AccountSnapshot, error) {
	return NewClient(nil).FetchAccountSnapshot(ctx, input)
}

func (c *Client) fetchSub2APIUsage(ctx context.Context, input FetchInput) (*AccountSnapshot, error) {
	baseURL := strings.TrimSpace(input.BaseURL)
	if baseURL == "" {
		return nil, invalidInput("base_url is required")
	}
	apiKey := strings.TrimSpace(input.Credential.APIKey)
	if apiKey == "" {
		return nil, missingCredential("api key is required for Sub2API /v1/usage")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := resolveTimeout(input.Timeout, defaultHTTPTimeout)
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	days := input.Days
	if days <= 0 {
		days = 7
	}
	endpoint := JoinURL(baseURL, fmt.Sprintf("%s?days=%d", sub2APIUsagePath, days))
	result, err := doGET(reqCtx, c.HTTP, endpoint, "application/json", bearerHeaders(apiKey))
	if err != nil {
		return nil, err
	}
	if result.StatusCode == http.StatusUnauthorized || result.StatusCode == http.StatusForbidden {
		return nil, unauthorized(fmt.Sprintf("Sub2API /v1/usage returned HTTP %d", result.StatusCode))
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return nil, httpError(fmt.Sprintf("Sub2API /v1/usage returned HTTP %d", result.StatusCode), nil)
	}

	var payload sub2APIUsageResponse
	if err := json.Unmarshal(result.Body, &payload); err != nil {
		return nil, unexpectedPayload("decode Sub2API /v1/usage response", err)
	}
	return normalizeSub2APIUsage(payload, time.Now().UTC()), nil
}

func normalizeSub2APIUsage(payload sub2APIUsageResponse, fetchedAt time.Time) *AccountSnapshot {
	snapshot := &AccountSnapshot{
		SiteType:  SiteTypeSub2API,
		Source:    sub2APIUsageSource,
		Mode:      strings.TrimSpace(payload.Mode),
		Currency:  firstNonEmpty(payload.Unit, "USD"),
		PlanName:  strings.TrimSpace(payload.PlanName),
		FetchedAt: fetchedAt,
	}
	if snapshot.Mode == "" {
		snapshot.Mode = "unknown"
	}
	if payload.Quota != nil {
		snapshot.QuotaLimit = CloneFloat64(payload.Quota.Limit)
		snapshot.UsedQuota = CloneFloat64(payload.Quota.Used)
		snapshot.QuotaRemaining = CloneFloat64(payload.Quota.Remaining)
		if unit := strings.TrimSpace(payload.Quota.Unit); unit != "" {
			snapshot.Currency = unit
		}
	}
	if snapshot.QuotaRemaining == nil {
		snapshot.QuotaRemaining = CloneFloat64(payload.Remaining)
	}
	if payload.Balance != nil {
		snapshot.WalletBalance = CloneFloat64(payload.Balance)
		if snapshot.QuotaRemaining == nil {
			snapshot.QuotaRemaining = CloneFloat64(payload.Balance)
		}
	}

	if len(payload.Subscription) > 0 && string(payload.Subscription) != "null" {
		var sub sub2APISubscription
		if err := json.Unmarshal(payload.Subscription, &sub); err == nil {
			summary := SubscriptionSummary{
				Name:         firstNonEmpty(snapshot.PlanName, "subscription"),
				Status:       "active",
				Remaining:    CloneFloat64(payload.Remaining),
				PeriodEnd:    sub.ExpiresAt,
				DailyLimit:   CloneFloat64(sub.DailyLimitUSD),
				WeeklyLimit:  CloneFloat64(sub.WeeklyLimitUSD),
				MonthlyLimit: CloneFloat64(sub.MonthlyLimitUSD),
				DailyUsage:   CloneFloat64(sub.DailyUsageUSD),
				WeeklyUsage:  CloneFloat64(sub.WeeklyUsageUSD),
				MonthlyUsage: CloneFloat64(sub.MonthlyUsageUSD),
			}
			snapshot.Subscriptions = []SubscriptionSummary{summary}
		}
	}

	if payload.Usage != nil {
		usage := &UsageSummary{}
		if payload.Usage.Total != nil {
			usage.TotalRequests = payload.Usage.Total.Requests
			usage.TotalCost = CloneFloat64(payload.Usage.Total.Cost)
		}
		if payload.Usage.Today != nil {
			usage.TodayRequests = payload.Usage.Today.Requests
			usage.TodayCost = CloneFloat64(payload.Usage.Today.Cost)
		}
		if usage.TotalRequests != nil || usage.TotalCost != nil || usage.TodayRequests != nil || usage.TodayCost != nil {
			snapshot.Usage = usage
		}
	}

	if snapshot.QuotaRemaining == nil && snapshot.WalletBalance == nil && snapshot.Usage == nil && len(snapshot.Subscriptions) == 0 {
		snapshot.Partial = true
		snapshot.Errors = append(snapshot.Errors, "Sub2API /v1/usage returned no balance fields")
	}
	return snapshot
}

// NormalizeSiteType canonicalizes user/flag site type values.
func NormalizeSiteType(raw string) SiteType {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(SiteTypeNewAPI), "newapi", "new_api":
		return SiteTypeNewAPI
	case string(SiteTypeSub2API), "sub2", "sub_2_api":
		return SiteTypeSub2API
	case string(SiteTypeDeepSeek), "deep-seek", "deep_seek":
		return SiteTypeDeepSeek
	case string(SiteTypeUnknown), "":
		return SiteTypeUnknown
	default:
		return SiteType(strings.ToLower(strings.TrimSpace(raw)))
	}
}

// ParseSiteTypeFlag parses CLI --site-type values including "auto".
func ParseSiteTypeFlag(raw string) (SiteType, bool, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" || trimmed == "auto" {
		return SiteTypeUnknown, true, nil
	}
	switch NormalizeSiteType(trimmed) {
	case SiteTypeNewAPI:
		return SiteTypeNewAPI, false, nil
	case SiteTypeSub2API:
		return SiteTypeSub2API, false, nil
	case SiteTypeDeepSeek:
		return SiteTypeDeepSeek, false, nil
	case SiteTypeUnknown:
		return SiteTypeUnknown, false, nil
	default:
		return "", false, invalidInput(fmt.Sprintf("unsupported site type %q", raw))
	}
}
