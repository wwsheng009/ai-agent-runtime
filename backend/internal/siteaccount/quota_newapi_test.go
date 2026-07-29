package siteaccount

import "testing"

func TestConvertNewAPIQuota_DefaultUSD(t *testing.T) {
	cfg := DefaultNewAPIQuotaDisplayConfig()
	got := ConvertNewAPIQuota(1_000_000, cfg)
	if got != 2.0 {
		t.Fatalf("ConvertNewAPIQuota() = %v, want 2.0", got)
	}
}

func TestConvertNewAPIQuota_CNYExchangeRate(t *testing.T) {
	cfg := NewAPIQuotaDisplayConfig{
		Scale:        Float64(500000),
		ExchangeRate: Float64(7.1),
		DisplayType:  "CNY",
		DisplayUnit:  "CNY",
		Currency:     "CNY",
	}
	got := ConvertNewAPIQuota(500000, cfg)
	if got != 7.1 {
		t.Fatalf("ConvertNewAPIQuota() = %v, want 7.1", got)
	}
}

func TestConvertNewAPIQuota_TokensNoScale(t *testing.T) {
	cfg := NewAPIQuotaDisplayConfig{
		Scale:       Float64(500000),
		DisplayType: "TOKENS",
		DisplayUnit: "tokens",
		Currency:    "TOKENS",
	}
	got := ConvertNewAPIQuota(12345, cfg)
	if got != 12345 {
		t.Fatalf("ConvertNewAPIQuota() = %v, want 12345", got)
	}
}

func TestConvertNewAPIQuota_InvalidScaleFallsBack(t *testing.T) {
	cfg := NewAPIQuotaDisplayConfig{
		Scale:       Float64(0),
		DisplayType: "USD",
	}
	got := ConvertNewAPIQuota(42, cfg)
	if got != 42 {
		t.Fatalf("ConvertNewAPIQuota() = %v, want 42", got)
	}
}

func TestConvertNewAPIQuota_QuotaDisplayType(t *testing.T) {
	cfg := NewAPIQuotaDisplayConfig{
		Scale:       Float64(500000),
		DisplayType: "QUOTA",
	}
	got := ConvertNewAPIQuota(999, cfg)
	if got != 999 {
		t.Fatalf("ConvertNewAPIQuota() = %v, want 999", got)
	}
}

func TestNewAPIQuotaDisplayConfigFromStatusJSON(t *testing.T) {
	raw := []byte(`{"success":true,"data":{"quota_per_unit":100000,"quota_display_type":"CNY","usd_exchange_rate":"7.2"}}`)
	cfg := NewAPIQuotaDisplayConfigFromStatusJSON(raw)
	if cfg.Scale == nil || *cfg.Scale != 100000 {
		t.Fatalf("unexpected scale: %+v", cfg.Scale)
	}
	if cfg.DisplayType != "CNY" || cfg.Currency != "CNY" {
		t.Fatalf("unexpected display type: %+v", cfg)
	}
	if cfg.ExchangeRate == nil || *cfg.ExchangeRate != 7.2 {
		t.Fatalf("unexpected exchange rate: %+v", cfg.ExchangeRate)
	}
}
