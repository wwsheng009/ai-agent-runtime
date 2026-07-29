package siteaccount

import (
	"encoding/json"
	"strconv"
	"strings"
)

const (
	defaultNewAPIQuotaPerUnit   = 500000
	newAPIQuotaDisplayTypeQuota = "QUOTA"
)

// NewAPIQuotaDisplayConfig controls New-API raw quota → display unit conversion.
type NewAPIQuotaDisplayConfig struct {
	Scale        *float64
	ExchangeRate *float64
	DisplayType  string
	DisplayUnit  string
	Currency     string
}

// DefaultNewAPIQuotaDisplayConfig returns the default USD scale config.
func DefaultNewAPIQuotaDisplayConfig() NewAPIQuotaDisplayConfig {
	return NewAPIQuotaDisplayConfig{
		Scale:        Float64(defaultNewAPIQuotaPerUnit),
		ExchangeRate: Float64(1),
		DisplayType:  "USD",
		DisplayUnit:  "USD",
		Currency:     "USD",
	}
}

// ConvertNewAPIQuota converts a raw New-API quota value into display units.
// This is the single source of truth for CLI/server/Web consumers.
func ConvertNewAPIQuota(raw float64, cfg NewAPIQuotaDisplayConfig) float64 {
	if cfg.Scale == nil || *cfg.Scale <= 0 || strings.EqualFold(cfg.DisplayType, newAPIQuotaDisplayTypeQuota) {
		return raw
	}
	value := raw
	if !strings.EqualFold(cfg.DisplayType, "TOKENS") {
		value /= *cfg.Scale
		if cfg.ExchangeRate != nil && *cfg.ExchangeRate > 0 {
			value *= *cfg.ExchangeRate
		}
	}
	return value
}

// ConvertNewAPIQuotaPtr converts an optional raw value.
func ConvertNewAPIQuotaPtr(raw *float64, cfg NewAPIQuotaDisplayConfig) *float64 {
	if raw == nil {
		return nil
	}
	return Float64(ConvertNewAPIQuota(*raw, cfg))
}

// NewAPIQuotaDisplayConfigFromStatusJSON builds display config from /api/status payload.
// Accepts either the raw status data object or a full {success,data} envelope.
func NewAPIQuotaDisplayConfigFromStatusJSON(raw []byte) NewAPIQuotaDisplayConfig {
	cfg := DefaultNewAPIQuotaDisplayConfig()
	if len(raw) == 0 {
		return cfg
	}

	data := json.RawMessage(raw)
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		data = envelope.Data
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return cfg
	}
	scale, ok := quotaDisplayFloat(payload, "quota_per_unit", "quotaPerUnit")
	if !ok || scale <= 0 {
		return cfg
	}
	cfg.Scale = Float64(scale)

	displayType := strings.ToUpper(quotaDisplayString(payload, "quota_display_type", "quotaDisplayType"))
	if displayType == "" {
		if displayInCurrency, exists := quotaDisplayBool(payload, "display_in_currency", "displayInCurrency"); exists && !displayInCurrency {
			displayType = "TOKENS"
		} else {
			displayType = "USD"
		}
	}

	rate := 1.0
	switch displayType {
	case "TOKENS":
		cfg.DisplayUnit = "tokens"
		cfg.Currency = "TOKENS"
	case "CNY":
		if value, exists := quotaDisplayFloat(payload, "usd_exchange_rate", "usdExchangeRate"); exists && value > 0 {
			rate = value
		}
		cfg.DisplayUnit = "CNY"
		cfg.Currency = "CNY"
	case "CUSTOM":
		if value, exists := quotaDisplayFloat(payload, "custom_currency_exchange_rate", "customCurrencyExchangeRate"); exists && value > 0 {
			rate = value
		}
		cfg.DisplayUnit = quotaDisplayString(payload, "custom_currency_symbol", "customCurrencySymbol")
		if cfg.DisplayUnit == "" {
			cfg.DisplayUnit = "CUSTOM"
		}
		cfg.Currency = "CUSTOM"
	case "QUOTA":
		cfg.DisplayUnit = "QUOTA"
		cfg.Currency = "QUOTA"
	default:
		displayType = "USD"
		cfg.DisplayUnit = "USD"
		cfg.Currency = "USD"
	}
	cfg.DisplayType = displayType
	cfg.ExchangeRate = Float64(rate)
	return cfg
}

func quotaDisplayString(payload map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) == nil {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func quotaDisplayFloat(payload map[string]json.RawMessage, keys ...string) (float64, bool) {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		var number float64
		if json.Unmarshal(raw, &number) == nil {
			return number, true
		}
		var text string
		if json.Unmarshal(raw, &text) == nil {
			parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func quotaDisplayBool(payload map[string]json.RawMessage, keys ...string) (bool, bool) {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		var value bool
		if json.Unmarshal(raw, &value) == nil {
			return value, true
		}
	}
	return false, false
}
