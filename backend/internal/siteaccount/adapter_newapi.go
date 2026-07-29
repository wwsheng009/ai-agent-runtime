package siteaccount

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	newAPIUserSelfSource = "newapi_user_self"
	newAPIStatusPath     = "/api/status"
	newAPIUserSelfPath   = "/api/user/self"
)

type newAPIEnvelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type newAPIUserSelfData struct {
	ID           json.RawMessage `json:"id"`
	Username     string          `json:"username"`
	DisplayName  string          `json:"display_name"`
	Email        string          `json:"email"`
	Group        string          `json:"group"`
	Quota        *float64        `json:"quota"`
	UsedQuota    *float64        `json:"used_quota"`
	RequestCount *int64          `json:"request_count"`
}

func (c *Client) fetchNewAPIUserSelf(ctx context.Context, input FetchInput) (*AccountSnapshot, error) {
	baseURL := strings.TrimSpace(input.BaseURL)
	if baseURL == "" {
		return nil, invalidInput("base_url is required")
	}
	token := strings.TrimSpace(input.Credential.SystemAccessToken)
	if token == "" {
		return nil, missingCredential("new-api system access token is required")
	}
	if input.Credential.SubjectUserID <= 0 {
		return nil, missingCredential("new-api subject user id is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := resolveTimeout(input.Timeout, defaultHTTPTimeout)
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	origin, err := OriginURL(baseURL)
	if err != nil {
		return nil, err
	}

	statusResult, statusErr := doGET(reqCtx, c.HTTP, JoinURL(origin, newAPIStatusPath), "application/json", nil)
	displayCfg := DefaultNewAPIQuotaDisplayConfig()
	partial := false
	var errs []string
	if statusErr != nil {
		partial = true
		errs = append(errs, fmt.Sprintf("fetch /api/status failed: %v", statusErr))
	} else if statusResult.StatusCode < 200 || statusResult.StatusCode >= 300 {
		partial = true
		errs = append(errs, fmt.Sprintf("/api/status returned HTTP %d", statusResult.StatusCode))
	} else {
		displayCfg = NewAPIQuotaDisplayConfigFromStatusJSON(statusResult.Body)
	}

	headers := bearerHeaders(token)
	headers["New-Api-User"] = strconv.FormatInt(input.Credential.SubjectUserID, 10)
	selfResult, selfErr := doGET(reqCtx, c.HTTP, JoinURL(origin, newAPIUserSelfPath), "application/json", headers)
	if selfErr != nil {
		return nil, selfErr
	}
	if selfResult.StatusCode == http.StatusUnauthorized || selfResult.StatusCode == http.StatusForbidden {
		return nil, unauthorized(fmt.Sprintf("New-API /api/user/self returned HTTP %d", selfResult.StatusCode))
	}
	if selfResult.StatusCode < 200 || selfResult.StatusCode >= 300 {
		return nil, httpError(fmt.Sprintf("New-API /api/user/self returned HTTP %d", selfResult.StatusCode), nil)
	}

	selfData, err := decodeNewAPIUserSelf(selfResult.Body)
	if err != nil {
		return nil, err
	}
	return normalizeNewAPIUserSelf(selfData, displayCfg, partial, errs, time.Now().UTC()), nil
}

func decodeNewAPIUserSelf(raw []byte) (newAPIUserSelfData, error) {
	var data newAPIUserSelfData
	if len(raw) == 0 {
		return data, unexpectedPayload("empty New-API /api/user/self response", nil)
	}

	var envelope newAPIEnvelope
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, &data); err != nil {
			return data, unexpectedPayload("decode New-API /api/user/self data", err)
		}
		if envelope.Success == false && strings.TrimSpace(envelope.Message) != "" {
			return data, unexpectedPayload(fmt.Sprintf("New-API /api/user/self reported failure: %s", envelope.Message), nil)
		}
		return data, nil
	}

	if err := json.Unmarshal(raw, &data); err != nil {
		return data, unexpectedPayload("decode New-API /api/user/self response", err)
	}
	return data, nil
}

func normalizeNewAPIUserSelf(
	data newAPIUserSelfData,
	cfg NewAPIQuotaDisplayConfig,
	partial bool,
	errs []string,
	fetchedAt time.Time,
) *AccountSnapshot {
	snapshot := &AccountSnapshot{
		SiteType:                 SiteTypeNewAPI,
		Source:                   newAPIUserSelfSource,
		Mode:                     "account_quota",
		Currency:                 firstNonEmpty(cfg.Currency, cfg.DisplayUnit, "USD"),
		QuotaDisplayScale:        CloneFloat64(cfg.Scale),
		QuotaDisplayExchangeRate: CloneFloat64(cfg.ExchangeRate),
		QuotaDisplayType:         cfg.DisplayType,
		QuotaDisplayUnit:         firstNonEmpty(cfg.DisplayUnit, cfg.Currency, "USD"),
		FetchedAt:                fetchedAt,
		Partial:                  partial,
		Errors:                   append([]string(nil), errs...),
	}

	if data.Quota != nil {
		snapshot.QuotaBalanceRaw = CloneFloat64(data.Quota)
		snapshot.QuotaBalance = ConvertNewAPIQuotaPtr(data.Quota, cfg)
		snapshot.QuotaRemaining = ConvertNewAPIQuotaPtr(data.Quota, cfg)
	}
	if data.UsedQuota != nil {
		snapshot.UsedQuotaRaw = CloneFloat64(data.UsedQuota)
		snapshot.UsedQuota = ConvertNewAPIQuotaPtr(data.UsedQuota, cfg)
	}
	if data.Quota != nil && data.UsedQuota != nil {
		totalRaw := *data.Quota + *data.UsedQuota
		snapshot.QuotaLimit = ConvertNewAPIQuotaPtr(Float64(totalRaw), cfg)
	}
	if data.RequestCount != nil {
		snapshot.Usage = &UsageSummary{TotalRequests: data.RequestCount}
	}

	userID := decodeFlexibleID(data.ID)
	username := firstNonEmpty(strings.TrimSpace(data.Username), strings.TrimSpace(data.DisplayName))
	if userID != "" || username != "" || strings.TrimSpace(data.Email) != "" {
		snapshot.ExternalUser = &ExternalUserSummary{
			ID:       userID,
			Username: username,
			Email:    strings.TrimSpace(data.Email),
		}
	}
	if group := strings.TrimSpace(data.Group); group != "" {
		snapshot.PlanName = group
	}

	if snapshot.QuotaRemaining == nil && snapshot.UsedQuota == nil {
		snapshot.Partial = true
		snapshot.Errors = append(snapshot.Errors, "New-API /api/user/self returned no quota fields")
	}
	return snapshot
}

func decodeFlexibleID(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return strings.TrimSpace(asString)
	}
	var asNumber json.Number
	if json.Unmarshal(raw, &asNumber) == nil {
		return strings.TrimSpace(asNumber.String())
	}
	var asInt int64
	if json.Unmarshal(raw, &asInt) == nil {
		return strconv.FormatInt(asInt, 10)
	}
	var asFloat float64
	if json.Unmarshal(raw, &asFloat) == nil {
		return strconv.FormatInt(int64(asFloat), 10)
	}
	return strings.Trim(string(raw), `"`)
}
