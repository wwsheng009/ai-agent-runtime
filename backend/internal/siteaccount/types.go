// Package siteaccount provides shared site-type detection and account balance
// snapshot capabilities for aicli and runtime-server.
package siteaccount

import "time"

// SiteType identifies the upstream product family behind an OpenAI-compatible base URL.
type SiteType string

const (
	SiteTypeUnknown  SiteType = "unknown"
	SiteTypeNewAPI   SiteType = "new-api"
	SiteTypeSub2API  SiteType = "sub2api"
	SiteTypeDeepSeek SiteType = "deepseek"
)

// Confidence is the detection confidence band.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// DetectInput configures site type detection.
type DetectInput struct {
	BaseURL string
	Timeout time.Duration
}

// EndpointHit records one candidate probe outcome.
type EndpointHit struct {
	Path       string   `json:"path"`
	SiteType   SiteType `json:"site_type"`
	StatusCode int      `json:"status_code"`
	Matched    bool     `json:"matched"`
	Protected  bool     `json:"protected,omitempty"`
	Detail     string   `json:"detail,omitempty"`
}

// DetectResult is the outcome of DetectSiteType.
type DetectResult struct {
	SiteType      SiteType       `json:"site_type"`
	Confidence    Confidence     `json:"confidence"`
	Score         map[string]int `json:"score,omitempty"`
	Hits          []EndpointHit  `json:"hits,omitempty"`
	PlatformHints map[string]any `json:"platform_hints,omitempty"`
	DetectedAt    time.Time      `json:"detected_at"`
	Warnings      []string       `json:"warnings,omitempty"`
}

// AccountCredential holds credentials used for account snapshot fetch.
type AccountCredential struct {
	APIKey            string
	SystemAccessToken string
	SubjectUserID     int64
	AccessToken       string
	RefreshToken      string
}

// FetchInput configures account snapshot retrieval.
type FetchInput struct {
	BaseURL    string
	SiteType   SiteType
	Credential AccountCredential
	Timeout    time.Duration
	// Days controls Sub2API /v1/usage?days= (default 7).
	Days int
}

// SubscriptionSummary is a compact subscription projection.
type SubscriptionSummary struct {
	Name         string     `json:"name,omitempty"`
	Status       string     `json:"status,omitempty"`
	Remaining    *float64   `json:"remaining,omitempty"`
	PeriodEnd    *time.Time `json:"period_end,omitempty"`
	DailyLimit   *float64   `json:"daily_limit,omitempty"`
	WeeklyLimit  *float64   `json:"weekly_limit,omitempty"`
	MonthlyLimit *float64   `json:"monthly_limit,omitempty"`
	DailyUsage   *float64   `json:"daily_usage,omitempty"`
	WeeklyUsage  *float64   `json:"weekly_usage,omitempty"`
	MonthlyUsage *float64   `json:"monthly_usage,omitempty"`
}

// UsageSummary is a compact usage projection.
type UsageSummary struct {
	TotalRequests *int64   `json:"total_requests,omitempty"`
	TotalCost     *float64 `json:"total_cost,omitempty"`
	TodayRequests *int64   `json:"today_requests,omitempty"`
	TodayCost     *float64 `json:"today_cost,omitempty"`
}

// BalanceDetail preserves one currency-specific balance breakdown. Providers
// such as DeepSeek can return more than one currency in a single response.
type BalanceDetail struct {
	Currency        string  `json:"currency"`
	TotalBalance    float64 `json:"total_balance"`
	GrantedBalance  float64 `json:"granted_balance"`
	ToppedUpBalance float64 `json:"topped_up_balance"`
}

// ExternalUserSummary is a non-sensitive external user identity projection.
type ExternalUserSummary struct {
	ID       string `json:"id,omitempty"`
	Username string `json:"username,omitempty"`
	Email    string `json:"email,omitempty"`
}

// AccountSnapshot is the normalized account/balance view shared by CLI and Web.
type AccountSnapshot struct {
	SiteType SiteType `json:"site_type"`
	Source   string   `json:"source"`
	Currency string   `json:"currency,omitempty"`
	Mode     string   `json:"mode,omitempty"`

	WalletBalance   *float64        `json:"wallet_balance,omitempty"`
	IsAvailable     *bool           `json:"is_available,omitempty"`
	BalanceDetails  []BalanceDetail `json:"balance_details,omitempty"`
	QuotaBalanceRaw *float64        `json:"quota_balance_raw,omitempty"`
	QuotaBalance    *float64        `json:"quota_balance,omitempty"`
	UsedQuotaRaw    *float64        `json:"used_quota_raw,omitempty"`
	UsedQuota       *float64        `json:"used_quota,omitempty"`
	QuotaLimit      *float64        `json:"quota_limit,omitempty"`
	QuotaRemaining  *float64        `json:"quota_remaining,omitempty"`

	QuotaDisplayScale        *float64 `json:"quota_display_scale,omitempty"`
	QuotaDisplayExchangeRate *float64 `json:"quota_display_exchange_rate,omitempty"`
	QuotaDisplayType         string   `json:"quota_display_type,omitempty"`
	QuotaDisplayUnit         string   `json:"quota_display_unit,omitempty"`

	PlanName      string                `json:"plan_name,omitempty"`
	Subscriptions []SubscriptionSummary `json:"subscriptions,omitempty"`
	Usage         *UsageSummary         `json:"usage,omitempty"`
	ExternalUser  *ExternalUserSummary  `json:"external_user,omitempty"`

	FetchedAt time.Time `json:"fetched_at"`
	Partial   bool      `json:"partial,omitempty"`
	Errors    []string  `json:"errors,omitempty"`
}

// AccountView is the stable DTO for CLI/JSON/Web consumers.
type AccountView struct {
	SiteType       string                `json:"site_type"`
	Confidence     string                `json:"confidence,omitempty"`
	Source         string                `json:"source,omitempty"`
	Mode           string                `json:"mode,omitempty"`
	Currency       string                `json:"currency,omitempty"`
	BalanceLabel   string                `json:"balance_label,omitempty"`
	BalanceValue   *float64              `json:"balance_value,omitempty"`
	WalletBalance  *float64              `json:"wallet_balance,omitempty"`
	IsAvailable    *bool                 `json:"is_available,omitempty"`
	BalanceDetails []BalanceDetail       `json:"balance_details,omitempty"`
	QuotaRemaining *float64              `json:"quota_remaining,omitempty"`
	QuotaUsed      *float64              `json:"quota_used,omitempty"`
	QuotaLimit     *float64              `json:"quota_limit,omitempty"`
	QuotaBalance   *float64              `json:"quota_balance,omitempty"`
	PlanName       string                `json:"plan_name,omitempty"`
	Subscriptions  []SubscriptionSummary `json:"subscriptions,omitempty"`
	Usage          *UsageSummary         `json:"usage,omitempty"`
	FetchedAt      string                `json:"fetched_at,omitempty"`
	Partial        bool                  `json:"partial,omitempty"`
	Errors         []string              `json:"errors,omitempty"`
	DisplayUnit    string                `json:"display_unit,omitempty"`
	DisplayType    string                `json:"display_type,omitempty"`
}

// Float64 returns a pointer to v.
func Float64(v float64) *float64 { return &v }

// Int64 returns a pointer to v.
func Int64(v int64) *int64 { return &v }

// CloneFloat64 clones a float pointer.
func CloneFloat64(v *float64) *float64 {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}

// CloneBool clones a bool pointer.
func CloneBool(v *bool) *bool {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}
