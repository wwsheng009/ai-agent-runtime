package output

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

const (
	modelToolTextMinSegmentBytes = 1024
	// modelArtifactNoticeIDPrefix marks a pointer to a persisted artifact record
	// (art_<uuid32>). The prefix is the stable span that splitTrailingArtifactNotice
	// and the frontend renderer match on; keep it byte-identical everywhere.
	modelArtifactNoticeIDPrefix = "Full raw output artifact_id: "
	// modelArtifactNoticePathPrefix marks a pointer to a shell-output artifact
	// file on disk (raw_output_artifact_path), honored when no artifact record id
	// was created.
	modelArtifactNoticePathPrefix = "Full raw output artifact: "
	// modelArtifactNoticeReadHint tells the model which consumer understands the
	// pointer. The id is an artifact record id, not a background job id; models
	// repeatedly copy it into task_output where it can never resolve.
	modelArtifactNoticeReadHint = "read the full raw output via artifact_read(artifact_id=<id>, offset=<bytes>, limit=<bytes>); never pass this id to task_output"
)

// modelToolTextByteBudget is the model-visible cap for tool result text entering
// history. Large outputs are truncated with a head/tail summary plus the raw
// output pointer. Configurable via SetModelToolTextByteBudget (e.g. from a
// per-session or startup setting); the zero-value default is 12 KiB.
var modelToolTextByteBudget = 12 * 1024

// SetModelToolTextByteBudget adjusts the model-visible tool text budget used
// when truncating large tool output before history insertion. Non-positive
// values are ignored so callers can safely pass parsed config even when unset.
func SetModelToolTextByteBudget(bytes int) {
	if bytes > 0 {
		modelToolTextByteBudget = bytes
	}
}

// ModelToolTextByteBudget returns the current model-visible tool text budget.
// Exported so shell tooling (bash output artifact threshold) stays aligned with
// the same configurable cap.
func ModelToolTextByteBudget() int {
	return modelToolTextByteBudget
}

// RenderFullToolResultContent builds the full tool_result text that should be
// sent back to the model. It preserves the original tool output instead of the
// reduced envelope summary used for CLI/event rendering.
func RenderFullToolResultContent(content interface{}, toolErr string) string {
	rawText := strings.TrimSpace(stringify(content))
	toolErr = strings.TrimSpace(toolErr)

	switch {
	case rawText == "" && toolErr == "":
		return "Tool returned no output."
	case rawText == "":
		return "Tool execution failed: " + toolErr
	case toolErr == "":
		return rawText
	default:
		return "Tool execution failed: " + toolErr + "\n" + rawText
	}
}

// RenderToolResultContentForModel returns the content that should be written to
// tool_result messages sent back to the model. Internal text-like outputs keep
// full raw text when small, but large payloads are truncated before entering
// history. Structured outputs keep the reduced envelope summary so specialized
// reducers can preserve stable facts without dumping large JSON.
func RenderToolResultContentForModel(content interface{}, toolErr string, envelope *Envelope) string {
	body := renderToolResultBodyForModel(content, toolErr, envelope)
	effectiveErr := strings.TrimSpace(toolErr)
	if effectiveErr == "" && envelope != nil {
		effectiveErr = strings.TrimSpace(envelope.Error)
	}
	metadata := cloneMetadataMap(envelopeMetadata(envelope))
	if effectiveErr == "" {
		if isModelVisibleEmptyBody(body, content) {
			if metadata == nil {
				metadata = map[string]interface{}{}
			}
			toolresult.MarkEmptySuccess(metadata)
		} else if metadata != nil {
			// Non-empty body: keep source/metadata-declared empty success (e.g.
			// grep/glob "no matches" text with match_count==0). Only drop empty
			// markers when there is no empty-success evidence and the body was
			// synthesized (mutation summary / ordinary payload).
			if !toolresult.HasEmptySuccessEvidence(metadata) {
				if empty, ok := metadata[toolresult.MetadataEmptyResultKey].(bool); ok && empty {
					delete(metadata, toolresult.MetadataEmptyResultKey)
				}
				if outcome := strings.TrimSpace(fmt.Sprint(metadata[toolresult.MetadataOutcomeKey])); outcome == toolresult.OutcomeEmpty {
					delete(metadata, toolresult.MetadataOutcomeKey)
				}
			} else {
				// Ensure outcome/empty_result stay consistent for declared empties.
				toolresult.MarkEmptySuccess(metadata)
			}
		}
	}
	diagnostic := toolresult.Diagnose(envelopeToolName(envelope), envelopeToolCallID(envelope), effectiveErr, metadata)
	if envelope != nil {
		if strings.TrimSpace(envelope.ErrorCode) != "" {
			diagnostic.ErrorCode = strings.TrimSpace(envelope.ErrorCode)
			diagnostic.Retryable = envelope.Retryable
		}
		if strings.TrimSpace(envelope.NextAction) != "" {
			diagnostic.NextAction = strings.TrimSpace(envelope.NextAction)
		}
		if diagnostic.OK && diagnostic.EmptyResult && strings.TrimSpace(diagnostic.NextAction) == "" {
			// Prefer envelope next_action if gateway already promoted empty-result guidance.
			if next := strings.TrimSpace(metadataString(envelope.Metadata, toolresult.MetadataNextActionKey)); next != "" {
				diagnostic.NextAction = next
			}
		}
	}
	return renderToolResultContract(body, diagnostic)
}

