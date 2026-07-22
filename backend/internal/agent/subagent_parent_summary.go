package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	defaultSubagentParentMetadataBudgetBytes = 8 * 1024
	maxSubagentParentFindingCount            = 3
	maxSubagentParentPatchCount              = 8
)

type subagentParentMetadataSummary struct {
	Reports   []map[string]interface{}
	ByteCount int
	SHA256    string
	Budget    int
	Truncated bool
	Omitted   int
}

// summarizeSubagentReportsForParent keeps child output metadata bounded. The
// rendered tool output is archived by the output gateway, while this compact
// projection is the only report structure copied into the parent message.
func summarizeSubagentReportsForParent(reports []SubagentResult, budget int) subagentParentMetadataSummary {
	if budget <= 0 {
		budget = defaultSubagentParentMetadataBudgetBytes
	}
	raw, _ := json.Marshal(reports)
	summary := subagentParentMetadataSummary{
		ByteCount: len(raw),
		SHA256:    fmt.Sprintf("%x", sha256.Sum256(raw)),
		Budget:    budget,
	}
	if len(reports) == 0 {
		return summary
	}

	perReportBudget := budget / len(reports)
	if perReportBudget > 2048 {
		perReportBudget = 2048
	}
	for _, report := range reports {
		projection, truncated := projectSubagentReportForParent(report, perReportBudget)
		candidate := append(summary.Reports, projection)
		encoded, _ := json.Marshal(candidate)
		if len(encoded) > budget {
			summary.Truncated = true
			summary.Omitted++
			continue
		}
		summary.Reports = candidate
		summary.Truncated = summary.Truncated || truncated
	}
	return summary
}

func projectSubagentReportForParent(report SubagentResult, budget int) (map[string]interface{}, bool) {
	if budget < 256 {
		budget = 256
	}
	textBudget := budget / 2
	projection := map[string]interface{}{
		"id":         truncateSubagentParentText(report.ID, 160),
		"role":       truncateSubagentParentText(report.Role, 80),
		"session_id": truncateSubagentParentText(report.SessionID, 160),
		"success":    report.Success,
		"read_only":  report.ReadOnly,
	}
	truncated := report.Contract != nil
	if report.BudgetTokens > 0 {
		projection["budget_tokens"] = report.BudgetTokens
	}
	if report.Usage != nil {
		projection["usage_total_tokens"] = report.Usage.TotalTokens
	}

	summary, summaryTruncated := truncateSubagentParentTextWithFlag(report.Summary, textBudget)
	if summary != "" {
		projection["summary"] = summary
	}
	truncated = truncated || summaryTruncated
	errorText, errorTruncated := truncateSubagentParentTextWithFlag(report.Error, budget/6)
	if errorText != "" {
		projection["error"] = errorText
	}
	truncated = truncated || errorTruncated

	findings := make([]string, 0, maxSubagentParentFindingCount)
	for index, finding := range report.Findings {
		if index >= maxSubagentParentFindingCount {
			truncated = true
			break
		}
		value, valueTruncated := truncateSubagentParentTextWithFlag(finding, budget/10)
		if value != "" {
			findings = append(findings, value)
		}
		truncated = truncated || valueTruncated
	}
	if len(findings) > 0 {
		projection["findings"] = findings
	}

	patches := make([]map[string]interface{}, 0, maxSubagentParentPatchCount)
	for index, patch := range report.Patches {
		if index >= maxSubagentParentPatchCount {
			truncated = true
			break
		}
		item := map[string]interface{}{}
		if path := truncateSubagentParentText(patch.Path, 320); path != "" {
			item["path"] = path
		}
		if patchSummary := truncateSubagentParentText(patch.Summary, budget/12); patchSummary != "" {
			item["summary"] = patchSummary
		}
		if patch.ApplyStatus != "" {
			item["apply_status"] = truncateSubagentParentText(patch.ApplyStatus, 80)
		}
		if patch.VerificationStatus != "" {
			item["verification_status"] = truncateSubagentParentText(patch.VerificationStatus, 80)
		}
		if len(patch.ArtifactRefs) > 0 {
			item["artifact_refs"] = compactSubagentArtifactRefs(patch.ArtifactRefs)
		}
		if patch.Diff != "" {
			truncated = true
		}
		if len(item) > 0 {
			patches = append(patches, item)
		}
	}
	if len(patches) > 0 {
		projection["patches"] = patches
	}
	if truncated {
		projection["truncated"] = true
	}
	return projection, truncated
}

func compactSubagentArtifactRefs(values []string) []string {
	const maxRefs = 8
	refs := make([]string, 0, minInt(len(values), maxRefs))
	for _, value := range values {
		value = truncateSubagentParentText(value, 256)
		if value == "" {
			continue
		}
		refs = append(refs, value)
		if len(refs) >= maxRefs {
			break
		}
	}
	return refs
}

func truncateSubagentParentText(value string, limit int) string {
	result, _ := truncateSubagentParentTextWithFlag(value, limit)
	return result
}

func truncateSubagentParentTextWithFlag(value string, limit int) (string, bool) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
	if value == "" || limit <= 0 {
		return "", value != ""
	}
	if len(value) <= limit {
		return value, false
	}
	const marker = "...(truncated)"
	if limit <= len(marker) {
		return safeSubagentUTF8Prefix(value, limit), true
	}
	return strings.TrimSpace(safeSubagentUTF8Prefix(value, limit-len(marker))) + marker, true
}

func safeSubagentUTF8Prefix(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	prefix := value[:limit]
	for len(prefix) > 0 && !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
