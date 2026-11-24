package memorystore

import (
	"fmt"
	"strings"
)

func normalizeRecordTrust(record *Record) error {
	if record == nil {
		return fmt.Errorf("memorystore: record is nil")
	}
	level := TrustLevel(strings.ToLower(strings.TrimSpace(string(record.TrustLevel))))
	if level == "" {
		sourceKind := strings.ToLower(strings.TrimSpace(record.SourceKind))
		sourceType := strings.ToLower(strings.TrimSpace(record.SourceType))
		sourceTrust := strings.ToLower(strings.TrimSpace(record.SourceTrust))
		switch {
		case record.Kind == "system_policy":
			level = TrustSystemPolicy
		case sourceKind == "manual":
			level = TrustUserExplicit
		case sourceKind == "correction":
			level = TrustUserConfirmed
		case sourceTrust == string(TrustUntrustedExternal):
			level = TrustUntrustedExternal
		case sourceType == "tool", sourceType == "web", sourceType == "repo":
			level = TrustUntrustedExternal
		case sourceType == "manual":
			level = TrustUserExplicit
		default:
			level = TrustAgentInference
		}
	}
	switch level {
	case TrustSystemPolicy, TrustUserExplicit, TrustUserConfirmed, TrustAgentInference, TrustUntrustedExternal:
	default:
		return fmt.Errorf("memorystore: invalid trust level %q", level)
	}
	record.TrustLevel = level
	record.TrustReason = strings.TrimSpace(record.TrustReason)
	if record.TrustReason == "" {
		record.TrustReason = "source_kind:" + strings.TrimSpace(record.SourceKind)
	}
	if record.TrustedAt.IsZero() {
		record.TrustedAt = record.CreatedAt
		if record.TrustedAt.IsZero() {
			record.TrustedAt = record.UpdatedAt
		}
	}
	return nil
}

func canPromoteTrustedRecord(record Record) bool {
	if record.TrustLevel != TrustUntrustedExternal {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(record.Kind)) {
	case "procedure", "workflow", "policy", "system_policy":
		return false
	default:
		return true
	}
}
