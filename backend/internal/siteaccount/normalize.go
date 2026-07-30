package siteaccount

import (
	"fmt"
	"strings"
	"time"
)

// NormalizeAccountView converts a snapshot (+ optional detect confidence) into a stable DTO.
func NormalizeAccountView(snapshot *AccountSnapshot, confidence Confidence) AccountView {
	view := AccountView{}
	if snapshot == nil {
		return view
	}
	view.SiteType = string(snapshot.SiteType)
	view.Confidence = string(confidence)
	view.Source = snapshot.Source
	view.Mode = snapshot.Mode
	view.Currency = firstNonEmpty(snapshot.Currency, snapshot.QuotaDisplayUnit)
	view.WalletBalance = CloneFloat64(snapshot.WalletBalance)
	view.QuotaRemaining = CloneFloat64(snapshot.QuotaRemaining)
	view.QuotaUsed = CloneFloat64(snapshot.UsedQuota)
	view.QuotaLimit = CloneFloat64(snapshot.QuotaLimit)
	view.QuotaBalance = CloneFloat64(snapshot.QuotaBalance)
	view.PlanName = snapshot.PlanName
	view.Subscriptions = append([]SubscriptionSummary(nil), snapshot.Subscriptions...)
	view.Usage = snapshot.Usage
	view.Partial = snapshot.Partial
	view.Errors = append([]string(nil), snapshot.Errors...)
	view.DisplayUnit = firstNonEmpty(snapshot.QuotaDisplayUnit, snapshot.Currency)
	view.DisplayType = snapshot.QuotaDisplayType
	if !snapshot.FetchedAt.IsZero() {
		view.FetchedAt = snapshot.FetchedAt.UTC().Format(time.RFC3339)
	}

	label, value := primaryBalance(snapshot)
	view.BalanceLabel = label
	view.BalanceValue = value
	return view
}

func primaryBalance(snapshot *AccountSnapshot) (string, *float64) {
	if snapshot == nil {
		return "", nil
	}
	unit := firstNonEmpty(snapshot.QuotaDisplayUnit, snapshot.Currency, "USD")
	if snapshot.QuotaRemaining != nil {
		return fmt.Sprintf("remaining (%s)", unit), CloneFloat64(snapshot.QuotaRemaining)
	}
	if snapshot.WalletBalance != nil {
		return fmt.Sprintf("wallet (%s)", unit), CloneFloat64(snapshot.WalletBalance)
	}
	if snapshot.QuotaBalance != nil {
		return fmt.Sprintf("quota (%s)", unit), CloneFloat64(snapshot.QuotaBalance)
	}
	if len(snapshot.Subscriptions) > 0 && snapshot.Subscriptions[0].Remaining != nil {
		return fmt.Sprintf("subscription remaining (%s)", unit), CloneFloat64(snapshot.Subscriptions[0].Remaining)
	}
	return "", nil
}

// FormatBalanceAmount formats a balance/money value with exactly two decimal places.
func FormatBalanceAmount(value float64) string {
	return fmt.Sprintf("%.2f", value)
}

// FormatBalanceLine returns a one-line human-readable balance summary.
func FormatBalanceLine(view AccountView) string {
	if view.BalanceValue != nil {
		unit := firstNonEmpty(view.DisplayUnit, view.Currency, "USD")
		mode := view.Mode
		if mode == "" {
			mode = "unknown"
		}
		source := view.Source
		if source == "" {
			source = "unknown"
		}
		return fmt.Sprintf("%s %s %s (%s, source=%s)",
			firstNonEmpty(view.BalanceLabel, "balance"),
			FormatBalanceAmount(*view.BalanceValue),
			unit,
			mode,
			source,
		)
	}
	if view.Source != "" {
		parts := []string{fmt.Sprintf("source=%s", view.Source)}
		if view.Mode != "" {
			parts = append(parts, "mode="+view.Mode)
		}
		if view.Partial {
			parts = append(parts, "partial")
		}
		return "account synced (" + strings.Join(parts, ", ") + ")"
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