func renderToolResultBodyForModel(content interface{}, toolErr string, envelope *Envelope) string {
	if isEditingToolResult(envelope) {
		return renderToolTextForModelHistory(content, toolErr, envelope)
	}
	if isExternalMCPToolResult(envelope) {
		return renderToolTextForModelHistory(content, toolErr, envelope)
	}
	if isCollaborationResult(envelope) || isBackgroundTaskResult(envelope) {
		return renderToolTextForModelHistory(content, toolErr, envelope)
	}
	if modelSummaryPreferred(envelope) {
		return appendToolArtifactNotice(envelope.Render(), modelArtifactNotice(envelope))
	}
	if isTaskOutputToolResult(envelope) {
		return renderToolTextForModelHistory(content, toolErr, envelope)
	}
	if kind := toolResultKindForModel(content, envelope); kind != "" {
		switch kind {
		case toolresult.KindText, toolresult.KindEmpty:
			return renderToolTextForModelHistory(content, toolErr, envelope)
		case toolresult.KindStructured, toolresult.KindBinary:
			if envelope != nil {
				if summary := strings.TrimSpace(envelope.Render()); summary != "" {
					if extra := structuredEnvelopeSummary(envelope, content); extra != "" {
						return summary + "\n" + extra
					}
					return summary
				}
				if extra := structuredEnvelopeSummary(envelope, content); extra != "" {
					return extra
				}
			}
			return RenderFullToolResultContent(content, toolErr)
		}
	}
	if isTextLikeToolResult(content) {
		return renderToolTextForModelHistory(content, toolErr, envelope)
	}
	if envelope != nil {
		if summary := strings.TrimSpace(envelope.Render()); summary != "" {
			return summary
		}
	}
	return RenderFullToolResultContent(content, toolErr)
}

func envelopeToolName(envelope *Envelope) string {
	if envelope == nil {
		return ""
	}
	return envelope.ToolName
}

func envelopeToolCallID(envelope *Envelope) string {
	if envelope == nil {
		return ""
	}
	return envelope.ToolCallID
}

