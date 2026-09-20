package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
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
	// OmittedRefs carries one dereference pointer per report whose payload did
	// not fit the batch budget. H4: "omitted" must stay expandable instead of
	// being a dead end the parent cannot follow up on.
	OmittedRefs []string
	// Deduplicated counts reports folded into an identical-conclusion sibling,
	// Conflicts counts reports that disagree with a sibling about the same
	// claim (P2-1 / H5). Both are advisory: nothing is arbitrated here.
	Deduplicated int
	Conflicts    int
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

	groups, deduplicated := groupSubagentReportsByConclusion(reports)
	summary.Deduplicated = deduplicated
	conflicts, conflictCount := detectSubagentReportConflicts(groups)
	summary.Conflicts = conflictCount

	perReportBudget := budget / len(groups)
	if perReportBudget > 2048 {
		perReportBudget = 2048
	}
	for index, group := range groups {
		report := group.report
		projection, truncated := projectSubagentReportForParent(report, perReportBudget)
		if len(group.duplicateSources) > 0 {
			// The merged children stay named so the parent can tell that several
			// subagents reached this conclusion (P2-1: keep the source labels).
			projection["duplicate_sources"] = group.duplicateSources
			projection["duplicate_count"] = len(group.duplicateSources)
		}
		if sources, ok := conflicts[index]; ok {
			// Same claim, different conclusion: present both, never pick one.
			projection["conflict"] = true
			projection["conflict_sources"] = sources
		}
		candidate := append(summary.Reports, projection)
		encoded, _ := json.Marshal(candidate)
		if len(encoded) > budget {
			// The full projection does not fit: fall back to a minimal stub that
			// still names the child and carries the dereference pointer, so the
			// parent can retrieve the body instead of losing the child entirely.
			stub := subagentReportStubForParent(report)
			stubCandidate := append(summary.Reports, stub)
			stubEncoded, _ := json.Marshal(stubCandidate)
			summary.Truncated = true
			summary.Omitted++
			if len(stubEncoded) <= budget {
				// The stub fits and already carries its own dereference pointer.
				summary.Reports = stubCandidate
				continue
			}
			// Even the stub does not fit: the report leaves the payload, so it
			// must leave a pointer behind (H4).
			summary.recordOmittedRef(report)
			continue
		}
		summary.Reports = candidate
		summary.Truncated = summary.Truncated || truncated
	}
	return summary
}

// maxSubagentParentOmittedRefs bounds the pointer list so a huge batch cannot
// grow the parent projection through the recovery path itself.
const maxSubagentParentOmittedRefs = 32

// recordOmittedRef keeps the read_agent_result pointer for a report that had to
// be stubbed, so "omitted" is expandable (H4/P2-1).
func (s *subagentParentMetadataSummary) recordOmittedRef(report SubagentResult) {
	if s == nil || len(s.OmittedRefs) >= maxSubagentParentOmittedRefs {
		return
	}
	action := subagentReportDereferenceAction(report)
	if action == "" {
		return
	}
	for _, existing := range s.OmittedRefs {
		if existing == action {
			return
		}
	}
	s.OmittedRefs = append(s.OmittedRefs, action)
}

// subagentReportStubForParent is the smallest projection that still identifies
// the child and tells the parent how to fetch the full deliverable.
func subagentReportStubForParent(report SubagentResult) map[string]interface{} {
	stub := map[string]interface{}{
		"id":        truncateSubagentParentText(report.ID, 160),
		"success":   report.Success,
		"truncated": true,
		"omitted":   true,
	}
	if session := truncateSubagentParentText(report.SessionID, 160); session != "" {
		stub["session_id"] = session
	}
	if action := subagentReportDereferenceAction(report); action != "" {
		stub["next_action"] = action
	}
	return stub
}

// subagentReportDereferenceAction renders the single tool call that retrieves
// this child's full deliverable, matching read_agent_result's accepted id forms
// (session id, agent id or agent path).
func subagentReportDereferenceAction(report SubagentResult) string {
	target := strings.TrimSpace(report.SessionID)
	if target == "" {
		target = strings.TrimSpace(report.ID)
	}
	if target == "" {
		return ""
	}
	return "read_agent_result(id=" + target + ", sections=[\"summary\"], offset=0, limit=8000)"
}

