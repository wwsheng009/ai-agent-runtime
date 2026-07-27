package toolresult

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

const (
	MetadataOKKey         = "ok"
	MetadataErrorCodeKey  = "error_code"
	MetadataRetryableKey  = "retryable"
	MetadataNextActionKey = "next_action"
	MetadataToolNameKey   = "tool_name"
	MetadataToolCallIDKey = "tool_call_id"
	// MetadataOutcomeKey is a model-facing success/failure disposition that is
	// richer than ok=true/false alone (empty success vs partial batch failure).
	MetadataOutcomeKey = "outcome"
	// MetadataEmptyResultKey marks a successful invocation that produced no
	// payload. Models should treat this as valid evidence, not as a hard fail.
	MetadataEmptyResultKey = "empty_result"
	// MetadataPathCandidatesKey carries nearby filesystem path suggestions for
	// missing-path recovery. Kept generic (no tool-name rewrites).
	MetadataPathCandidatesKey = "path_candidates"
	// MetadataAttemptedArgsKey carries a compact, redacted summary of the args
	// that produced an empty/failed result so the model can broaden queries.
	MetadataAttemptedArgsKey = "attempted_args"
	// MetadataRequestedCountKey is the generic batch size (requested items).
	MetadataRequestedCountKey = "requested_count"
	// MetadataFailedCountKey is the number of failed items in a batch.
	MetadataFailedCountKey = "failed_count"
	// MetadataSucceededCountKey is the number of successful items in a batch.
	MetadataSucceededCountKey = "succeeded_count"
	// MetadataPartialFailureKey marks a batch that finished with mixed results.
	MetadataPartialFailureKey = "partial_failure"
	// MetadataFailedItemsKey carries compact failed-item recovery rows
	// (index/path/error/ref). Generic: no tool-name branches; tools may publish
	// the key directly or leave enough structure for ExtractFailedItems.
	MetadataFailedItemsKey = "failed_items"

	OutcomeSuccess = "success"
	OutcomeEmpty   = "empty"
	OutcomePartial = "partial"
	OutcomeFailed  = "failed"
)