func renderToolResultContract(body string, diagnostic toolresult.Diagnostic) string {
	type modelContract struct {
		OK                      bool                    `json:"ok"`
		Outcome                 string                  `json:"outcome,omitempty"`
		ToolName                string                  `json:"tool_name,omitempty"`
		ToolCallID              string                  `json:"tool_call_id,omitempty"`
		ErrorCode               string                  `json:"error_code,omitempty"`
		Retryable               *bool                   `json:"retryable,omitempty"`
		EmptyResult             *bool                   `json:"empty_result,omitempty"`
		NextAction              string                  `json:"next_action,omitempty"`
		Policy                  string                  `json:"policy,omitempty"`
		PolicySource            string                  `json:"policy_source,omitempty"`
		Overridable             *bool                   `json:"overridable,omitempty"`
		PathCandidates          []string                `json:"path_candidates,omitempty"`
		AttemptedArgs           map[string]interface{}  `json:"attempted_args,omitempty"`
		RequestedCount          int                     `json:"requested_count,omitempty"`
		FailedCount             int                     `json:"failed_count,omitempty"`
		SucceededCount          int                     `json:"succeeded_count,omitempty"`
		PartialFailure          *bool                   `json:"partial_failure,omitempty"`
		FailedItems             []toolresult.FailedItem `json:"failed_items,omitempty"`
		FilePath                string                  `json:"file_path,omitempty"`
		SuggestedViewOffset     *int                    `json:"suggested_view_offset,omitempty"`
		SuggestedViewLimit      *int                    `json:"suggested_view_limit,omitempty"`
		CurrentSnippet          string                  `json:"current_snippet,omitempty"`
		CurrentSnippetStartLine *int                    `json:"current_snippet_start_line,omitempty"`
	}
	contractValue := modelContract{
		OK:                      diagnostic.OK,
		Outcome:                 diagnostic.Outcome,
		ToolName:                diagnostic.ToolName,
		ToolCallID:              diagnostic.ToolCallID,
		ErrorCode:               diagnostic.ErrorCode,
		NextAction:              diagnostic.NextAction,
		Policy:                  diagnostic.Policy,
		PolicySource:            diagnostic.PolicySource,
		Overridable:             diagnostic.Overridable,
		PathCandidates:          append([]string(nil), diagnostic.PathCandidates...),
		AttemptedArgs:           diagnostic.AttemptedArgs,
		RequestedCount:          diagnostic.RequestedCount,
		FailedCount:             diagnostic.FailedCount,
		SucceededCount:          diagnostic.SucceededCount,
		FailedItems:             append([]toolresult.FailedItem(nil), diagnostic.FailedItems...),
		FilePath:                diagnostic.FilePath,
		SuggestedViewOffset:     diagnostic.SuggestedViewOffset,
		SuggestedViewLimit:      diagnostic.SuggestedViewLimit,
		CurrentSnippet:          diagnostic.CurrentSnippet,
		CurrentSnippetStartLine: diagnostic.CurrentSnippetStartLine,
	}
	if diagnostic.PartialFailure || diagnostic.Outcome == toolresult.OutcomePartial {
		partial := true
		contractValue.PartialFailure = &partial
	}
	if !diagnostic.OK {
		contractValue.Retryable = &diagnostic.Retryable
		if contractValue.Outcome == "" {
			contractValue.Outcome = toolresult.OutcomeFailed
		}
	} else {
		// Success contracts are only emitted when they carry actionable disposition
		// (empty/partial) so ordinary success stays compact for the model.
		if !diagnostic.EmptyResult && diagnostic.Outcome != toolresult.OutcomeEmpty && diagnostic.Outcome != toolresult.OutcomePartial {
			return body
		}
		if diagnostic.EmptyResult || diagnostic.Outcome == toolresult.OutcomeEmpty {
			empty := true
			contractValue.EmptyResult = &empty
			if contractValue.Outcome == "" {
				contractValue.Outcome = toolresult.OutcomeEmpty
			}
			if strings.TrimSpace(contractValue.NextAction) == "" {
				contractValue.NextAction = toolresult.DefaultEmptyResultNextAction
			}
			if strings.TrimSpace(body) == "" || body == "Tool returned no output." {
				body = "Tool returned no output. This is a successful empty result, not a failure."
			}
		}
		if diagnostic.Outcome == toolresult.OutcomePartial && strings.TrimSpace(contractValue.NextAction) == "" {
			if diagnostic.FailedCount > 0 && diagnostic.RequestedCount > 0 {
				// Reuse the shared item-aware partial guidance so contract text
				// stays aligned with diagnose/gateway next_action.
				contractValue.NextAction = toolresult.NextActionForPartialBatch(diagnostic.FailedCount, diagnostic.RequestedCount, diagnostic.FailedItems)
			} else {
				contractValue.NextAction = "Reuse successful item outputs; fix or re-run only the failed items."
			}
		}
	}
	contract, err := json.Marshal(contractValue)
	if err != nil {
		return body
	}
	header := "Runtime tool result contract: " + string(contract)
	body = strings.TrimSpace(body)
	if body == "" {
		return header
	}
	separator := "\n\n"
	remaining := modelToolTextByteBudget - len(header) - len(separator)
	if remaining <= 0 {
		return safePrefixByBytes(header, modelToolTextByteBudget)
	}
	// Prefer keeping the trailing artifact notice outside truncation so empty/
	// partial/failure contracts never drop the pointer to full raw output.
	bodyCore, artifactNotice := splitTrailingArtifactNotice(body)
	if artifactNotice != "" {
		noticeBudget := len(artifactNotice) + len(separator)
		if remaining <= noticeBudget {
			// Contract already consumes most of the budget; keep notice if possible.
			return appendToolArtifactNotice(header, artifactNotice)
		}
		bodyBudget := remaining - noticeBudget
		if len(bodyCore) > bodyBudget {
			bodyCore = formatTruncatedToolTextForModel(bodyCore, bodyBudget)
		}
		return header + separator + appendToolArtifactNotice(bodyCore, artifactNotice)
	}
	if len(body) > remaining {
		body = formatTruncatedToolTextForModel(body, remaining)
	}
	return header + separator + body
}