// ---------------------------------------------------------------------------
// P2-1 / H5: conclusion-level aggregation.
//
// The projection used to be a plain concatenation of per-child projections, so
// two children reporting the same conclusion spent the budget twice, two
// children disagreeing looked like two unrelated findings, and nothing pinned
// the rendered order. The pass below keeps the projection a faithful, bounded
// view of the batch:
//
//   - exact-fingerprint dedup folds byte-identical conclusions (after
//     normalisation) into the first declaration-ordered occurrence and records
//     the merged source labels on the surviving entry;
//   - same-claim/different-conclusion reports stay side by side and are flagged
//     as a conflict: the parent decides, this code never arbitrates;
//   - grouping preserves the caller's order, which the scheduler already fills
//     by task declaration index, so the projection is reproducible.
//
// Dedup is deliberately fingerprint-exact: fuzzy merging could silently drop a
// genuinely different conclusion, which is worse than showing a duplicate.
// ---------------------------------------------------------------------------

const (
	// minSubagentConclusionFingerprintRunes avoids merging children that only
	// share a trivial summary ("done", "ok"): a fingerprint is only meaningful
	// once there is a conclusion to compare.
	minSubagentConclusionFingerprintRunes = 24
	// maxSubagentParentDuplicateSources bounds the merged-source label list so
	// dedup itself cannot grow the projection.
	maxSubagentParentDuplicateSources = 8
	// maxSubagentParentClaimKeyRunes bounds the subject half of a conclusion.
	maxSubagentParentClaimKeyRunes = 160
)

// subagentReportGroup is one surviving conclusion plus the children whose
// conclusion was folded into it.
type subagentReportGroup struct {
	report           SubagentResult
	duplicateSources []string
}

// groupSubagentReportsByConclusion folds reports whose conclusion fingerprint
// is identical into the first occurrence. Reports without a comparable
// conclusion stay as their own group, so nothing is lost.
func groupSubagentReportsByConclusion(reports []SubagentResult) ([]subagentReportGroup, int) {
	groups := make([]subagentReportGroup, 0, len(reports))
	position := make(map[string]int, len(reports))
	deduplicated := 0
	for _, report := range reports {
		fingerprint := subagentConclusionFingerprint(report)
		if fingerprint == "" {
			groups = append(groups, subagentReportGroup{report: report})
			continue
		}
		if existing, ok := position[fingerprint]; ok {
			groups[existing].duplicateSources = appendSubagentDuplicateSource(groups[existing].duplicateSources, report)
			deduplicated++
			continue
		}
		position[fingerprint] = len(groups)
		groups = append(groups, subagentReportGroup{report: report})
	}
	return groups, deduplicated
}

// detectSubagentReportConflicts cross-references groups that make the same
// claim (same subject) but reached different conclusions. Identical conclusions
// were already folded by dedup, so every remaining group under one claim key is
// a distinct conclusion and gets flagged with its siblings' sources. Returns the
// per-group sibling labels and how many groups were flagged.
func detectSubagentReportConflicts(groups []subagentReportGroup) (map[int][]string, int) {
	byClaim := make(map[string][]int, len(groups))
	claimOrder := make([]string, 0, len(groups))
	for index, group := range groups {
		key := subagentClaimKey(group.report.Summary)
		if key == "" {
			continue
		}
		if _, ok := byClaim[key]; !ok {
			claimOrder = append(claimOrder, key)
		}
		byClaim[key] = append(byClaim[key], index)
	}
	conflicts := make(map[int][]string)
	flagged := 0
	for _, key := range claimOrder {
		members := byClaim[key]
		if len(members) < 2 {
			continue
		}
		for _, index := range members {
			sources := make([]string, 0, len(members)-1)
			for _, other := range members {
				if other == index {
					continue
				}
				if label := subagentReportSourceLabel(groups[other].report); label != "" {
					sources = append(sources, label)
				}
			}
			if len(sources) == 0 {
				continue
			}
			conflicts[index] = sources
			flagged++
		}
	}
	return conflicts, flagged
}