// Diagnostic is the stable execution contract attached to every tool result.
// OK describes the tool invocation itself; a successful status query may still
// report an underlying job error in its result payload and metadata.
type Diagnostic struct {
	OK         bool   `json:"ok"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	Retryable  bool   `json:"retryable,omitempty"`
	NextAction string `json:"next_action,omitempty"`
	// Outcome is the model-facing disposition: success | empty | partial | failed.
	Outcome string `json:"outcome,omitempty"`
	// EmptyResult is true when the tool invocation succeeded with no payload.
	EmptyResult bool `json:"empty_result,omitempty"`
	// PathCandidates are nearby path suggestions for missing-path recovery.
	PathCandidates []string `json:"path_candidates,omitempty"`
	// AttemptedArgs is a compact summary of the args that produced this result.
	AttemptedArgs map[string]interface{} `json:"attempted_args,omitempty"`
	// RequestedCount is the number of items requested in a multi-item call.
	RequestedCount int `json:"requested_count,omitempty"`
	// FailedCount is the number of failed items when outcome is partial/failed.
	FailedCount int `json:"failed_count,omitempty"`
	// SucceededCount is the number of successful items when known.
	SucceededCount int `json:"succeeded_count,omitempty"`
	// PartialFailure is true when a multi-item call mixed successes and failures.
	PartialFailure bool `json:"partial_failure,omitempty"`
	// FailedItems lists compact failed batch entries so the model can re-run only
	// those items. Prefer indexes/paths/errors already present in tool metadata.
	FailedItems []FailedItem `json:"failed_items,omitempty"`
	// FilePath is the primary target path for stale/path recovery when authored.
	FilePath string `json:"file_path,omitempty"`
	// SuggestedViewOffset is a 0-based view offset hint for STALE_CONTEXT recovery.
	SuggestedViewOffset *int `json:"suggested_view_offset,omitempty"`
	// SuggestedViewLimit is the recommended view window size with SuggestedViewOffset.
	SuggestedViewLimit *int `json:"suggested_view_limit,omitempty"`
	// CurrentSnippetStartLine is the 1-based line of current_snippet when present.
	CurrentSnippetStartLine *int `json:"current_snippet_start_line,omitempty"`
	// CurrentSnippet is the exact current file window for STALE recovery so the
	// model can rebuild old_string/@@ without an extra view when the contract is
	// the only durable structured surface (body error text may truncate).
	// Cap applied in attachRecoveryHints to keep model contracts bounded.
	CurrentSnippet string `json:"current_snippet,omitempty"`
}

// FailedItem is one failed entry from a multi-item tool invocation.
// Fields are optional and schema-agnostic; only non-empty fields are promoted.
type FailedItem struct {
	// Index is the 0-based batch position when known. Pointer so index 0 is
	// distinguishable from "unset" under json omitempty.
	Index *int   `json:"index,omitempty"`
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
	// Ref is a short human-readable target (command snippet, path, etc.).
	Ref string `json:"ref,omitempty"`
}

// Diagnose builds an actionable tool invocation diagnostic from the execution
// error and any structured metadata supplied by the tool runtime.
func Diagnose(toolName, toolCallID, toolErr string, metadata map[string]interface{}) Diagnostic {
	diagnostic := Diagnostic{
		OK:         strings.TrimSpace(toolErr) == "",
		ToolName:   strings.TrimSpace(toolName),
		ToolCallID: strings.TrimSpace(toolCallID),
	}
	if diagnostic.OK {
		enrichSuccessDiagnostic(&diagnostic, metadata)
		return diagnostic
	}

	structuredCode := strings.TrimSpace(diagnosticString(metadata, MetadataErrorCodeKey))
	messageCode := classifyToolErrorCode(toolErr)
	// Prefer tool-authored structured codes, but refine generic TOOL_EXECUTION
	// when message / failure_class evidence yields a more specific recovery code
	// (historical chat-logs and partial promotion paths often stamp TOOL_EXECUTION
	// while the body is clearly STALE_CONTEXT / PATH_NOT_FOUND / …).
	// Also correct a small class of mislabels where edit/apply_patch STALE bodies
	// were stamped TIMEOUT/PATH because closest snippets mention those words.
	switch {
	case knownRuntimeErrorCode(structuredCode) && !isGenericToolExecutionCode(structuredCode):
		if refined := refineMislabeledStructuredCode(diagnostic.ToolName, structuredCode, messageCode, metadata, toolErr); refined != "" {
			diagnostic.ErrorCode = refined
		} else {
			diagnostic.ErrorCode = structuredCode
		}
	case knownRuntimeErrorCode(structuredCode) && isGenericToolExecutionCode(structuredCode):
		if refined := refineGenericToolExecutionCode(structuredCode, messageCode, metadata, toolErr); refined != "" {
			diagnostic.ErrorCode = refined
		} else {
			diagnostic.ErrorCode = structuredCode
		}
	default:
		if refined := refineGenericToolExecutionCode("", messageCode, metadata, toolErr); refined != "" {
			diagnostic.ErrorCode = refined
		} else {
			diagnostic.ErrorCode = messageCode
		}
	}
	refinedFromStructured := knownRuntimeErrorCode(structuredCode) &&
		diagnostic.ErrorCode != "" &&
		diagnostic.ErrorCode != structuredCode
	if retryable, ok := diagnosticBool(metadata, MetadataRetryableKey); ok {
		// Authored retryable wins, except when a more specific non-retryable
		// recovery code was refined from a generic TOOL_EXECUTION stamp or a
		// mislabeled TIMEOUT/PATH was corrected to STALE_CONTEXT.
		if (isGenericToolExecutionCode(structuredCode) || refinedFromStructured) &&
			!isGenericToolExecutionCode(diagnostic.ErrorCode) &&
			!retryableToolErrorCode(diagnostic.ErrorCode) {
			diagnostic.Retryable = false
		} else {
			diagnostic.Retryable = retryable
		}
	} else {
		diagnostic.Retryable = retryableToolErrorCode(diagnostic.ErrorCode)
	}
	// Prefer metadata-authored next_action; otherwise derive from error code and
	// message patterns (shell/exit failures need message-aware recovery text).
	// Prefer top-level, then nested tool_metadata (pre-promotion / historical payloads).
	// When we refined a generic TOOL_EXECUTION into a specific recovery code,
	// ignore generic default next_action so models get STALE_CONTEXT guidance.
	// Same for mislabeled TIMEOUT/PATH that we corrected to STALE.
	authoredNext := strings.TrimSpace(diagnosticString(metadata, MetadataNextActionKey))
	explicitNext := authoredNext != "" && !isGenericDefaultNextAction(authoredNext)
	if refinedFromStructured {
		// Wrong recovery class next_action (timeout/path) must not stick after refine.
		if isMismatchedRecoveryNextAction(authoredNext, diagnostic.ErrorCode) {
			explicitNext = false
		}
	}
	if explicitNext {
		diagnostic.NextAction = authoredNext
	} else {
		diagnostic.NextAction = nextActionForToolError(diagnostic.ErrorCode, toolErr)
	}
	attachBatchStats(&diagnostic, metadata)
	// Partial batch guidance replaces generic actions when mixed results are
	// detected and the caller did not author an explicit next_action.
	if applyPartialOutcome(&diagnostic, metadata) && !explicitNext {
		requested := diagnostic.RequestedCount
		failed := diagnostic.FailedCount
		if requested > 0 && failed > 0 {
			if len(diagnostic.FailedItems) == 0 {
				diagnostic.FailedItems = ExtractFailedItems(metadata)
			}
			diagnostic.NextAction = nextActionForPartialBatchItems(failed, requested, diagnostic.FailedItems)
		}
	}
	if diagnostic.Outcome == "" {
		diagnostic.Outcome = OutcomeFailed
	}
	attachRecoveryHints(&diagnostic, metadata, toolErr)
	return diagnostic
}

func enrichSuccessDiagnostic(diagnostic *Diagnostic, metadata map[string]interface{}) {
	if diagnostic == nil {
		return
	}
	attachBatchStats(diagnostic, metadata)
	if empty, ok := diagnosticBool(metadata, MetadataEmptyResultKey); ok && empty {
		diagnostic.EmptyResult = true
	}
	if outcome := strings.TrimSpace(diagnosticTopLevelString(metadata, MetadataOutcomeKey)); outcome != "" {
		diagnostic.Outcome = NormalizeOutcome(outcome)
	}
	// Multi-item tools may finish with mixed results while still returning a
	// non-empty body and no top-level toolErr (e.g. batch file reads). Surface
	// that as outcome=partial so the model reuses successes and repairs only
	// the failed items instead of treating the call as fully successful.
	if diagnostic.Outcome == "" || diagnostic.Outcome == OutcomeSuccess {
		applyPartialOutcome(diagnostic, metadata)
	}
	if diagnostic.Outcome == "" {
		if diagnostic.EmptyResult {
			diagnostic.Outcome = OutcomeEmpty
		} else {
			diagnostic.Outcome = OutcomeSuccess
		}
	}
	if diagnostic.Outcome == OutcomeEmpty {
		diagnostic.EmptyResult = true
		// Empty disposition should not also claim partial batch failure.
		if diagnostic.PartialFailure && diagnostic.FailedCount == 0 {
			diagnostic.PartialFailure = false
		}
		// Surface next_action on the diagnostic itself so gateway envelope fields
		// and model contracts stay consistent (not only top-level metadata).
		if strings.TrimSpace(diagnostic.NextAction) == "" {
			explicit := strings.TrimSpace(diagnosticTopLevelString(metadata, MetadataNextActionKey))
			diagnostic.NextAction = nextActionForEmptyResult(explicit, ExtractAttemptedArgs(metadata))
		}
	}
	// Only attach recovery hints for dispositions that benefit model recovery
	// (empty/partial). Ordinary success stays compact.
	if diagnostic.EmptyResult || diagnostic.Outcome == OutcomeEmpty || diagnostic.Outcome == OutcomePartial {
		attachRecoveryHints(diagnostic, metadata, "")
		// Refresh partial next_action once failed_items are known so success-path
		// mixed batches also get item-aware guidance (not only count-only text).
		if diagnostic.Outcome == OutcomePartial &&
			strings.TrimSpace(diagnosticTopLevelString(metadata, MetadataNextActionKey)) == "" &&
			diagnostic.RequestedCount > 0 && diagnostic.FailedCount > 0 && len(diagnostic.FailedItems) > 0 {
			if !strings.Contains(strings.ToLower(diagnostic.NextAction), "failed items:") {
				diagnostic.NextAction = nextActionForPartialBatchItems(diagnostic.FailedCount, diagnostic.RequestedCount, diagnostic.FailedItems)
			}
		}
	}
}

func attachRecoveryHints(diagnostic *Diagnostic, metadata map[string]interface{}, toolErr string) {
	if diagnostic == nil {
		return
	}
	if candidates := ExtractPathCandidates(metadata); len(candidates) > 0 {
		diagnostic.PathCandidates = candidates
	}
	if args := ExtractAttemptedArgs(metadata); len(args) > 0 {
		diagnostic.AttemptedArgs = args
	}
	if items := ExtractFailedItems(metadata); len(items) > 0 {
		diagnostic.FailedItems = items
	}
	if path := strings.TrimSpace(diagnosticString(metadata, "file_path")); path != "" {
		diagnostic.FilePath = path
	}
	if offset, ok := diagnosticIntPtr(metadata, "suggested_view_offset"); ok {
		diagnostic.SuggestedViewOffset = offset
	}
	if limit, ok := diagnosticIntPtr(metadata, "suggested_view_limit"); ok {
		diagnostic.SuggestedViewLimit = limit
	}
	if start, ok := diagnosticIntPtr(metadata, "current_snippet_start_line"); ok {
		diagnostic.CurrentSnippetStartLine = start
	}
	if snippet := diagnosticMultilineString(metadata, "current_snippet"); snippet != "" {
		diagnostic.CurrentSnippet = capCurrentSnippetForContract(snippet)
	}
	// Prefer toolErr body, then metadata error_message (historical exports).
	errBody := strings.TrimSpace(toolErr)
	if errBody == "" {
		errBody = diagnosticString(metadata, "error_message")
	}
	// Historical / partial-promotion chat-logs often only embed closest lines in
	// the error body ("最接近片段" / multi-line "最接近的当前内容"). Recover them so
	// model contracts and tool.completed export still get current_snippet.
	if strings.TrimSpace(diagnostic.CurrentSnippet) == "" {
		if parsed, startLine := parseClosestSnippetFromErrorMessage(errBody); parsed != "" {
			diagnostic.CurrentSnippet = capCurrentSnippetForContract(parsed)
			if diagnostic.CurrentSnippetStartLine == nil && startLine > 0 {
				v := startLine
				diagnostic.CurrentSnippetStartLine = &v
			}
			if diagnostic.SuggestedViewOffset == nil && startLine > 0 {
				off := startLine - 1
				diagnostic.SuggestedViewOffset = &off
			}
		}
	}
	if diagnostic.SuggestedViewOffset == nil {
		if off, ok := parseSuggestedViewOffsetFromMessage(errBody); ok {
			diagnostic.SuggestedViewOffset = &off
			if diagnostic.SuggestedViewLimit == nil {
				lim := 40
				diagnostic.SuggestedViewLimit = &lim
			}
		}
	}
}

// parseClosestSnippetFromErrorMessage recovers copy-pasteable current lines from
// edit/apply_patch STALE error bodies when structured current_snippet is missing.
// Returns (snippet, startLine1Based). startLine is 0 when unknown.
func parseClosestSnippetFromErrorMessage(message string) (string, int) {
	message = strings.ReplaceAll(message, "\r\n", "\n")
	message = strings.ReplaceAll(message, "\r", "\n")
	if strings.TrimSpace(message) == "" {
		return "", 0
	}

	// Multi-line header only: 最接近的当前内容（第 N 行附近…）:\n  12|code
	// Reject prose mentions such as 按返回的“最接近的当前内容”重建补丁 — those lack the
	// （第 … header form and would otherwise swallow 期望内容 as a false snippet.
	const multiMarker = "最接近的当前内容（第"
	if idx := strings.Index(message, multiMarker); idx >= 0 {
		rest := message[idx:]
		// Optional start line from header.
		startLine := 0
		if open := strings.Index(rest, "第 "); open >= 0 {
			numPart := rest[open+len("第 "):]
			end := strings.Index(numPart, " 行")
			if end > 0 {
				startLine = parseLeadingInt(strings.TrimSpace(numPart[:end]))
			}
		}
		// Body after first newline following the header line.
		body := rest
		if nl := strings.Index(rest, "\n"); nl >= 0 {
			body = rest[nl+1:]
		} else {
			// Header without a following block is not usable.
			body = ""
		}
		// Stop before next_action / old_string preview / 期望内容 trailers.
		for _, stop := range []string{"\nnext_action:", "\nold_string 预览:", "\n期望内容:", "\n建议从第 "} {
			if cut := strings.Index(body, stop); cut >= 0 {
				body = body[:cut]
			}
		}
		lines := strings.Split(body, "\n")
		out := make([]string, 0, len(lines))
		firstLineNo := 0
		numbered := 0
		for _, ln := range lines {
			// Skip empty header residue / ellipsis-only tails.
			if strings.TrimSpace(ln) == "" {
				if len(out) > 0 {
					out = append(out, "")
				}
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(ln), "...") {
				break
			}
			// "  12|text" line-number prefix from formatEditClosestLines / formatPatchCurrentLines.
			if pipe := strings.Index(ln, "|"); pipe > 0 {
				prefix := strings.TrimSpace(ln[:pipe])
				if isAllDigits(prefix) {
					if firstLineNo == 0 {
						firstLineNo = parseLeadingInt(prefix)
					}
					numbered++
					out = append(out, ln[pipe+1:])
					continue
				}
			}
			// formatPatchCurrentLines uses "12: text" style.
			if colon := strings.Index(ln, ": "); colon > 0 {
				prefix := strings.TrimSpace(ln[:colon])
				if isAllDigits(prefix) {
					if firstLineNo == 0 {
						firstLineNo = parseLeadingInt(prefix)
					}
					numbered++
					out = append(out, ln[colon+2:])
					continue
				}
			}
			// Stop if we hit prose that is clearly not code block body.
			if strings.HasPrefix(ln, "next_action:") || strings.HasPrefix(ln, "old_string") || strings.HasPrefix(ln, "期望内容") {
				break
			}
			// Only keep non-numbered lines once we have seen at least one numbered
			// code line; otherwise raw prose / 期望内容 would pollute the snippet.
			if numbered > 0 {
				out = append(out, ln)
			}
		}
		// Trim trailing empties.
		for len(out) > 0 && out[len(out)-1] == "" {
			out = out[:len(out)-1]
		}
		// Require at least one numbered line so prose false-positives never win.
		if len(out) > 0 && numbered > 0 {
			if startLine <= 0 {
				startLine = firstLineNo
			}
			return strings.Join(out, "\n"), startLine
		}
	}

	// Quoted single-fragment form: 最接近片段: "...."
	// Historical binaries often mid-line-truncated the %q payload (no closing
	// quote before next_action / EOF). Still recover the partial snippet so
	// offline dashboards / model contracts get *some* copy-paste signal.
	const quoteMarker = "最接近片段:"
	if idx := strings.Index(message, quoteMarker); idx >= 0 {
		rest := strings.TrimSpace(message[idx+len(quoteMarker):])
		if strings.HasPrefix(rest, "\"") {
			rest = rest[1:]
			// Find closing quote; content may contain escaped quotes.
			end := -1
			escaped := false
			for i := 0; i < len(rest); i++ {
				if escaped {
					escaped = false
					continue
				}
				if rest[i] == '\\' {
					escaped = true
					continue
				}
				if rest[i] == '"' {
					end = i
					break
				}
			}
			raw := ""
			if end >= 0 {
				raw = rest[:end]
			} else {
				// Truncated historical body: take until trailer keywords / EOL run.
				cut := len(rest)
				for _, stop := range []string{" next_action:", "\nnext_action:", " old_string", "\nold_string", " 建议从第", "\n建议从第"} {
					if i := strings.Index(rest, stop); i >= 0 && i < cut {
						cut = i
					}
				}
				raw = rest[:cut]
				// Drop a dangling incomplete escape at the cut boundary.
				raw = strings.TrimRight(raw, `\`)
			}
			// Unescape common Go %q sequences used in live errors.
			raw = strings.ReplaceAll(raw, `\\`, `\`)
			raw = strings.ReplaceAll(raw, `\n`, "\n")
			raw = strings.ReplaceAll(raw, `\t`, "\t")
			raw = strings.ReplaceAll(raw, `\"`, `"`)
			if strings.TrimSpace(raw) != "" {
				return raw, 0
			}
		}
	}
	return "", 0
}

// parseSuggestedViewOffsetFromMessage extracts 0-based view offset from STALE
// error prose (suggested_view_offset=N or 建议从第 N 行).
func parseSuggestedViewOffsetFromMessage(message string) (int, bool) {
	message = strings.TrimSpace(message)
	if message == "" {
		return 0, false
	}
	const key = "suggested_view_offset="
	if idx := strings.Index(message, key); idx >= 0 {
		n := parseLeadingInt(strings.TrimSpace(message[idx+len(key):]))
		if n >= 0 {
			// Accept 0 as valid first-line offset.
			rest := strings.TrimSpace(message[idx+len(key):])
			if rest != "" && (rest[0] >= '0' && rest[0] <= '9') {
				return n, true
			}
		}
	}
	const suggest = "建议从第 "
	if idx := strings.Index(message, suggest); idx >= 0 {
		n := parseLeadingInt(strings.TrimSpace(message[idx+len(suggest):]))
		if n > 0 {
			return n - 1, true
		}
	}
	return 0, false
}

func parseLeadingInt(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n := 0
	found := false
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		found = true
		n = n*10 + int(r-'0')
	}
	if !found {
		return 0
	}
	return n
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// maxCurrentSnippetContractBytes bounds model-visible current_snippet so STALE
// recovery stays copy-pasteable without blowing the tool_result contract budget.
// ~4KiB covers ~16 full code lines with generous indent; larger windows still
// have suggested_view_offset + body error block / chat-log metadata.
const maxCurrentSnippetContractBytes = 4 * 1024

func capCurrentSnippetForContract(snippet string) string {
	snippet = strings.ReplaceAll(snippet, "\r\n", "\n")
	snippet = strings.ReplaceAll(snippet, "\r", "\n")
	if snippet == "" {
		return ""
	}
	// Prefer whole lines under the byte budget so models never copy a mid-line cut.
	if len(snippet) <= maxCurrentSnippetContractBytes {
		return snippet
	}
	lines := strings.Split(snippet, "\n")
	var b strings.Builder
	b.Grow(maxCurrentSnippetContractBytes)
	for i, line := range lines {
		candidate := line
		if i > 0 {
			candidate = "\n" + line
		}
		if b.Len()+len(candidate) > maxCurrentSnippetContractBytes {
			if b.Len() == 0 {
				// Single oversized line: hard cut with ellipsis marker.
				cut := maxCurrentSnippetContractBytes
				if cut > 3 {
					cut -= 3
				}
				return snippet[:cut] + "..."
			}
			break
		}
		b.WriteString(candidate)
	}
	return b.String()
}

// diagnosticMultilineString reads a metadata string without TrimSpace so leading
// indent / trailing blank lines on code snippets stay exact for copy-paste.
func diagnosticMultilineString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	if value, ok := metadata[key].(string); ok && value != "" {
		return value
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if value, ok := nested[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

// NormalizeOutcome maps free-form outcome labels onto the stable contract.
func NormalizeOutcome(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case OutcomeSuccess, "ok", "succeeded":
		return OutcomeSuccess
	case OutcomeEmpty, "empty_success", "no_output", "no_matches":
		return OutcomeEmpty
	case OutcomePartial, "partial_success", "partial_failure":
		return OutcomePartial
	case OutcomeFailed, "error", "failure":
		return OutcomeFailed
	default:
		return ""
	}
}

// MarkEmptySuccess stamps a successful no-payload / no-match disposition onto
// metadata. Tools should call this only when they have clear evidence of empty
// success (e.g. match_count==0, returned_count==0), never for real failures.
// Existing partial/failed outcomes and mutation proofs are left untouched.
func MarkEmptySuccess(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	// Do not downgrade mixed/failed batch dispositions or mutation proofs.
	if outcome := NormalizeOutcome(diagnosticTopLevelString(metadata, MetadataOutcomeKey)); outcome == OutcomePartial || outcome == OutcomeFailed {
		return metadata
	}
	if partial, ok := diagnosticBool(metadata, MetadataPartialFailureKey); ok && partial {
		return metadata
	}
	if stats := ExtractBatchStats(metadata); stats.Failed > 0 {
		return metadata
	}
	if strings.TrimSpace(MutationSummary(metadata)) != "" {
		return metadata
	}
	metadata[MetadataEmptyResultKey] = true
	if outcome := NormalizeOutcome(diagnosticTopLevelString(metadata, MetadataOutcomeKey)); outcome == "" || outcome == OutcomeSuccess {
		metadata[MetadataOutcomeKey] = OutcomeEmpty
	}
	return metadata
}

// HasEmptySuccessEvidence reports whether metadata already carries a clear
// empty-success signal (explicit empty_result / outcome=empty, or a zero
// result-count field with no failure/mutation proof). Generic: no tool names.
func HasEmptySuccessEvidence(metadata map[string]interface{}) bool {
	if len(metadata) == 0 {
		return false
	}
	// Hard exclusions first: mutation proofs and failure/partial dispositions
	// never count as empty success, even if a stale empty_result flag is set.
	if strings.TrimSpace(MutationSummary(metadata)) != "" {
		return false
	}
	if outcome := NormalizeOutcome(diagnosticTopLevelString(metadata, MetadataOutcomeKey)); outcome == OutcomePartial || outcome == OutcomeFailed {
		return false
	}
	if partial, ok := diagnosticBool(metadata, MetadataPartialFailureKey); ok && partial {
		return false
	}
	if stats := ExtractBatchStats(metadata); stats.Failed > 0 {
		return false
	}
	if empty, ok := diagnosticBool(metadata, MetadataEmptyResultKey); ok && empty {
		return true
	}
	if NormalizeOutcome(diagnosticTopLevelString(metadata, MetadataOutcomeKey)) == OutcomeEmpty {
		return true
	}
	// Prefer explicit result-count keys. Presence with value 0 is positive
	// evidence; absence is not treated as empty.
	for _, key := range []string{"match_count", "returned_count", "result_count", "hit_count"} {
		if _, present := metadata[key]; !present {
			continue
		}
		if diagnosticInt(metadata, key) == 0 {
			return true
		}
	}
	// "count" alone is ambiguous (could be request size). Only treat as empty
	// when a companion files/results slice is present and empty.
	if _, present := metadata["count"]; present && diagnosticInt(metadata, "count") == 0 {
		if files, ok := metadata["files"]; ok {
			if stringSliceFromAny(files) != nil || isEmptySliceValue(files) {
				if len(stringSliceFromAny(files)) == 0 {
					return true
				}
			}
		}
		if results, ok := metadata["results"]; ok {
			if stringSliceFromAny(results) != nil || isEmptySliceValue(results) {
				if len(stringSliceFromAny(results)) == 0 {
					return true
				}
			}
		}
		if matches, ok := metadata["matches"]; ok {
			if stringSliceFromAny(matches) != nil || isEmptySliceValue(matches) {
				if len(stringSliceFromAny(matches)) == 0 {
					return true
				}
			}
		}
	}
	return false
}

func isEmptySliceValue(value interface{}) bool {
	switch typed := value.(type) {
	case []string:
		return len(typed) == 0
	case []interface{}:
		return len(typed) == 0
	default:
		return false
	}
}

// DefaultEmptyResultNextAction is the stable recovery guidance for successful
// empty tool results (no matches / no output). Models should treat empty as
// valid evidence rather than a hard failure.
const DefaultEmptyResultNextAction = "Empty successful result is valid evidence; do not treat it as a hard failure or retry unchanged. Broaden the query, change inputs, or proceed with the next task step."

func nextActionForEmptyResult(existing string, attemptedArgs map[string]interface{}) string {
	base := strings.TrimSpace(existing)
	if base == "" {
		base = DefaultEmptyResultNextAction
	}
	// When compact attempted_args are known, append a short arg-aware hint so
	// the model can broaden without replaying identical inputs. Keep this
	// generic (keys only; no tool-name branches).
	if len(attemptedArgs) == 0 {
		return base
	}
	keys := make([]string, 0, len(attemptedArgs))
	for key := range attemptedArgs {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return base
	}
	sort.Strings(keys)
	// Bound noise if many keys were attempted.
	if len(keys) > 6 {
		keys = keys[:6]
	}
	hint := fmt.Sprintf(" Previous args used keys [%s]; change or broaden those inputs before retrying.", strings.Join(keys, ", "))
	if strings.Contains(strings.ToLower(base), "previous args used keys") {
		return base
	}
	return base + hint
}

// NextActionForPartialBatch builds model-facing recovery guidance for mixed
// multi-item results. Optional failed items append a compact target list.
func NextActionForPartialBatch(failed, requested int, items []FailedItem) string {
	return nextActionForPartialBatchItems(failed, requested, items)
}

func nextActionForPartialBatch(failed, requested int) string {
	return NextActionForPartialBatch(failed, requested, nil)
}

func nextActionForPartialBatchItems(failed, requested int, items []FailedItem) string {
	base := fmt.Sprintf(
		"Batch finished with %d/%d item failure(s). Reuse successful item outputs; fix or re-run only the failed items with corrected inputs. Do not re-run the entire batch unchanged.",
		failed,
		requested,
	)
	summary := summarizeFailedItems(items)
	if summary == "" {
		return base
	}
	// Keep next_action bounded: one compact clause of failed targets.
	return base + " Failed items: " + summary + "."
}

func summarizeFailedItems(items []FailedItem) string {
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		label := failedItemLabel(item)
		if label == "" {
			continue
		}
		parts = append(parts, label)
		if len(parts) >= failedItemsMaxSummary {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if len(items) > len(parts) {
		return strings.Join(parts, "; ") + fmt.Sprintf("; …(+%d)", len(items)-len(parts))
	}
	return strings.Join(parts, "; ")
}

// BatchStats captures multi-item execution counts from generic metadata keys.
// Tools may publish requested_count or request_count, and either succeeded_count
// or executed_count-failed_count. No tool-name switches.
type BatchStats struct {
	Requested int
	Failed    int
	Succeeded int
	IsBatch   bool
	Partial   bool
}

// ExtractBatchStats reads multi-item counts from tool metadata. It is tolerant of
// common key aliases used by batch-capable tools.
func ExtractBatchStats(metadata map[string]interface{}) BatchStats {
	stats := BatchStats{}
	if len(metadata) == 0 {
		return stats
	}
	if batch, ok := diagnosticBool(metadata, "batch"); ok && batch {
		stats.IsBatch = true
	}
	if partial, ok := diagnosticBool(metadata, MetadataPartialFailureKey); ok && partial {
		stats.Partial = true
		stats.IsBatch = true
	}
	stats.Failed = firstPositiveDiagnosticInt(metadata, MetadataFailedCountKey, "failures")
	stats.Succeeded = firstPositiveDiagnosticInt(metadata, MetadataSucceededCountKey)
	stats.Requested = firstPositiveDiagnosticInt(metadata, MetadataRequestedCountKey, "request_count")
	if stats.Requested == 0 {
		// Some batch tools publish executed_count instead of requested_count.
		if executed := diagnosticInt(metadata, "executed_count"); executed > 0 {
			stats.Requested = executed
		}
	}
	if stats.Succeeded == 0 && stats.Requested > 0 && stats.Failed >= 0 && stats.Failed < stats.Requested {
		// Infer succeeded when only requested/failed are known.
		if stats.Failed > 0 || stats.Partial || stats.IsBatch {
			stats.Succeeded = stats.Requested - stats.Failed
		}
	}
	if stats.Requested == 0 && (stats.Succeeded > 0 || stats.Failed > 0) {
		stats.Requested = stats.Succeeded + stats.Failed
	}
	if !stats.Partial && stats.Failed > 0 && stats.Succeeded > 0 {
		stats.Partial = true
	}
	if stats.Partial || stats.Failed > 0 || stats.Succeeded > 0 || stats.Requested > 1 {
		stats.IsBatch = true
	}
	return stats
}

func attachBatchStats(diagnostic *Diagnostic, metadata map[string]interface{}) {
	if diagnostic == nil {
		return
	}
	stats := ExtractBatchStats(metadata)
	if stats.Requested > 0 {
		diagnostic.RequestedCount = stats.Requested
	}
	if stats.Failed > 0 {
		diagnostic.FailedCount = stats.Failed
	}
	if stats.Succeeded > 0 {
		diagnostic.SucceededCount = stats.Succeeded
	}
	diagnostic.PartialFailure = stats.Partial
}

// applyPartialOutcome sets outcome=partial when metadata indicates mixed results.
// Returns true when partial was applied.
func applyPartialOutcome(diagnostic *Diagnostic, metadata map[string]interface{}) bool {
	if diagnostic == nil {
		return false
	}
	stats := ExtractBatchStats(metadata)
	if stats.Failed <= 0 {
		return false
	}
	// Need evidence of at least one success, or requested > failed.
	if stats.Succeeded <= 0 && !(stats.Requested > stats.Failed) {
		return false
	}
	// Prefer explicit batch/partial markers, but mixed counts alone are enough.
	if !(stats.IsBatch || stats.Partial || stats.Succeeded > 0) {
		return false
	}
	diagnostic.Outcome = OutcomePartial
	diagnostic.PartialFailure = true
	if diagnostic.RequestedCount == 0 && stats.Requested > 0 {
		diagnostic.RequestedCount = stats.Requested
	}
	if diagnostic.FailedCount == 0 && stats.Failed > 0 {
		diagnostic.FailedCount = stats.Failed
	}
	if diagnostic.SucceededCount == 0 && stats.Succeeded > 0 {
		diagnostic.SucceededCount = stats.Succeeded
	}
	if len(diagnostic.FailedItems) == 0 {
		diagnostic.FailedItems = ExtractFailedItems(metadata)
	}
	if strings.TrimSpace(diagnostic.NextAction) == "" {
		requested := diagnostic.RequestedCount
		if requested <= 0 {
			requested = stats.Requested
		}
		failed := diagnostic.FailedCount
		if failed <= 0 {
			failed = stats.Failed
		}
		diagnostic.NextAction = nextActionForPartialBatchItems(failed, requested, diagnostic.FailedItems)
	}
	return true
}

func firstPositiveDiagnosticInt(metadata map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		if value := diagnosticInt(metadata, key); value > 0 {
			return value
		}
	}
	return 0
}

// EnsureAttemptedArgs promotes or injects a compact attempted_args summary into
// metadata. Existing top-level or nested summaries win; otherwise fallbackArgs
// (typically the raw tool call args) are compacted and stored. This is
// schema-agnostic and never branches on tool names.
func EnsureAttemptedArgs(metadata map[string]interface{}, fallbackArgs map[string]interface{}) map[string]interface{} {
	if existing := ExtractAttemptedArgs(metadata); len(existing) > 0 {
		if metadata != nil {
			if _, ok := metadata[MetadataAttemptedArgsKey]; !ok {
				// Promote nested summaries (tool_invocation / tool_metadata) so
				// model contracts and event consumers see a stable top-level key.
				metadata[MetadataAttemptedArgsKey] = cloneAttemptedArgs(existing)
			}
		}
		return existing
	}
	compact := CompactAttemptedArgs(fallbackArgs)
	if len(compact) == 0 {
		return nil
	}
	if metadata != nil {
		metadata[MetadataAttemptedArgsKey] = compact
	}
	return compact
}

// ApplyDiagnosticMetadata promotes the invocation diagnostic to the top-level
// envelope metadata consumed by events, observations, persistence, and UIs.
func ApplyDiagnosticMetadata(metadata map[string]interface{}, diagnostic Diagnostic) {
	if metadata == nil {
		return
	}
	metadata[MetadataOKKey] = diagnostic.OK
	if outcome := strings.TrimSpace(diagnostic.Outcome); outcome != "" {
		metadata[MetadataOutcomeKey] = outcome
	}
	if diagnostic.ToolName != "" {
		metadata[MetadataToolNameKey] = diagnostic.ToolName
	}
	if diagnostic.ToolCallID != "" {
		metadata[MetadataToolCallIDKey] = diagnostic.ToolCallID
	}
	if len(diagnostic.PathCandidates) > 0 {
		metadata[MetadataPathCandidatesKey] = append([]string(nil), diagnostic.PathCandidates...)
	}
	// Prefer diagnostic AttemptedArgs; otherwise promote nested summaries when
	// the disposition is recovery-relevant (empty/partial/failed). Ordinary
	// success stays free of attempted_args unless already present on diagnostic.
	if len(diagnostic.AttemptedArgs) > 0 {
		metadata[MetadataAttemptedArgsKey] = cloneAttemptedArgs(diagnostic.AttemptedArgs)
	} else if !diagnostic.OK || diagnostic.EmptyResult || diagnostic.Outcome == OutcomeEmpty || diagnostic.Outcome == OutcomePartial {
		if existing := ExtractAttemptedArgs(metadata); len(existing) > 0 {
			metadata[MetadataAttemptedArgsKey] = cloneAttemptedArgs(existing)
		}
	}
	if diagnostic.RequestedCount > 0 {
		metadata[MetadataRequestedCountKey] = diagnostic.RequestedCount
	}
	if diagnostic.FailedCount > 0 {
		metadata[MetadataFailedCountKey] = diagnostic.FailedCount
	}
	if diagnostic.SucceededCount > 0 {
		metadata[MetadataSucceededCountKey] = diagnostic.SucceededCount
	}
	if diagnostic.PartialFailure || diagnostic.Outcome == OutcomePartial {
		metadata[MetadataPartialFailureKey] = true
	}
	if len(diagnostic.FailedItems) > 0 {
		metadata[MetadataFailedItemsKey] = failedItemsToMaps(diagnostic.FailedItems)
	} else if diagnostic.Outcome == OutcomePartial || diagnostic.PartialFailure || (!diagnostic.OK && diagnostic.FailedCount > 0) {
		if items := ExtractFailedItems(metadata); len(items) > 0 {
			metadata[MetadataFailedItemsKey] = failedItemsToMaps(items)
		}
	}
	if diagnostic.OK {
		if diagnostic.EmptyResult || diagnostic.Outcome == OutcomeEmpty {
			metadata[MetadataEmptyResultKey] = true
			existing := strings.TrimSpace(diagnosticTopLevelString(metadata, MetadataNextActionKey))
			if existing == "" {
				if strings.TrimSpace(diagnostic.NextAction) != "" {
					existing = diagnostic.NextAction
				}
				metadata[MetadataNextActionKey] = nextActionForEmptyResult(existing, diagnostic.AttemptedArgs)
			} else if strings.TrimSpace(diagnostic.NextAction) != "" {
				// Prefer the richer diagnostic guidance when metadata only has a stub.
				metadata[MetadataNextActionKey] = diagnostic.NextAction
			}
		}
		if diagnostic.Outcome == OutcomePartial {
			existing := strings.TrimSpace(diagnosticTopLevelString(metadata, MetadataNextActionKey))
			if existing == "" && strings.TrimSpace(diagnostic.NextAction) != "" {
				metadata[MetadataNextActionKey] = diagnostic.NextAction
			} else if strings.TrimSpace(diagnostic.NextAction) != "" && len(diagnostic.NextAction) > len(existing) {
				// Prefer item-aware partial guidance when it is richer.
				metadata[MetadataNextActionKey] = diagnostic.NextAction
			}
		}
		return
	}
	metadata[MetadataErrorCodeKey] = diagnostic.ErrorCode
	metadata[MetadataRetryableKey] = diagnostic.Retryable
	metadata[MetadataNextActionKey] = diagnostic.NextAction
	// STALE / path recovery fields must land on top-level metadata so chat-log
	// export, model contracts, and offline dashboards can read them without
	// digging nested tool_metadata or re-parsing error text.
	promoteStaleRecoveryFields(metadata, diagnostic)
}

// promoteStaleRecoveryFields writes file/snippet/view recovery hints from the
// diagnostic onto metadata when absent. Never overwrites tool-authored values.
func promoteStaleRecoveryFields(metadata map[string]interface{}, diagnostic Diagnostic) {
	if metadata == nil {
		return
	}
	if path := strings.TrimSpace(diagnostic.FilePath); path != "" {
		if _, exists := metadata["file_path"]; !exists {
			metadata["file_path"] = path
		}
	}
	if diagnostic.SuggestedViewOffset != nil {
		if _, exists := metadata["suggested_view_offset"]; !exists {
			metadata["suggested_view_offset"] = *diagnostic.SuggestedViewOffset
		}
	}
	if diagnostic.SuggestedViewLimit != nil {
		if _, exists := metadata["suggested_view_limit"]; !exists {
			metadata["suggested_view_limit"] = *diagnostic.SuggestedViewLimit
		}
	}
	if snippet := diagnostic.CurrentSnippet; snippet != "" {
		// Keep exact bytes (indent) — do not TrimSpace.
		if existing, _ := metadata["current_snippet"].(string); existing == "" {
			metadata["current_snippet"] = snippet
		}
	}
	if diagnostic.CurrentSnippetStartLine != nil {
		if _, exists := metadata["current_snippet_start_line"]; !exists {
			metadata["current_snippet_start_line"] = *diagnostic.CurrentSnippetStartLine
		}
	}
}

// ExtractPathCandidates returns nearby path suggestions from metadata when present.
func ExtractPathCandidates(metadata map[string]interface{}) []string {
	if len(metadata) == 0 {
		return nil
	}
	if values := stringSliceFromAny(metadata[MetadataPathCandidatesKey]); len(values) > 0 {
		return values
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if values := stringSliceFromAny(nested[MetadataPathCandidatesKey]); len(values) > 0 {
			return values
		}
	}
	if invocation, ok := metadata["tool_invocation"].(map[string]interface{}); ok {
		if values := stringSliceFromAny(invocation[MetadataPathCandidatesKey]); len(values) > 0 {
			return values
		}
	}
	return nil
}

// ExtractAttemptedArgs returns a compact args summary from metadata when present.
func ExtractAttemptedArgs(metadata map[string]interface{}) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	if values := mapFromAny(metadata[MetadataAttemptedArgsKey]); len(values) > 0 {
		return values
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if values := mapFromAny(nested[MetadataAttemptedArgsKey]); len(values) > 0 {
			return values
		}
	}
	if invocation, ok := metadata["tool_invocation"].(map[string]interface{}); ok {
		if values := mapFromAny(invocation[MetadataAttemptedArgsKey]); len(values) > 0 {
			return values
		}
	}
	return nil
}

// ExtractFailedItems returns compact failed batch entries from generic metadata.
// Sources (first non-empty wins, then merged with de-dupe):
//  1. top-level / nested failed_items
//  2. batch items[] with success=false / error / status=failed
//  3. string lists under common failure keys (errors, failures, failed_paths)
//
// No tool-name branches: only structural signals.
func ExtractFailedItems(metadata map[string]interface{}) []FailedItem {
	if len(metadata) == 0 {
		return nil
	}
	if items := extractFailedItemsFromValue(metadata[MetadataFailedItemsKey]); len(items) > 0 {
		return compactFailedItems(items)
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if items := extractFailedItemsFromValue(nested[MetadataFailedItemsKey]); len(items) > 0 {
			return compactFailedItems(items)
		}
	}
	if invocation, ok := metadata["tool_invocation"].(map[string]interface{}); ok {
		if items := extractFailedItemsFromValue(invocation[MetadataFailedItemsKey]); len(items) > 0 {
			return compactFailedItems(items)
		}
	}

	// Infer from generic batch items / error lists.
	var collected []FailedItem
	if items := extractFailedItemsFromBatchItems(metadata["items"]); len(items) > 0 {
		collected = append(collected, items...)
	}
	for _, key := range []string{"errors", "failures", "failed_paths", "error_items"} {
		if items := extractFailedItemsFromValue(metadata[key]); len(items) > 0 {
			collected = append(collected, items...)
		}
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if items := extractFailedItemsFromBatchItems(nested["items"]); len(items) > 0 {
			collected = append(collected, items...)
		}
		for _, key := range []string{"errors", "failures", "failed_paths", "error_items"} {
			if items := extractFailedItemsFromValue(nested[key]); len(items) > 0 {
				collected = append(collected, items...)
			}
		}
	}
	return compactFailedItems(collected)
}

const (
	failedItemsMaxSummary = 4
	failedItemsMaxItems   = 8
	failedItemMaxRefRunes = 80
	failedItemMaxErrRunes = 120
)

func failedItemLabel(item FailedItem) string {
	label := strings.TrimSpace(item.Ref)
	if label == "" {
		label = strings.TrimSpace(item.Path)
	}
	if label == "" && item.Index != nil {
		label = fmt.Sprintf("#%d", *item.Index)
	}
	if label == "" {
		label = strings.TrimSpace(item.Error)
	}
	if label == "" {
		return ""
	}
	label = truncateRunes(label, failedItemMaxRefRunes)
	if errText := strings.TrimSpace(item.Error); errText != "" && label != errText {
		// Avoid duplicating the full error when it is already the label.
		errText = truncateRunes(errText, 60)
		if !strings.Contains(label, errText) {
			label = label + " (" + errText + ")"
		}
	}
	return label
}

func extractFailedItemsFromValue(value interface{}) []FailedItem {
	switch typed := value.(type) {
	case []FailedItem:
		return append([]FailedItem(nil), typed...)
	case []map[string]interface{}:
		out := make([]FailedItem, 0, len(typed))
		for i, row := range typed {
			if item, ok := failedItemFromMap(row, i, false); ok {
				out = append(out, item)
			}
		}
		return out
	case []interface{}:
		out := make([]FailedItem, 0, len(typed))
		for i, raw := range typed {
			switch row := raw.(type) {
			case map[string]interface{}:
				if item, ok := failedItemFromMap(row, i, false); ok {
					out = append(out, item)
				}
			case string:
				if item, ok := failedItemFromString(row, i); ok {
					out = append(out, item)
				}
			case FailedItem:
				out = append(out, row)
			default:
				text := strings.TrimSpace(fmt.Sprint(raw))
				if text != "" && text != "<nil>" {
					if item, ok := failedItemFromString(text, i); ok {
						out = append(out, item)
					}
				}
			}
		}
		return out
	case []string:
		out := make([]FailedItem, 0, len(typed))
		for i, row := range typed {
			if item, ok := failedItemFromString(row, i); ok {
				out = append(out, item)
			}
		}
		return out
	default:
		return nil
	}
}

func extractFailedItemsFromBatchItems(value interface{}) []FailedItem {
	rows, ok := value.([]interface{})
	if !ok {
		// Also accept []map[string]interface{} from typed tool metadata.
		if typed, ok := value.([]map[string]interface{}); ok {
			out := make([]FailedItem, 0, len(typed))
			for i, row := range typed {
				if !batchItemLooksFailed(row) {
					continue
				}
				if item, ok := failedItemFromMap(row, i, true); ok {
					out = append(out, item)
				}
			}
			return out
		}
		return nil
	}
	out := make([]FailedItem, 0)
	for i, raw := range rows {
		row, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if !batchItemLooksFailed(row) {
			continue
		}
		if item, ok := failedItemFromMap(row, i, true); ok {
			out = append(out, item)
		}
	}
	return out
}

func batchItemLooksFailed(row map[string]interface{}) bool {
	if len(row) == 0 {
		return false
	}
	if success, ok := row["success"].(bool); ok {
		return !success
	}
	if okFlag, ok := row["ok"].(bool); ok {
		return !okFlag
	}
	if status := strings.ToLower(strings.TrimSpace(fmt.Sprint(row["status"]))); status != "" && status != "<nil>" {
		switch status {
		case "failed", "failure", "error", "err":
			return true
		case "ok", "success", "succeeded", "pass", "passed":
			return false
		}
	}
	if errText := firstNonEmptyString(row, "error", "message", "err"); errText != "" {
		return true
	}
	return false
}

func failedItemFromMap(row map[string]interface{}, fallbackIndex int, preferIndex bool) (FailedItem, bool) {
	if len(row) == 0 {
		return FailedItem{}, false
	}
	item := FailedItem{}
	if idx, ok := intFromAny(row["index"]); ok {
		item.Index = intPtr(idx)
	} else if preferIndex {
		item.Index = intPtr(fallbackIndex)
	}
	item.Path = firstNonEmptyString(row, "path", "file_path", "file", "target_path")
	item.Error = firstNonEmptyString(row, "error", "message", "err")
	item.Ref = firstNonEmptyString(row, "ref", "command", "target", "name", "id")
	if item.Path == "" {
		// Some tools nest path under metadata.
		if nested, ok := row["metadata"].(map[string]interface{}); ok {
			if path := firstNonEmptyString(nested, "path", "file_path", "file"); path != "" {
				item.Path = path
			}
		}
	}
	if item.Ref == "" && item.Path != "" {
		item.Ref = item.Path
	}
	if item.Ref == "" && item.Index != nil && item.Error == "" {
		// Index-only rows are still useful for "re-run item #n".
		item.Ref = fmt.Sprintf("#%d", *item.Index)
	}
	if item.Path == "" && item.Error == "" && item.Ref == "" && item.Index == nil {
		return FailedItem{}, false
	}
	// Bound string fields.
	item.Path = truncateRunes(item.Path, failedItemMaxRefRunes)
	item.Ref = truncateRunes(item.Ref, failedItemMaxRefRunes)
	item.Error = truncateRunes(item.Error, failedItemMaxErrRunes)
	return item, true
}

func failedItemFromString(text string, fallbackIndex int) (FailedItem, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return FailedItem{}, false
	}
	item := FailedItem{}
	// Common "path: error" / "path - error" shapes from multi-file tools.
	if path, errText, ok := splitPathError(text); ok {
		item.Path = truncateRunes(path, failedItemMaxRefRunes)
		item.Ref = item.Path
		item.Error = truncateRunes(errText, failedItemMaxErrRunes)
		return item, true
	}
	// Bare path-looking string.
	if looksLikePathRef(text) {
		item.Path = truncateRunes(text, failedItemMaxRefRunes)
		item.Ref = item.Path
		return item, true
	}
	item.Error = truncateRunes(text, failedItemMaxErrRunes)
	item.Ref = truncateRunes(text, failedItemMaxRefRunes)
	_ = fallbackIndex // keep signature stable for callers that track position
	return item, true
}

func splitPathError(text string) (path, errText string, ok bool) {
	for _, sep := range []string{": ", "：", " - ", " — "} {
		if idx := strings.Index(text, sep); idx > 0 {
			left := strings.TrimSpace(text[:idx])
			right := strings.TrimSpace(text[idx+len(sep):])
			if left != "" && right != "" && looksLikePathRef(left) {
				return left, right, true
			}
		}
	}
	return "", "", false
}

func looksLikePathRef(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\n\r") {
		return false
	}
	// Heuristic: path separators, drive letters, or common file extensions.
	if strings.ContainsAny(value, `/\`) {
		return true
	}
	if len(value) >= 3 && value[1] == ':' && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) {
		return true
	}
	lower := strings.ToLower(value)
	for _, ext := range []string{".go", ".ts", ".js", ".py", ".md", ".txt", ".json", ".yaml", ".yml", ".toml"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	// Relative basename with extension-like suffix.
	if strings.Contains(value, ".") && !strings.Contains(value, " ") {
		return true
	}
	return false
}

func compactFailedItems(items []FailedItem) []FailedItem {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]FailedItem, 0, len(items))
	for _, item := range items {
		key := failedItemDedupeKey(item)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
		if len(out) >= failedItemsMaxItems {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func failedItemDedupeKey(item FailedItem) string {
	parts := make([]string, 0, 4)
	if item.Index != nil {
		parts = append(parts, fmt.Sprintf("i=%d", *item.Index))
	}
	if path := strings.TrimSpace(item.Path); path != "" {
		parts = append(parts, "p="+path)
	}
	if ref := strings.TrimSpace(item.Ref); ref != "" {
		parts = append(parts, "r="+ref)
	}
	if errText := strings.TrimSpace(item.Error); errText != "" {
		parts = append(parts, "e="+errText)
	}
	return strings.Join(parts, "|")
}

// FailedItemMap builds one compact failed-item recovery row for tool metadata.
// Tools should publish MetadataFailedItemsKey with these maps at the source so
// the model can re-run only failed entries without relying on string heuristics.
// Empty fields are omitted; returns nil when nothing useful remains.
func FailedItemMap(index *int, path, ref, errText string) map[string]interface{} {
	row := map[string]interface{}{}
	if index != nil {
		row["index"] = *index
	}
	if path = strings.TrimSpace(path); path != "" {
		row["path"] = truncateRunes(path, failedItemMaxRefRunes)
	}
	if ref = strings.TrimSpace(ref); ref != "" {
		row["ref"] = truncateRunes(ref, failedItemMaxRefRunes)
	}
	if errText = strings.TrimSpace(errText); errText != "" {
		row["error"] = truncateRunes(errText, failedItemMaxErrRunes)
	}
	if len(row) == 0 {
		return nil
	}
	// Prefer path as ref when ref was omitted.
	if _, hasRef := row["ref"]; !hasRef {
		if path, ok := row["path"].(string); ok && path != "" {
			row["ref"] = path
		}
	}
	return row
}

// IntPtr returns a pointer to v for FailedItemMap index fields (including 0).
func IntPtr(v int) *int {
	return intPtr(v)
}

func failedItemsToMaps(items []FailedItem) []map[string]interface{} {
	if len(items) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		row := map[string]interface{}{}
		if item.Index != nil {
			row["index"] = *item.Index
		}
		if path := strings.TrimSpace(item.Path); path != "" {
			row["path"] = path
		}
		if ref := strings.TrimSpace(item.Ref); ref != "" {
			row["ref"] = ref
		}
		if errText := strings.TrimSpace(item.Error); errText != "" {
			row["error"] = errText
		}
		if len(row) == 0 {
			continue
		}
		out = append(out, row)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func intPtr(value int) *int {
	v := value
	return &v
}

func intFromAny(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case float32:
		return int(typed), true
	case json.Number:
		if n, err := typed.Int64(); err == nil {
			return int(n), true
		}
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return 0, false
		}
		var n int
		if _, err := fmt.Sscanf(text, "%d", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

func firstNonEmptyString(row map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := row[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

// CompactAttemptedArgs builds a model-safe, size-bounded summary of tool args.
// It is schema-agnostic: keys are sorted, long strings truncated, nested depth
// limited, and likely secrets redacted. Never hard-codes tool or command names.
func CompactAttemptedArgs(args map[string]interface{}) map[string]interface{} {
	if len(args) == 0 {
		return nil
	}
	return compactAttemptedValue(args, 0).(map[string]interface{})
}

const (
	attemptedArgsMaxKeys        = 12
	attemptedArgsMaxDepth       = 2
	attemptedArgsMaxStringRunes = 120
	attemptedArgsMaxSliceItems  = 6
)

func compactAttemptedValue(value interface{}, depth int) interface{} {
	if depth > attemptedArgsMaxDepth {
		return "…"
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		if typed == nil {
			return map[string]interface{}{}
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			if strings.HasPrefix(key, "_") {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > attemptedArgsMaxKeys {
			keys = keys[:attemptedArgsMaxKeys]
		}
		out := make(map[string]interface{}, len(keys))
		for _, key := range keys {
			if isSensitiveArgKey(key) {
				out[key] = "[redacted]"
				continue
			}
			out[key] = compactAttemptedValue(typed[key], depth+1)
		}
		return out
	case map[string]string:
		converted := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			converted[key] = item
		}
		return compactAttemptedValue(converted, depth)
	case []interface{}:
		limit := len(typed)
		if limit > attemptedArgsMaxSliceItems {
			limit = attemptedArgsMaxSliceItems
		}
		out := make([]interface{}, 0, limit)
		for i := 0; i < limit; i++ {
			out = append(out, compactAttemptedValue(typed[i], depth+1))
		}
		if len(typed) > limit {
			out = append(out, fmt.Sprintf("…(+%d)", len(typed)-limit))
		}
		return out
	case []string:
		items := make([]interface{}, len(typed))
		for i, item := range typed {
			items[i] = item
		}
		return compactAttemptedValue(items, depth)
	case string:
		return truncateRunes(typed, attemptedArgsMaxStringRunes)
	case json.Number:
		return typed.String()
	case bool, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return typed
	case nil:
		return nil
	default:
		return truncateRunes(fmt.Sprint(typed), attemptedArgsMaxStringRunes)
	}
}

func isSensitiveArgKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	switch {
	case strings.Contains(lower, "password"),
		strings.Contains(lower, "secret"),
		strings.Contains(lower, "token"),
		strings.Contains(lower, "api_key"),
		strings.Contains(lower, "apikey"),
		strings.Contains(lower, "authorization"),
		strings.Contains(lower, "cookie"),
		strings.Contains(lower, "private_key"),
		strings.Contains(lower, "credential"):
		return true
	default:
		return false
	}
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

func stringSliceFromAny(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text := strings.TrimSpace(fmt.Sprint(item))
			if text != "" && text != "<nil>" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func mapFromAny(value interface{}) map[string]interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneAttemptedArgs(typed)
	default:
		return nil
	}
}

func cloneAttemptedArgs(in map[string]interface{}) map[string]interface{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func diagnosticString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if value, ok := nested[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func diagnosticTopLevelString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func diagnosticBool(metadata map[string]interface{}, key string) (bool, bool) {
	if len(metadata) == 0 {
		return false, false
	}
	if value, ok := metadata[key].(bool); ok {
		return value, true
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if value, ok := nested[key].(bool); ok {
			return value, true
		}
	}
	return false, false
}

func diagnosticInt(metadata map[string]interface{}, key string) int {
	if len(metadata) == 0 {
		return 0
	}
	if value, ok := intFromAny(metadata[key]); ok {
		return value
	}
	// Match diagnosticString/diagnosticBool: tool execution metadata is often
	// nested under tool_metadata by the agent loop. Batch disposition counts
	// (failed_count/succeeded_count/request_count) must be visible to
	// ExtractBatchStats / applyPartialOutcome so success-path partial batches
	// promote outcome=partial instead of staying success.
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if value, ok := intFromAny(nested[key]); ok {
			return value
		}
	}
	return 0
}

// diagnosticIntPtr returns a pointer when the key is present so 0-valued
// suggested_view_offset remains distinguishable from "unset".
func diagnosticIntPtr(metadata map[string]interface{}, key string) (*int, bool) {
	if len(metadata) == 0 {
		return nil, false
	}
	if value, ok := intFromAny(metadata[key]); ok {
		v := value
		return &v, true
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if value, ok := intFromAny(nested[key]); ok {
			v := value
			return &v, true
		}
	}
	return nil, false
}

// isGenericToolExecutionCode reports codes that are safe to refine with message
// / failure_class evidence. Specific recovery codes must not be overwritten.
func isGenericToolExecutionCode(code string) bool {
	switch runtimeerrors.ErrorCode(strings.TrimSpace(code)) {
	case "", runtimeerrors.ErrToolExecution, runtimeerrors.ErrToolBrokerFailure:
		return true
	default:
		return false
	}
}

// isGenericDefaultNextAction reports the stock "inspect and retry" guidance that
// often accompanies a bare TOOL_EXECUTION stamp and should yield to refined codes.
func isGenericDefaultNextAction(next string) bool {
	next = strings.TrimSpace(next)
	if next == "" {
		return false
	}
	if next == DefaultToolExecutionNextAction {
		return true
	}
	lower := strings.ToLower(next)
	return strings.HasPrefix(lower, "inspect the error details") &&
		strings.Contains(lower, "retry only when")
}

// refineGenericToolExecutionCode upgrades a generic/empty code using message
// classification and failure_class hints. Returns "" when no refinement applies.
func refineGenericToolExecutionCode(structuredCode, messageCode string, metadata map[string]interface{}, toolErr string) string {
	// failure_class authored by edit/apply_patch is authoritative for stale misses
	// even when error_code stayed generic (partial promotion / older binaries).
	failureClass := strings.ToLower(strings.TrimSpace(diagnosticString(metadata, "failure_class")))
	if failureClass == "stale_context" {
		return string(runtimeerrors.ErrToolStaleContext)
	}
	msgCode := strings.TrimSpace(messageCode)
	if msgCode == "" {
		msgCode = classifyToolErrorCode(toolErr)
	}
	if !knownRuntimeErrorCode(msgCode) || isGenericToolExecutionCode(msgCode) {
		return ""
	}
	// Only refine when structured was generic/empty; never demote a specific code.
	if structuredCode != "" && !isGenericToolExecutionCode(structuredCode) {
		return ""
	}
	return msgCode
}

// refineMislabeledStructuredCode corrects a narrow set of wrong specific codes
// when the body is unambiguously an edit/apply_patch STALE miss. Live chat-logs
// showed TOOL_TIMEOUT / TOOL_PATH_NOT_FOUND stamped on old_string misses because
// closest snippets contain those substrings and message heuristics fired first.
// Returns "" when no correction applies (preserves true timeouts/paths).
// Only edit-family tools are eligible — bash TOOL_TIMEOUT must never demote.
func refineMislabeledStructuredCode(toolName, structuredCode, messageCode string, metadata map[string]interface{}, toolErr string) string {
	if !isEditFamilyToolName(toolName) {
		return ""
	}
	structuredCode = strings.TrimSpace(structuredCode)
	if structuredCode == "" || isGenericToolExecutionCode(structuredCode) {
		return ""
	}
	// Only override timeout/path mislabels — never auth/permission/spawn/etc.
	switch runtimeerrors.ErrorCode(structuredCode) {
	case runtimeerrors.ErrToolTimeout, runtimeerrors.ErrToolPathNotFound:
		// ok
	default:
		return ""
	}
	failureClass := strings.ToLower(strings.TrimSpace(diagnosticString(metadata, "failure_class")))
	if failureClass == "stale_context" || messageLooksLikeStaleEditOrPatch(toolErr) {
		return string(runtimeerrors.ErrToolStaleContext)
	}
	msgCode := strings.TrimSpace(messageCode)
	if msgCode == "" {
		msgCode = classifyToolErrorCode(toolErr)
	}
	if msgCode == string(runtimeerrors.ErrToolStaleContext) {
		return string(runtimeerrors.ErrToolStaleContext)
	}
	return ""
}

func isEditFamilyToolName(toolName string) bool {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "edit", "multiedit", "apply_patch", "applypatch", "patch":
		return true
	default:
		return false
	}
}

// messageLooksLikeStaleEditOrPatch reports high-confidence edit/apply_patch miss
// phrasing independent of timeout/path substrings that may appear in snippets.
func messageLooksLikeStaleEditOrPatch(message string) bool {
	msg := strings.TrimSpace(message)
	if msg == "" {
		return false
	}
	lower := strings.ToLower(msg)
	if strings.Contains(msg, "old_string 未在文件中找到") ||
		strings.Contains(msg, "无法定位 hunk") ||
		strings.Contains(lower, "stale_context") ||
		strings.Contains(lower, "stale old_string") ||
		strings.Contains(lower, "stale @@") {
		return true
	}
	// English-ish edit miss without Chinese lead-in.
	if strings.Contains(lower, "old_string") &&
		(strings.Contains(lower, "not found") || strings.Contains(lower, "exact match")) {
		return true
	}
	// Hunk miss variants.
	if strings.Contains(lower, "hunk") &&
		(strings.Contains(lower, "not found") || strings.Contains(lower, "failed to find") ||
			strings.Contains(lower, "could not find") || strings.Contains(msg, "未找到期望旧内容")) {
		return true
	}
	return false
}

// isMismatchedRecoveryNextAction reports authored next_action text that clearly
// belongs to a different recovery class than the refined error code (e.g. timeout
// guidance stuck on a STALE_CONTEXT refine).
func isMismatchedRecoveryNextAction(next, refinedCode string) bool {
	next = strings.TrimSpace(next)
	refinedCode = strings.TrimSpace(refinedCode)
	if next == "" || refinedCode == "" {
		return false
	}
	lower := strings.ToLower(next)
	switch runtimeerrors.ErrorCode(refinedCode) {
	case runtimeerrors.ErrToolStaleContext:
		// Timeout / path-not-found stock guidance must not stick on STALE.
		if strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout") ||
			strings.Contains(lower, "deadline") ||
			(strings.Contains(lower, "path not found") && !strings.Contains(lower, "current_snippet") &&
				!strings.Contains(lower, "old_string") && !strings.Contains(lower, "stale")) {
			return true
		}
		// Prefer STALE-authored text; generic inspect/retry is already filtered.
		return false
	default:
		return false
	}
}

func classifyToolErrorCode(message string) string {
	message = strings.TrimSpace(message)
	if code := bracketedRuntimeErrorCode(message); code != "" {
		return code
	}
	lower := strings.ToLower(message)
	msg := message
	switch {
	case strings.Contains(lower, "background job") && strings.Contains(lower, "not found"),
		strings.Contains(lower, "job_id") && strings.Contains(lower, "not found"):
		return string(runtimeerrors.ErrJobNotFound)
	case strings.Contains(lower, "tool not found"), strings.Contains(lower, "unknown tool"):
		return string(runtimeerrors.ErrToolNotFound)
	case strings.Contains(lower, "rate limit"), strings.Contains(lower, "http 429"),
		strings.Contains(lower, "status 429"):
		return string(runtimeerrors.ErrAPIRateLimit)
	case strings.Contains(lower, "unauthorized"), strings.Contains(lower, "forbidden"),
		strings.Contains(lower, "http 401"), strings.Contains(lower, "http 403"),
		strings.Contains(lower, "status 401"), strings.Contains(lower, "status 403"):
		return string(runtimeerrors.ErrAPIUnauthorized)
	case strings.Contains(lower, "connection refused"), strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "network unavailable"), strings.Contains(lower, "no such host"):
		return string(runtimeerrors.ErrNetworkUnavailable)
	case strings.Contains(lower, "http 500"), strings.Contains(lower, "http 502"),
		strings.Contains(lower, "http 503"), strings.Contains(lower, "http 504"),
		strings.Contains(lower, "status 500"), strings.Contains(lower, "status 502"),
		strings.Contains(lower, "status 503"), strings.Contains(lower, "status 504"):
		return string(runtimeerrors.ErrAPIServerError)
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "access denied"),
		strings.Contains(lower, "not allowed"), strings.Contains(lower, "read-only"),
		strings.Contains(lower, "operation not permitted"), strings.Contains(lower, "denied by policy"),
		strings.Contains(lower, "hook blocked"):
		return string(runtimeerrors.ErrAgentPermission)
	// Prefer shell-compat when the missing-file signal is about an executable
	// (exec: ... / executable file not found) rather than a tool path arg.
	case isMissingCommandShellFailure(lower, message), isShellDialectFailure(lower):
		return string(runtimeerrors.ErrToolShellCompat)
	// STALE before PATH/TIMEOUT: edit/apply_patch error bodies embed closest file
	// snippets that often contain "timeout" / "no such file" map keys, which must
	// not reclassify an old_string/hunk miss as path/timeout.
	case messageLooksLikeStaleEditOrPatch(msg):
		return string(runtimeerrors.ErrToolStaleContext)
	case strings.Contains(lower, "path not found"), strings.Contains(lower, "file not found"),
		strings.Contains(lower, "no such file or directory"), strings.Contains(lower, "cannot find the path specified"),
		strings.Contains(lower, "cannot find the file specified"),
		strings.Contains(lower, "系统找不到指定的路径"), strings.Contains(lower, "系统找不到指定的文件"),
		strings.Contains(lower, "cannot find path"), strings.Contains(lower, "itemnotfoundexception"):
		return string(runtimeerrors.ErrToolPathNotFound)
	case strings.Contains(lower, "approval") && strings.Contains(lower, "expired"):
		return string(runtimeerrors.ErrApprovalExpired)
	case strings.Contains(lower, "spawn depth limit") || strings.Contains(lower, "spawn_depth") ||
		(strings.Contains(lower, "max_depth") && strings.Contains(lower, "spawn")) ||
		(strings.Contains(lower, "depth limit") && strings.Contains(lower, "spawn")):
		return string(runtimeerrors.ErrAgentSpawnDepthLimit)
	case strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "timed out"),
		strings.Contains(lower, "timeout"):
		return string(runtimeerrors.ErrToolTimeout)
	case strings.Contains(lower, "context canceled"), strings.Contains(lower, "context cancelled"),
		strings.Contains(lower, "run canceled"), strings.Contains(lower, "run cancelled"):
		return string(runtimeerrors.ErrAgentRunCanceled)
	case strings.Contains(lower, "invalid argument"), strings.Contains(lower, "invalid args"),
		strings.Contains(lower, "missing required"), strings.Contains(lower, " is required"),
		strings.Contains(lower, "cannot unmarshal"), strings.Contains(lower, "unexpected end of json"),
		strings.Contains(lower, "failed to parse arguments"), strings.Contains(lower, "unknown field"),
		// Toolkit tools emit Chinese validation copy (live residual: grep/edit/write
		// "pattern/file_path 参数缺失或无效" was bare TOOL_EXECUTION + generic next_action).
		strings.Contains(message, "参数缺失"), strings.Contains(message, "参数无效"),
		strings.Contains(message, "参数错误"), strings.Contains(message, "参数类型错误"),
		strings.Contains(message, "参数缺失或无效"), strings.Contains(message, "参数缺失或为空"),
		strings.Contains(message, "参数缺失或类型错误"), strings.Contains(message, "参数缺失或不是字符串"),
		// apply_patch parse failures (live residual: nested Begin marker mid-hunk
		// and empty/malformed envelopes) were bare TOOL_EXECUTION.
		strings.Contains(message, "不是合法的 hunk"), strings.Contains(message, "不是合法的补丁"),
		strings.Contains(message, "不是合法的新增文件内容"), strings.Contains(message, "hunk 没有内容"),
		strings.Contains(message, "补丁中没有可执行的文件操作"),
		strings.Contains(lower, "malformed patch"), strings.Contains(lower, "invalid patch"):
		return string(runtimeerrors.ErrToolInvalidArgs)
	default:
		return string(runtimeerrors.ErrToolExecution)
	}
}

func bracketedRuntimeErrorCode(message string) string {
	for start := strings.Index(message, "["); start >= 0; {
		remainder := message[start+1:]
		end := strings.Index(remainder, "]")
		if end < 0 {
			return ""
		}
		candidate := strings.TrimSpace(remainder[:end])
		if knownRuntimeErrorCode(candidate) {
			return candidate
		}
		next := start + end + 2
		if next >= len(message) {
			return ""
		}
		following := strings.Index(message[next:], "[")
		if following < 0 {
			return ""
		}
		start = next + following
	}
	return ""
}

func knownRuntimeErrorCode(code string) bool {
	switch runtimeerrors.ErrorCode(strings.TrimSpace(code)) {
	case runtimeerrors.ErrNetworkTimeout, runtimeerrors.ErrNetworkUnavailable,
		runtimeerrors.ErrAPIRateLimit, runtimeerrors.ErrAPIUnauthorized, runtimeerrors.ErrAPINotFound,
		runtimeerrors.ErrAPIBadRequest, runtimeerrors.ErrAPIServerError,
		runtimeerrors.ErrToolNotFound, runtimeerrors.ErrToolExecution, runtimeerrors.ErrToolTimeout,
		runtimeerrors.ErrWritePrecondition, runtimeerrors.ErrJobNotFound, runtimeerrors.ErrTurnDeadlineExceeded,
		runtimeerrors.ErrAgentRunCanceled, runtimeerrors.ErrApprovalExpired, runtimeerrors.ErrSessionLeaseConflict,
		runtimeerrors.ErrToolInvalidArgs, runtimeerrors.ErrToolPathNotFound, runtimeerrors.ErrToolStaleContext,
		runtimeerrors.ErrToolShellCompat, runtimeerrors.ErrAgentSpawnDepthLimit,
		runtimeerrors.ErrToolBrokerFailure,
		runtimeerrors.ErrProcessStartFailed, runtimeerrors.ErrProcessHealthcheck,
		runtimeerrors.ErrAgentMaxSteps, runtimeerrors.ErrAgentPermission, runtimeerrors.ErrContextBudget,
		runtimeerrors.ErrStreamInterrupted, runtimeerrors.ErrUpstreamUnavailable,
		runtimeerrors.ErrMemoryFull, runtimeerrors.ErrWorkflowCycle, runtimeerrors.ErrWorkflowStep,
		runtimeerrors.ErrSkillNotFound, runtimeerrors.ErrSkillLoadFailed, runtimeerrors.ErrInvalidManifest,
		runtimeerrors.ErrToolNotRegistered, runtimeerrors.ErrValidationFailed,
		runtimeerrors.ErrConfigNotFound, runtimeerrors.ErrConfigInvalid:
		return true
	default:
		return false
	}
}

func retryableToolErrorCode(code string) bool {
	switch runtimeerrors.ErrorCode(strings.TrimSpace(code)) {
	case runtimeerrors.ErrNetworkTimeout, runtimeerrors.ErrNetworkUnavailable,
		runtimeerrors.ErrAPIRateLimit, runtimeerrors.ErrAPIServerError,
		runtimeerrors.ErrToolTimeout, runtimeerrors.ErrTurnDeadlineExceeded,
		runtimeerrors.ErrSessionLeaseConflict, runtimeerrors.ErrProcessStartFailed,
		runtimeerrors.ErrStreamInterrupted, runtimeerrors.ErrUpstreamUnavailable:
		return true
	default:
		return false
	}
}

// DefaultToolExecutionNextAction is the generic recovery guidance when no more
// specific shell/message pattern matches.
const DefaultToolExecutionNextAction = "Inspect the error details, correct the cause, and retry only when the operation is safe."

// DefaultShellCompatNextAction guides recovery from shell/environment mismatches
// (missing utilities, wrong dialect, Unix-only pipelines on Windows shells).
const DefaultShellCompatNextAction = "Command or utility is missing, broken, or incompatible with the current shell. Use a shell-native alternative (for example Select-Object -First N instead of head on PowerShell/pwsh), install/fix the utility (including unusable PATH placeholders and broken package shims), or prefer a dedicated toolkit tool when available. Do not retry the same command unchanged."

func nextActionForToolError(code string, message string) string {
	// Message-aware shell recovery first for execution/compat codes so bare
	// "exit status 1" and Windows dialect failures get actionable guidance.
	if refined := shellFailureNextAction(message); refined != "" {
		switch runtimeerrors.ErrorCode(strings.TrimSpace(code)) {
		case runtimeerrors.ErrToolExecution, runtimeerrors.ErrToolShellCompat, "":
			return refined
		}
	}
	switch runtimeerrors.ErrorCode(strings.TrimSpace(code)) {
	case runtimeerrors.ErrToolInvalidArgs, runtimeerrors.ErrAPIBadRequest,
		runtimeerrors.ErrValidationFailed, runtimeerrors.ErrConfigInvalid:
		if refined := invalidArgsNextAction(message); refined != "" {
			return refined
		}
		return "Correct the tool arguments using the current schema, then call it again. Do not retry the same invalid/missing args unchanged."
	case runtimeerrors.ErrJobNotFound:
		return "Use the exact job_id returned by background_task; do not guess or synthesize an id."
	case runtimeerrors.ErrToolNotFound, runtimeerrors.ErrToolNotRegistered:
		return "Choose a tool name from the current tool definitions; do not retry the unavailable name."
	case runtimeerrors.ErrToolPathNotFound:
		return "Path not found. Prefer path_candidates when present, or ls/glob under the existing parent directory to discover the correct name; then retry with a confirmed path. Do not retry the same missing path unchanged."
	case runtimeerrors.ErrToolShellCompat:
		return DefaultShellCompatNextAction
	case runtimeerrors.ErrAgentPermission, runtimeerrors.ErrAPIUnauthorized, runtimeerrors.ErrApprovalExpired:
		return "Request the required approval or use an allowed tool; do not retry unchanged."
	case runtimeerrors.ErrToolTimeout, runtimeerrors.ErrTurnDeadlineExceeded:
		// Prefer search-aware recovery when the timeout message already steers
		// toward dedicated grep / narrower scopes (bash soft-enrichment path).
		if strings.Contains(strings.ToLower(message), "grep") ||
			strings.Contains(message, "代码搜索超时") ||
			strings.Contains(strings.ToLower(message), "search") {
			return "Code search timed out. Prefer toolkit `grep` with a narrower path/glob/max_count, or shrink the shell workdir/pattern. Do not replay the same broad scan unchanged; check partial output first if present."
		}
		return "Check whether the operation completed before a bounded retry to avoid duplicate side effects. Narrow the scope or raise timeout only when side effects remain safe."
	case runtimeerrors.ErrNetworkTimeout, runtimeerrors.ErrNetworkUnavailable,
		runtimeerrors.ErrAPIRateLimit, runtimeerrors.ErrAPIServerError,
		runtimeerrors.ErrSessionLeaseConflict, runtimeerrors.ErrStreamInterrupted,
		runtimeerrors.ErrUpstreamUnavailable:
		return "Retry with bounded backoff; stop after repeated failure and report the blocker."
	case runtimeerrors.ErrProcessStartFailed, runtimeerrors.ErrProcessHealthcheck:
		return "Inspect launch and health-check details, correct the cause, then retry only if side effects are safe."
	case runtimeerrors.ErrWritePrecondition, runtimeerrors.ErrToolStaleContext:
		return "Re-read the target file with view/grep, rebuild the edit/patch from the latest content, and do not retry the same stale context unchanged."
	case runtimeerrors.ErrAgentSpawnDepthLimit:
		return "complete_locally_or_use_spawn_team: this agent cannot spawn another child (max depth). Continue the work in the current agent, reuse an existing child, or use spawn_team for multi-worker orchestration. Do not retry the same spawn_agent."
	case runtimeerrors.ErrContextBudget:
		return "Reduce or compact the input and tool output before continuing."
	case runtimeerrors.ErrAgentRunCanceled:
		return "Do not retry automatically; start a new run only when continuation is still required."
	case runtimeerrors.ErrToolExecution:
		return DefaultToolExecutionNextAction
	default:
		return DefaultToolExecutionNextAction
	}
}

// invalidArgsNextAction refines generic schema guidance for common argument
// validation failures. Keep this message-pattern based so it applies uniformly
// to built-in, toolkit, MCP, and function-catalog tools.
func invalidArgsNextAction(message string) string {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return ""
	}

	switch {
	case strings.Contains(lower, "unknown field"),
		strings.Contains(lower, "unexpected field"),
		strings.Contains(lower, "additional properties") && strings.Contains(lower, "not allowed"),
		strings.Contains(lower, "unrecognized argument"),
		strings.Contains(lower, "unknown argument"):
		return "Remove or rename unsupported arguments using the current tool schema, then retry. Do not resend the same unknown fields unchanged."
	case strings.Contains(lower, "required") &&
		(strings.Contains(lower, "missing") || strings.Contains(lower, "is required") || strings.Contains(lower, "不能为空")),
		strings.Contains(lower, "missing required"),
		strings.Contains(lower, "缺少必填"),
		strings.Contains(lower, "缺少必要"),
		// Toolkit tools emit Chinese validation copy such as
		// "pattern/file_path 参数缺失或无效" / "…参数缺失或为空".
		strings.Contains(message, "参数缺失"),
		strings.Contains(message, "参数缺失或无效"),
		strings.Contains(message, "参数缺失或为空"),
		strings.Contains(message, "参数缺失或类型错误"),
		strings.Contains(message, "参数缺失或不是字符串"):
		return "Provide every required argument with the schema-prescribed type and shape, then retry. Do not resend the same incomplete arguments unchanged."
	case strings.Contains(lower, "cannot unmarshal"),
		strings.Contains(lower, "invalid json"),
		strings.Contains(lower, "json syntax"),
		strings.Contains(lower, "unexpected end of json"),
		strings.Contains(lower, "expected type"),
		strings.Contains(lower, "must be a"),
		strings.Contains(message, "参数无效"),
		strings.Contains(message, "参数错误"),
		strings.Contains(message, "参数类型错误"):
		return "Correct the JSON syntax and argument value types to match the current tool schema, then retry. Do not resend the same malformed payload unchanged."
	case strings.Contains(lower, "mutually exclusive"),
		strings.Contains(lower, "conflicting argument"),
		strings.Contains(lower, "cannot be used together"),
		strings.Contains(lower, "不能同时"):
		return "Remove the conflicting arguments and choose one schema-supported option, then retry. Do not resend the same incompatible combination unchanged."
	default:
		return ""
	}
}

// shellFailureNextAction derives actionable recovery text from shell/process
// failure messages. Patterns are environment/signal based — no tool-name
// rewrites (e.g. bash→grep) and no hard-coded single-command special cases
// beyond generic shell dialect signals (head/Select-Object, exit codes).
func shellFailureNextAction(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	lower := strings.ToLower(message)

	switch {
	case isMissingCommandShellFailure(lower, message), isShellDialectFailure(lower):
		return DefaultShellCompatNextAction
	case isShellSyntaxFailure(lower):
		return "Shell syntax or quoting failed. Simplify the command, fix quotes/escapes for the active shell, or split into smaller commands / a commands batch. Do not retry the identical command string."
	case strings.Contains(lower, "command batch completed with") && strings.Contains(lower, "failure"):
		return "Inspect failed item summaries in the result content; fix only the failed commands and re-run those items (prefer stop_on_error=true while diagnosing). Do not re-run successful batch items unchanged."
	case isGitIgnoredPathFailure(lower):
		return "Git refused a path that is ignored by .gitignore / exclude rules. Inspect with `git check-ignore -v <path>` or `git status --ignored`. Use a non-ignored path, update ignore rules, or `git add -f` only when force-adding is intentional. Do not retry the same ignored path unchanged."
	case isNoMatchShellFailure(lower, message):
		return "Treat this as no-match / empty evidence, not a hard crash. Change the search pattern, broaden path or workdir, or use a dedicated search tool that reports empty results clearly. Do not retry the identical query unchanged."
	case isBareExitFailure(lower, message):
		return "Process exited non-zero with little diagnostic detail. Inspect full Content/stderr if present, verify workdir and command spelling, then change inputs before retry. Do not replay the identical command blindly."
	case strings.Contains(lower, "exit status"), strings.Contains(lower, "exit code"),
		strings.Contains(lower, "命令执行失败"), strings.Contains(lower, "command failed"):
		return "Shell command failed. Read the error details and output summary, correct the cause (args, path, shell syntax, or environment), then retry only with a changed command. Do not retry unchanged."
	default:
		return ""
	}
}

func isMissingCommandShellFailure(lower, message string) bool {
	if lower == "" {
		lower = strings.ToLower(message)
	}
	switch {
	case strings.Contains(lower, "command not found"),
		strings.Contains(lower, "not found in path"),
		strings.Contains(lower, "no such file or directory") && looksLikeExecutableMissing(lower),
		strings.Contains(lower, "is not recognized as"),
		strings.Contains(lower, "is not recognized as a name of a cmdlet"),
		strings.Contains(lower, "the term '") && strings.Contains(lower, "is not recognized"),
		strings.Contains(lower, "不是内部或外部命令"),
		strings.Contains(lower, "无法将") && strings.Contains(lower, "项识别为"),
		strings.Contains(lower, "exit status 127"),
		strings.Contains(lower, "exit code 127"),
		// Windows cmd / PowerShell common missing-command statuses.
		strings.Contains(lower, "exit status 9009"),
		strings.Contains(lower, "exit code 9009"),
		strings.Contains(lower, "命令未找到"):
		return true
	default:
		return false
	}
}

func looksLikeExecutableMissing(lower string) bool {
	// Avoid treating generic path-not-found as shell-compat when the message is
	// clearly about a file path rather than a missing executable.
	return strings.Contains(lower, "exec:") ||
		strings.Contains(lower, "executable file not found") ||
		strings.Contains(lower, "failed to run") ||
		strings.Contains(lower, "cannot find the file specified") && strings.Contains(lower, "cmd")
}

func isShellDialectFailure(lower string) bool {
	// Generic Windows shell dialect signals (Unix utilities commonly misused).
	// Keep this pattern-based, not a rewrite-to-other-tool policy.
	if strings.Contains(lower, "select-object") &&
		(strings.Contains(lower, "head") || strings.Contains(lower, "tail")) {
		return true
	}
	if strings.Contains(lower, "the term 'head'") ||
		strings.Contains(lower, "the term 'tail'") ||
		strings.Contains(lower, "the term 'cat'") ||
		strings.Contains(lower, "the term 'ls'") ||
		strings.Contains(lower, "the term 'grep'") {
		return true
	}
	if strings.Contains(lower, "powershell") && strings.Contains(lower, "head") &&
		strings.Contains(lower, "not recognized") {
		return true
	}
	return false
}

func isShellSyntaxFailure(lower string) bool {
	switch {
	case strings.Contains(lower, "parsererror"),
		strings.Contains(lower, "parse error"),
		strings.Contains(lower, "syntax error"),
		strings.Contains(lower, "unexpected token"),
		strings.Contains(lower, "missing expression"),
		strings.Contains(lower, "missing closing"),
		strings.Contains(lower, "term expected"),
		strings.Contains(lower, "unrecognized token"),
		strings.Contains(lower, "unexpected eof"),
		strings.Contains(lower, "syntaxerror"):
		return true
	default:
		return false
	}
}

func isGitIgnoredPathFailure(lower string) bool {
	switch {
	case strings.Contains(lower, "the following paths are ignored by one of your .gitignore files"),
		strings.Contains(lower, "ignored by one of your .gitignore"),
		strings.Contains(lower, "is ignored by one of your .gitignore"),
		strings.Contains(lower, "use -f if you really want to add them"),
		strings.Contains(lower, "use -f if you really want to add it"),
		strings.Contains(lower, "hint: use -f if you really want to add"),
		strings.Contains(lower, "the following paths are ignored") && strings.Contains(lower, "gitignore"),
		strings.Contains(lower, "matches an ignore rule"):
		return true
	default:
		return false
	}
}

func isNoMatchShellFailure(lower, message string) bool {
	// Enrichment text from shell tools often states exit 1 == no matches.
	if strings.Contains(lower, "通常表示未匹配") ||
		strings.Contains(lower, "no matches") ||
		strings.Contains(lower, "no match") ||
		strings.Contains(lower, "未匹配到结果") ||
		strings.Contains(lower, "exit 1 means") && strings.Contains(lower, "no match") {
		return true
	}
	// Bare rg/grep-style "exit status 1" with empty-output guidance.
	if (strings.Contains(lower, "exit status 1") || strings.Contains(lower, "exit code 1")) &&
		(strings.Contains(lower, "无 stdout") || strings.Contains(lower, "no stdout") ||
			strings.Contains(lower, "无 stdout/stderr") || strings.Contains(lower, "no stdout/stderr") ||
			strings.Contains(lower, "not a crash") || strings.Contains(lower, "不是命令崩溃")) {
		return true
	}
	_ = message
	return false
}

func isBareExitFailure(lower, message string) bool {
	// Dominant efficiency-report shape: tool fails with only "exit status 1".
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return false
	}
	// Allow short wrappers like "命令执行失败: exit status 1"
	compact := strings.TrimSpace(lower)
	if compact == "exit status 1" || compact == "exit status 2" ||
		compact == "exit code 1" || compact == "exit code 2" {
		return true
	}
	if (strings.HasPrefix(compact, "exit status ") || strings.HasPrefix(compact, "exit code ")) &&
		len(compact) <= len("exit status 999") {
		return true
	}
	// Enriched but still low-signal: exit + command preview without useful stderr.
	if (strings.Contains(compact, "exit status") || strings.Contains(compact, "exit code")) &&
		(strings.Contains(compact, "无 stdout/stderr") || strings.Contains(compact, "no stdout/stderr") ||
			strings.Contains(compact, "无 stdout") || strings.Contains(compact, "no stdout")) &&
		!strings.Contains(compact, "输出摘要") && !strings.Contains(compact, "output summary") {
		return true
	}
	return false
}