// splitTrailingArtifactNotice peels a model-visible artifact pointer off the
// end of a tool body so contract budgeting can preserve it.
func splitTrailingArtifactNotice(body string) (core string, notice string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", ""
	}
	for _, prefix := range []string{
		modelArtifactNoticeIDPrefix,
		modelArtifactNoticePathPrefix,
	} {
		if strings.HasPrefix(body, prefix) && !strings.Contains(body, "\n") {
			return "", body
		}
		marker := "\n\n" + prefix
		if idx := strings.LastIndex(body, marker); idx >= 0 {
			tail := strings.TrimSpace(body[idx+2:])
			if strings.HasPrefix(tail, prefix) && !strings.Contains(tail, "\n") {
				return strings.TrimSpace(body[:idx]), tail
			}
		}
		marker = "\n" + prefix
		if idx := strings.LastIndex(body, marker); idx >= 0 {
			tail := strings.TrimSpace(body[idx+1:])
			if strings.HasPrefix(tail, prefix) && !strings.Contains(tail, "\n") {
				return strings.TrimSpace(body[:idx]), tail
			}
		}
	}
	return body, ""
}

func isModelVisibleEmptyBody(body string, content interface{}) bool {
	if strings.TrimSpace(stringify(content)) != "" {
		return false
	}
	trimmed := strings.TrimSpace(body)
	switch trimmed {
	case "", "Tool returned no output.", "Tool returned no output. This is a successful empty result, not a failure.":
		return true
	default:
		// Non-empty synthesized bodies (mutation summaries, reducers) are not empty results.
		return false
	}
}

func cloneMetadataMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func modelSummaryPreferred(envelope *Envelope) bool {
	if envelope == nil || strings.TrimSpace(envelope.Summary) == "" {
		return false
	}
	preferred, _ := envelope.Metadata["model_summary_preferred"].(bool)
	return preferred
}

func isTaskOutputToolResult(envelope *Envelope) bool {
	if envelope == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(envelope.ToolName), "task_output")
}

func isCollaborationResult(envelope *Envelope) bool {
	if envelope == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(envelope.ToolName)) {
	case "wait_agent", "read_agent_events", "list_agents", "spawn_agent", "send_message", "followup_task",
		"send_input", "resolve_agent_approval", "close_agent", "resume_agent":
		return true
	default:
		return false
	}
}

func isBackgroundTaskResult(envelope *Envelope) bool {
	if envelope == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(envelope.ToolName)) {
	case "background_task":
		return true
	default:
		return false
	}
}

func isEditingToolResult(envelope *Envelope) bool {
	if envelope == nil {
		return false
	}
	switch strings.TrimSpace(envelope.ToolName) {
	case "edit", "apply_patch":
		return true
	default:
		return false
	}
}

func toolResultKindForModel(content interface{}, envelope *Envelope) string {
	if envelope != nil {
		if kind := toolresult.KindFromMetadata(envelope.Metadata); kind != "" {
			return kind
		}
	}
	if isTextLikeToolResult(content) {
		if strings.TrimSpace(stringify(content)) == "" {
			return toolresult.KindEmpty
		}
		return toolresult.KindText
	}
	if content != nil {
		return toolresult.KindStructured
	}
	return ""
}