// subagentConclusionFingerprint keys an exact-conclusion match. The success flag
// and error text are part of the key: two children with the same summary but a
// different outcome are not the same report. An empty result means "no
// conclusion worth comparing", and such reports are never merged.
func subagentConclusionFingerprint(report SubagentResult) string {
	normalized := normalizeSubagentConclusionText(report.Summary)
	if utf8.RuneCountInString(normalized) < minSubagentConclusionFingerprintRunes {
		return ""
	}
	payload := "success=" + strconv.FormatBool(report.Success) +
		"\nerror=" + normalizeSubagentConclusionText(report.Error) +
		"\nsummary=" + normalized
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", sum[:8])
}

// subagentClaimKey keys the subject half of a conclusion: the normalized first
// line/sentence. Two reports that share a claim key but not a fingerprint
// disagree about the same subject, which is a conflict rather than a duplicate.
func subagentClaimKey(summary string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(summary, "\r\n", "\n"))
	if trimmed == "" {
		return ""
	}
	if index := strings.IndexAny(trimmed, "\n。！？!?"); index >= 0 {
		trimmed = trimmed[:index]
	}
	normalized := normalizeSubagentConclusionText(trimmed)
	if utf8.RuneCountInString(normalized) < minSubagentConclusionFingerprintRunes {
		return ""
	}
	runes := []rune(normalized)
	if len(runes) > maxSubagentParentClaimKeyRunes {
		runes = runes[:maxSubagentParentClaimKeyRunes]
	}
	sum := sha256.Sum256([]byte(string(runes)))
	return fmt.Sprintf("%x", sum[:8])
}

// normalizeSubagentConclusionText lowercases and collapses whitespace and
// punctuation so two children that word the same conclusion slightly
// differently still compare equal at the exact-fingerprint level.
func normalizeSubagentConclusionText(value string) string {
	value = strings.ToLower(strings.ReplaceAll(value, "\r\n", "\n"))
	var builder strings.Builder
	pendingSpace := false
	for _, r := range value {
		switch {
		case unicode.IsSpace(r):
			pendingSpace = true
			continue
		case unicode.IsPunct(r):
			continue
		}
		if pendingSpace && builder.Len() > 0 {
			builder.WriteByte(' ')
		}
		pendingSpace = false
		builder.WriteRune(r)
	}
	return strings.TrimSpace(builder.String())
}

// subagentReportSourceLabel names one child for the duplicate/conflict lists.
func subagentReportSourceLabel(report SubagentResult) string {
	label := strings.TrimSpace(report.SessionID)
	if label == "" {
		label = strings.TrimSpace(report.ID)
	}
	return truncateSubagentParentText(label, 160)
}

// appendSubagentDuplicateSource records one folded child, de-duplicated by label
// and capped so the recovery list stays bounded.
func appendSubagentDuplicateSource(list []string, report SubagentResult) []string {
	label := subagentReportSourceLabel(report)
	if label == "" || len(list) >= maxSubagentParentDuplicateSources {
		return list
	}
	for _, existing := range list {
		if existing == label {
			return list
		}
	}
	return append(list, label)
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
	if len(report.ReadOnlyFilteredTools) > 0 {
		projection["read_only_filtered_tools"] = compactSubagentFilteredTools(report.ReadOnlyFilteredTools)
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
	if summaryTruncated {
		// The tail of the deliverable is reachable through the read tool's
		// offset/limit paging, so truncation here is a bounded default view
		// rather than a loss (H4).
		if action := subagentReportDereferenceAction(report); action != "" {
			projection["summary_next_action"] = action
		}
		projection["summary_runes"] = utf8.RuneCountInString(strings.TrimSpace(report.Summary))
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

// compactSubagentFilteredTools bounds the requested tools that a read-only child
// never received so the parent can see the narrowed allowlist without letting
// the projection grow unbounded.
func compactSubagentFilteredTools(values []string) []string {
	const maxTools = 12
	tools := make([]string, 0, minInt(len(values), maxTools))
	for _, value := range values {
		tool := truncateSubagentParentText(value, 64)
		if tool == "" {
			continue
		}
		tools = append(tools, tool)
		if len(tools) >= maxTools {
			break
		}
	}
	return tools
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
