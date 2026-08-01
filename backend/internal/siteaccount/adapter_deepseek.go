package siteaccount

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	deepSeekBalancePath   = "/user/balance"
	deepSeekBalanceSource = "deepseek_user_balance"
)

type deepSeekAdapter struct{}

func (deepSeekAdapter) SiteType() SiteType { return SiteTypeDeepSeek }
func (deepSeekAdapter) AccountSources() []string {
	return []string{deepSeekBalanceSource}
}
func (deepSeekAdapter) Probes() []EndpointProbe {
	return []EndpointProbe{{
		Path:        deepSeekBalancePath,
		Label:       "DeepSeek user balance",
		Score:       10,
		Protected:   true,
		BodyMatches: looksLikeDeepSeekBalanceChallenge,
	}}
}
func (deepSeekAdapter) Fetch(ctx context.Context, client *Client, input FetchInput) (*AccountSnapshot, error) {
	return client.fetchDeepSeekBalance(ctx, input)
}

type deepSeekBalanceResponse struct {
	IsAvailable  *bool                 `json:"is_available"`
	BalanceInfos []deepSeekBalanceInfo `json:"balance_infos"`
}

type deepSeekBalanceInfo struct {
	Currency        string `json:"currency"`
	TotalBalance    string `json:"total_balance"`
	GrantedBalance  string `json:"granted_balance"`
	ToppedUpBalance string `json:"topped_up_balance"`
}

func (c *Client) fetchDeepSeekBalance(ctx context.Context, input FetchInput) (*AccountSnapshot, error) {
	baseURL := strings.TrimSpace(input.BaseURL)
	if baseURL == "" {
		return nil, invalidInput("base_url is required")
	}
	apiKey := strings.TrimSpace(input.Credential.APIKey)
	if apiKey == "" {
		return nil, missingCredential("api key is required for DeepSeek /user/balance")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(ctx, resolveTimeout(input.Timeout, defaultHTTPTimeout))
	defer cancel()
	origin, err := OriginURL(baseURL)
	if err != nil {
		return nil, err
	}
	result, err := doGET(reqCtx, c.HTTP, JoinURL(origin, deepSeekBalancePath), "application/json", bearerHeaders(apiKey))
	if err != nil {
		return nil, err
	}
	if result.StatusCode == http.StatusUnauthorized || result.StatusCode == http.StatusForbidden {
		return nil, unauthorized(fmt.Sprintf("DeepSeek /user/balance returned HTTP %d", result.StatusCode))
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return nil, httpError(fmt.Sprintf("DeepSeek /user/balance returned HTTP %d", result.StatusCode), nil)
	}
	var payload deepSeekBalanceResponse
	if err := json.Unmarshal(result.Body, &payload); err != nil {
		return nil, unexpectedPayload("decode DeepSeek /user/balance response", err)
	}
	return normalizeDeepSeekBalance(payload, time.Now().UTC())
}

func normalizeDeepSeekBalance(payload deepSeekBalanceResponse, fetchedAt time.Time) (*AccountSnapshot, error) {
	if payload.IsAvailable == nil {
		return nil, unexpectedPayload("DeepSeek /user/balance response is missing is_available", nil)
	}
	if len(payload.BalanceInfos) == 0 {
		return nil, unexpectedPayload("DeepSeek /user/balance response contains no balance_infos", nil)
	}
	details := make([]BalanceDetail, 0, len(payload.BalanceInfos))
	for index, info := range payload.BalanceInfos {
		currency := strings.ToUpper(strings.TrimSpace(info.Currency))
		if currency == "" {
			return nil, unexpectedPayload(fmt.Sprintf("DeepSeek balance_infos[%d] is missing currency", index), nil)
		}
		total, err := parseDeepSeekAmount(info.TotalBalance)
		if err != nil {
			return nil, unexpectedPayload(fmt.Sprintf("parse DeepSeek balance_infos[%d].total_balance", index), err)
		}
		granted, err := parseDeepSeekAmount(info.GrantedBalance)
		if err != nil {
			return nil, unexpectedPayload(fmt.Sprintf("parse DeepSeek balance_infos[%d].granted_balance", index), err)
		}
		toppedUp, err := parseDeepSeekAmount(info.ToppedUpBalance)
		if err != nil {
			return nil, unexpectedPayload(fmt.Sprintf("parse DeepSeek balance_infos[%d].topped_up_balance", index), err)
		}
		details = append(details, BalanceDetail{Currency: currency, TotalBalance: total, GrantedBalance: granted, ToppedUpBalance: toppedUp})
	}
	primary := details[0]
	return &AccountSnapshot{
		SiteType: SiteTypeDeepSeek, Source: deepSeekBalanceSource, Mode: "wallet",
		Currency: primary.Currency, WalletBalance: Float64(primary.TotalBalance),
		IsAvailable: CloneBool(payload.IsAvailable), BalanceDetails: details, FetchedAt: fetchedAt,
	}, nil
}

func parseDeepSeekAmount(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("amount is empty")
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("amount is not finite")
	}
	return value, nil
}