func isExternalMCPToolResult(envelope *Envelope) bool {
	if envelope == nil {
		return false
	}
	mcpName := strings.TrimSpace(metadataString(envelope.Metadata, "mcp_name"))
	if mcpName == "" {
		return false
	}
	return !strings.EqualFold(mcpName, "toolkit")
}

func metadataString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	if value, ok := metadata[key].(string); ok {
		return value
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if value, ok := nested[key].(string); ok {
			return value
		}
	}
	return ""
}

func isTextLikeToolResult(content interface{}) bool {
	switch content.(type) {
	case nil:
		return true
	case string:
		return true
	case []byte:
		return true
	case fmt.Stringer:
		return true
	default:
		return false
	}
}

func renderToolTextForModelHistory(content interface{}, toolErr string, envelope *Envelope) string {
	full := RenderFullToolResultContent(content, toolErr)
	if strings.TrimSpace(toolErr) == "" && strings.TrimSpace(stringify(content)) == "" {
		if summary := toolresult.MutationSummary(envelopeMetadata(envelope)); summary != "" {
			full = summary
		}
	}
	notice := modelArtifactNotice(envelope)
	if strings.TrimSpace(full) == "" {
		return appendToolArtifactNotice(full, notice)
	}
	if len(full) <= modelToolTextByteBudget {
		// The raw body fits the budget. No truncation happened, so a record-id
		// pointer is dropped unless it still adds value: failed results keep
		// the recovery hint, and artifact_read windows (artifact_source_id)
		// must never grow a recursive pointer cascade. Path/file notices
		// (raw_output_artifact_path) stay unconditional so on-disk artifacts
		// remain discoverable even when small.
		if notice == "" {
			return full
		}
		if isIDArtifactNotice(notice) {
			observability.RecordToolPointerNotice(observability.PointerNoticeKindID)
			if !failedResult(toolErr, envelope) && !isArtifactReadWindow(envelopeMetadata(envelope)) {
				return full
			}
		} else {
			observability.RecordToolPointerNotice(observability.PointerNoticeKindPath)
		}
		return appendToolArtifactNotice(full, notice)
	}
	// O-1: L4 head/tail fold happened at the render layer.
	observability.RecordToolOutputTruncation(observability.TruncationLayerRender, observability.TruncatedByBytes)
	// Truncation happened: thread the artifact id into the fold marker so the
	// head/tail view itself carries an explicit continuation command.
	artifactID := strings.TrimSpace(metadataString(envelopeMetadata(envelope), "artifact_id"))
	if artifactID == "" && len(envelopeArtifactIDs(envelope)) > 0 {
		artifactID = envelopeArtifactIDs(envelope)[0]
	}
	if notice == "" {
		return formatTruncatedToolTextForModel(full, modelToolTextByteBudget, artifactID)
	}
	bodyBudget := modelToolTextByteBudget - len(notice) - len("\n\n")
	if bodyBudget <= 0 {
		return safePrefixByBytes(notice, modelToolTextByteBudget)
	}
	return appendToolArtifactNotice(formatTruncatedToolTextForModel(full, bodyBudget, artifactID), notice)
}

func envelopeArtifactIDs(envelope *Envelope) []string {
	if envelope == nil {
		return nil
	}
	return envelope.ArtifactIDs
}

// isIDArtifactNotice reports whether the notice points at an artifact record
// id (art_<hex>) rather than an on-disk path artifact.
func isIDArtifactNotice(notice string) bool {
	return strings.HasPrefix(strings.TrimSpace(notice), modelArtifactNoticeIDPrefix)
}

// failedResult reports whether the tool result carries a real failure, in
// which case the raw-output pointer doubles as the recovery hint and must
// survive even when the body fits the budget.
func failedResult(toolErr string, envelope *Envelope) bool {
	if strings.TrimSpace(toolErr) != "" {
		return true
	}
	if envelope == nil {
		return false
	}
	if strings.TrimSpace(envelope.Error) != "" {
		return true
	}
	if value, ok := envelope.Metadata["tool_error"].(string); ok {
		return strings.TrimSpace(value) != ""
	}
	return false
}

func envelopeMetadata(envelope *Envelope) map[string]interface{} {
	if envelope == nil {
		return nil
	}
	return envelope.Metadata
}

func modelArtifactNotice(envelope *Envelope) string {
	if envelope == nil {
		return ""
	}
	tail := artifactNoticeTail(envelope)
	if artifactID := strings.TrimSpace(metadataString(envelope.Metadata, "artifact_id")); artifactID != "" {
		return modelArtifactNoticeIDPrefix + artifactID + tail + "; " + modelArtifactNoticeReadHint
	}
	if len(envelope.ArtifactIDs) > 0 && strings.TrimSpace(envelope.ArtifactIDs[0]) != "" {
		return modelArtifactNoticeIDPrefix + strings.TrimSpace(envelope.ArtifactIDs[0]) + tail + "; " + modelArtifactNoticeReadHint
	}
	path := strings.TrimSpace(metadataString(envelope.Metadata, "raw_output_artifact_path"))
	if path != "" {
		return modelArtifactNoticePathPrefix + path + tail
	}
	return ""
}

// artifactNoticeTail appends a compact machine-parseable summary to the raw
// output pointer: size=<bytes> when known, plus kind=<output kind>. The line
// stays single-line so splitTrailingArtifactNotice and the frontend renderer
// can match the prefix and peel the whole notice.
func artifactNoticeTail(envelope *Envelope) string {
	if envelope == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if size := metadataInt(envelope.Metadata, "raw_bytes", "byte_count"); size > 0 {
		parts = append(parts, "size="+strconv.Itoa(size))
	}
	kind := strings.TrimSpace(toolresult.KindFromMetadata(envelope.Metadata))
	if kind == "" {
		kind = "raw_output"
	}
	parts = append(parts, "kind="+kind)
	return " " + strings.Join(parts, " ")
}

// metadataInt reads the first numeric metadata value found among keys,
// tolerating int/float64 (JSON round-trips) and string encodings.
func metadataInt(metadata map[string]interface{}, keys ...string) int {
	if len(metadata) == 0 {
		return 0
	}
	for _, key := range keys {
		switch value := metadata[key].(type) {
		case int:
			return value
		case int64:
			return int(value)
		case float64:
			return int(value)
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func appendToolArtifactNotice(body string, notice string) string {
	body = strings.TrimSpace(body)
	notice = strings.TrimSpace(notice)
	switch {
	case body == "":
		return notice
	case notice == "":
		return body
	default:
		return body + "\n\n" + notice
	}
}

// truncationMarker renders the fold marker inserted between the head and tail
// segments. Both the emitted marker and the budget reserve go through this
// helper so the charged bytes can never drift from the rendered bytes.
func truncationMarker(artifactID string, omittedBytes int) string {
	marker := fmt.Sprintf("\n\n[output truncated for history safety: omitted %d bytes from the middle]", omittedBytes)
	// P2 continuation guidance: embed the exact dereference command in the
	// fold marker so the model never wanders (e.g. re-calling the same tool).
	if id := strings.TrimSpace(artifactID); id != "" {
		marker += fmt.Sprintf("; read via artifact_read(artifact_id=%s, offset=<bytes>, limit=<bytes>)", id)
	}
	return marker + "\n\n"
}

// truncationMarkerReserve returns the exact number of bytes the fold marker can
// occupy for a result whose omitted middle is at most maxOmittedBytes. Omitted
// bytes never exceed the total content length, so digit-counting on that upper
// bound makes the reserve an exact ceiling rather than a guess.
func truncationMarkerReserve(artifactID string, maxOmittedBytes int) int {
	if maxOmittedBytes < 0 {
		maxOmittedBytes = 0
	}
	return len(truncationMarker(artifactID, maxOmittedBytes))
}

func formatTruncatedToolTextForModel(content string, budget int, artifactID ...string) string {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if content == "" || budget <= 0 || len(content) <= budget {
		return content
	}

	totalLines := countTextLines(content)
	totalBytes := len(content)
	header := fmt.Sprintf("Total output lines: %d\nTotal output bytes: %d\n\n", totalLines, totalBytes)
	if firstErr := firstFailureLine(content); firstErr != "" {
		header += "First error line: " + firstErr + "\n\n"
	}

	var pointerID string
	if len(artifactID) > 0 {
		pointerID = strings.TrimSpace(artifactID[0])
	}
	reserve := truncationMarkerReserve(pointerID, totalBytes)

	// The budget is a hard ceiling: header + head + marker + tail must never
	// exceed it. Reserve the header and the (exact, upper-bound) marker first,
	// then split whatever remains between head and tail.
	bodyBudget := budget - len(header) - reserve
	if bodyBudget <= 0 {
		return safePrefixByBytes(content, budget)
	}

	if bodyBudget >= totalBytes {
		bodyBudget = totalBytes - 1
	}
	if bodyBudget <= 0 {
		return safePrefixByBytes(content, budget)
	}

	headBudget := bodyBudget * 2 / 3
	tailBudget := bodyBudget - headBudget
	// The minimum segment size is a usability floor, not a budget override:
	// apply it only when the remaining body budget can actually afford it.
	if bodyBudget >= modelToolTextMinSegmentBytes*2 {
		if headBudget < modelToolTextMinSegmentBytes {
			headBudget = modelToolTextMinSegmentBytes
			tailBudget = bodyBudget - headBudget
		}
		if tailBudget < modelToolTextMinSegmentBytes {
			tailBudget = modelToolTextMinSegmentBytes
			headBudget = bodyBudget - tailBudget
		}
	}

	head := safePrefixByBytes(content, headBudget)
	tail := safeSuffixByBytes(content, tailBudget)
	if len(head)+len(tail) >= totalBytes {
		// head+tail already span the whole content, so there is no middle to
		// omit; take a deterministic split inside the body budget instead.
		head = safePrefixByBytes(content, bodyBudget*2/3)
		tail = safeSuffixByBytes(content, bodyBudget/3)
	}

	omittedBytes := totalBytes - len(head) - len(tail)
	if omittedBytes < 0 {
		omittedBytes = 0
	}
	marker := truncationMarker(pointerID, omittedBytes)
	if pointerID != "" {
		observability.RecordToolPointerNotice(observability.PointerNoticeKindDerefHint)
	}
	return header + head + marker + tail
}

// firstFailureLine returns the first content line that looks like a failure
// (error/failed/fatal/panic/exception or Chinese equivalents). Failures often
// live at the tail of a large output that head/tail truncation can still miss
// (or that lands inside the omitted middle), so the truncated summary carries
// the earliest signal line explicitly. Returns "" when nothing matches.
func firstFailureLine(text string) string {
	markers := []string{"error", "failed", "failure", "fatal", "panic", "exception", "错误", "失败"}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		for _, marker := range markers {
			if strings.Contains(lower, marker) {
				return summarizeLine(line, 240)
			}
		}
	}
	return ""
}

// structuredEnvelopeSummary emits a compact schema/size summary for
// structured/binary tool results that render as envelope summaries, so the
// model still gets data-shape signal instead of a bare summary line or id.
func structuredEnvelopeSummary(envelope *Envelope, content interface{}) string {
	if envelope == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if kind := strings.TrimSpace(toolresult.KindFromMetadata(envelope.Metadata)); kind != "" {
		parts = append(parts, "kind="+kind)
	}
	if fields := structuredFieldCount(content); fields > 0 {
		parts = append(parts, "fields="+strconv.Itoa(fields))
	}
	if size := metadataInt(envelope.Metadata, "raw_bytes", "byte_count"); size > 0 {
		parts = append(parts, "size="+strconv.Itoa(size))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Structured output summary: " + strings.Join(parts, " ")
}

func structuredFieldCount(content interface{}) int {
	switch typed := content.(type) {
	case map[string]interface{}:
		return len(typed)
	case []interface{}:
		return len(typed)
	default:
		return 0
	}
}

func countTextLines(text string) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func safePrefixByBytes(text string, maxBytes int) string {
	if maxBytes <= 0 || text == "" {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	index := 0
	for index < len(text) {
		_, size := utf8.DecodeRuneInString(text[index:])
		if size <= 0 || index+size > maxBytes {
			break
		}
		index += size
	}
	return text[:index]
}

func safeSuffixByBytes(text string, maxBytes int) string {
	if maxBytes <= 0 || text == "" {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	start := len(text)
	used := 0
	for start > 0 {
		_, size := utf8.DecodeLastRuneInString(text[:start])
		if size <= 0 || used+size > maxBytes {
			break
		}
		start -= size
		used += size
	}
	return text[start:]
}
