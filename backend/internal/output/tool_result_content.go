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

const (
	// modelToolTextBudgetFloorBytes bounds a tool-declared window from below: a
	// declared budget smaller than this would starve the model of the very
	// output the tool produced, so the layer budget stays the effective floor.
	modelToolTextBudgetFloorBytes = 4 * 1024
	// modelToolTextBudgetCeilingBytes bounds a tool-declared window from above:
	// history must stay bounded even if a tool asks for a wider window.
	modelToolTextBudgetCeilingBytes = 64 * 1024
)

// effectiveModelToolTextBudget resolves the byte budget the render layer folds
// a tool result to.
//
// A tool may declare its own model-visible window through
// toolresult.MetadataModelVisibleBudgetKey (shell output does) without opting
// out of render-layer folding. Honoring the declaration is what gives the tool
// "its own budget": the payload stays intact for the archive and artifact_read,
// while the model-visible head is sized by the tool's own contract instead of
// the one-size-fits-all backstop. The declared value is clamped to
// [modelToolTextByteBudget, modelToolTextBudgetCeilingBytes]; without a
// declaration the layer budget applies.
func effectiveModelToolTextBudget(metadata map[string]interface{}) int {
	declared := toolresult.ModelVisibleBudgetBytes(metadata)
	if declared <= 0 {
		return modelToolTextByteBudget
	}
	// The declared window is honored both ways, but never below the layer
	// backstop (nor below the sanity floor) and never above the ceiling.
	floor := modelToolTextByteBudget
	if floor < modelToolTextBudgetFloorBytes {
		floor = modelToolTextBudgetFloorBytes
	}
	if declared < floor {
		return floor
	}
	if declared > modelToolTextBudgetCeilingBytes {
		return modelToolTextBudgetCeilingBytes
	}
	return declared
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

// isCollaborationResult reports whether the tool result carries the parent's
// collaboration output verbatim. Every supervision/collaboration tool must be
// listed here: the generic structured path renders only
// "Structured output summary: kind=structured size=N" and drops the body, so a
// missing entry is a silent content loss (H1:
// read_agent_result was missing and the child deliverable became unreadable).
func isCollaborationResult(envelope *Envelope) bool {
	if envelope == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(envelope.ToolName)) {
	case "wait_agent", "read_agent_events", "read_agent_result", "list_agents", "spawn_agent", "send_message", "followup_task",
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

// toolTruncationMetadataKeys is the unified vocabulary for "the producing tool
// already folded its own output". Controlled tools publish at least one of
// these keys together with their continuation metadata (offset/limit/eof/
// artifact_id); every downstream layer must treat the body as final and must
// not fold it a second time.
//
// Canonical key: "is_truncated" (controls tools that own their paging window).
// Legacy aliases kept readable so existing emitters stay compatible:
// "results_truncated" (grep byte budget), "truncated".
//
// "output_truncated" is deliberately NOT part of this vocabulary: it is a
// capture-layer fact ("the raw stream exceeded the retention limit"), not a
// statement that the payload was folded to a model-visible window. Treating it
// as tool-owned truncation is what let 256 KiB exec captures bypass the render
// layer entirely.
// toolresult.MetadataSkipRenderTruncationKey ("skip_render_truncation") is part
// of this vocabulary: a tool that sets it opts out of render-layer (L4)
// truncation management entirely and owns its own paging window.
var toolTruncationMetadataKeys = []string{
	"skip_render_truncation",
	"is_truncated",
	"results_truncated",
	"truncated",
}

// toolFoldedOwnWindowKeys is the subset of the truncation vocabulary that means
// "this payload was actually folded to the producing tool's own window". It is
// the gate for attaching a raw-output pointer to a payload whose tool owns its
// window: an intact body needs no pointer, a folded one does.
var toolFoldedOwnWindowKeys = []string{
	"is_truncated",
	"results_truncated",
	"truncated",
}

// toolFoldedOwnWindow reports whether the producing tool published fold
// metadata for this result, i.e. whether bytes are missing from the body the
// model is about to see.
func toolFoldedOwnWindow(metadata map[string]interface{}) bool {
	return metadataFlagTruthy(metadata, toolFoldedOwnWindowKeys)
}

// toolTruncatedUpstream reports whether the controlled tool already truncated
// its own output. This is the single source of truth that stops the truncation
// cascade: once a tool has folded its output and published continuation
// metadata, the render layer must not fold it again (a second fold would charge
// the byte budget twice, emit a duplicate truncation notice and contradict the
// tool's own continuation guidance).
func toolTruncatedUpstream(metadata map[string]interface{}) bool {
	if len(metadata) == 0 {
		return false
	}
	// A producing tool that declares it owns truncation
	// (skip_render_truncation, flat or nested under "tool_metadata") decides
	// the final shape of its payload. This explicit marker is checked before
	// the inferred vocabulary scan below.
	if toolresult.SkipsRenderTruncation(metadata) {
		return true
	}
	return metadataFlagTruthy(metadata, toolTruncationMetadataKeys)
}

// metadataFlagTruthy scans metadata (flat or nested under "tool_metadata") for
// the first truthy value among keys, tolerating the bool/string/number
// encodings that tool emitters and JSON round-trips produce.
func metadataFlagTruthy(metadata map[string]interface{}, keys []string) bool {
	if len(metadata) == 0 {
		return false
	}
	if flagsTruthyIn(metadata, keys) {
		return true
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		return flagsTruthyIn(nested, keys)
	}
	return false
}

func flagsTruthyIn(metadata map[string]interface{}, keys []string) bool {
	for _, key := range keys {
		switch value := metadata[key].(type) {
		case bool:
			if value {
				return true
			}
		case string:
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "true", "1", "yes", "y":
				return true
			}
		case int:
			if value != 0 {
				return true
			}
		case float64:
			if value != 0 {
				return true
			}
		}
	}
	return false
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
	// A tool may declare its own model-visible window (shell output does): the
	// fold budget below then follows the tool's contract instead of the
	// one-size-fits-all backstop.
	budget := effectiveModelToolTextBudget(envelopeMetadata(envelope))
	// Controlled tool that already truncated its own output owns the final shape
	// of its payload: it folded to its own limit, published continuation metadata
	// (offset/limit/eof/artifact) and told the model how to page through the rest.
	// Folding again here would charge the byte budget twice, emit a duplicate
	// "middle omitted" marker and contradict the tool's own continuation notice.
	if toolTruncatedUpstream(envelopeMetadata(envelope)) {
		return attachOwnedWindowPointer(full, toolErr, envelope, notice)
	}
	if strings.TrimSpace(full) == "" {
		return appendToolArtifactNotice(full, notice)
	}
	if len(full) <= budget {
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
	// O-1: L4 head-only fold happened at the render layer.
	observability.RecordToolOutputTruncation(observability.TruncationLayerRender, observability.TruncatedByBytes)
	// Truncation happened: the fold notice itself carries the "first N lines +
	// next step" contract, so no artifact id is threaded through this path. The
	// artifact notice (when one exists) is still appended below.
	if notice == "" {
		return formatTruncatedToolTextForModel(full, budget)
	}
	bodyBudget := budget - len(notice) - len("\n\n")
	if bodyBudget <= 0 {
		return safePrefixByBytes(notice, budget)
	}
	return appendToolArtifactNotice(formatTruncatedToolTextForModel(full, bodyBudget), notice)
}

// attachOwnedWindowPointer decides the raw-output pointer for a payload whose
// producing tool already owns its window (skip_render_truncation).
//
// The pointer is a recovery aid, not decoration: it is attached only when it
// can be acted on. A body that was really folded (the tool published fold
// metadata), a failed call (the pointer doubles as the recovery hint) and an
// artifact_read window (the pointer is the paging contract itself) keep it;
// an intact body that the model can already read in full does not, because a
// pointer there only invites a pointless dereference.
func attachOwnedWindowPointer(full string, toolErr string, envelope *Envelope, notice string) string {
	metadata := envelopeMetadata(envelope)
	if notice == "" {
		return full
	}
	if !isIDArtifactNotice(notice) {
		// On-disk path artifacts stay discoverable even for complete bodies.
		observability.RecordToolPointerNotice(observability.PointerNoticeKindPath)
		return appendToolArtifactNotice(full, notice)
	}
	if !toolFoldedOwnWindow(metadata) && !failedResult(toolErr, envelope) && !isArtifactReadWindow(metadata) {
		return full
	}
	observability.RecordToolPointerNotice(observability.PointerNoticeKindID)
	return appendToolArtifactNotice(full, notice)
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

// truncationMarker renders the head-only fold notice appended after the shown
// lines. It names how many lines are visible, how many were dropped from the
// end, and what the model should do next, so a folded result is never a dead
// end. Both the emitted notice and the budget reserve go through this helper so
// the charged bytes can never drift from the rendered bytes.
func truncationMarker(shownLines, totalLines, omittedBytes int) string {
	if shownLines < 0 {
		shownLines = 0
	}
	if totalLines < shownLines {
		totalLines = shownLines
	}
	omittedLines := totalLines - shownLines
	marker := fmt.Sprintf(
		"\n\n[output truncated for history safety: showing the first %d of %d lines; omitted %d lines (%d bytes) from the end]",
		shownLines, totalLines, omittedLines, omittedBytes,
	)
	// Next-step guidance: the omitted tail is not reachable by re-issuing the
	// identical call (it would fold the same way), so name the cheapest
	// narrower-window recovery per tool family instead of leaving the model to
	// guess or abandon the read.
	marker += "\n[next step: re-issue the same call with a narrower window to read the omitted tail — " +
		"view: offset=<next line, 0-based> plus a smaller limit; " +
		"grep/shell: narrow the pattern, path or command output instead of repeating the identical call]"
	return marker + "\n\n"
}

// truncationMarkerReserve returns the exact number of bytes the fold notice can
// occupy for a result with totalLines lines and totalBytes bytes. The shown line
// count is at most totalLines and the omitted line/byte counts are at most those
// totals, so digit-counting on the upper bound makes the reserve an exact
// ceiling rather than a guess.
func truncationMarkerReserve(totalLines, totalBytes int) int {
	if totalLines < 0 {
		totalLines = 0
	}
	if totalBytes < 0 {
		totalBytes = 0
	}
	reserve := len(truncationMarker(totalLines, totalLines, totalBytes))
	if partial := len(truncationMarkerPartial(totalLines, totalBytes, totalBytes)); partial > reserve {
		reserve = partial
	}
	return reserve
}

// truncationMarkerPartial renders the fold notice for a head that stops in the
// middle of a line (the first line alone overflowed the body budget). Line
// arithmetic cannot describe that loss: the clean-cut notice would claim "0
// lines omitted" while thousands of bytes were dropped from the same line, and
// its line-offset recovery cannot resume a mid-line cut. This variant states
// the shown lines, the shown bytes of the cut line, and points at byte-range
// recovery instead.
func truncationMarkerPartial(completeLines, partialBytes, omittedBytes int) string {
	if completeLines < 0 {
		completeLines = 0
	}
	if partialBytes < 0 {
		partialBytes = 0
	}
	if omittedBytes < 0 {
		omittedBytes = 0
	}
	marker := fmt.Sprintf(
		"\n\n[output truncated for history safety: showing %d complete lines plus the first %d bytes of line %d; omitted %d bytes from the end]",
		completeLines, partialBytes, completeLines+1, omittedBytes,
	)
	marker += "\n[next step: this window stops mid-line, so a line offset cannot resume it — " +
		"read the raw output pointer below by byte range (artifact_read with offset=<byte offset>) " +
		"or re-issue a narrower call instead of repeating the identical one]"
	return marker + "\n\n"
}

// formatTruncatedToolTextForModel folds oversized tool text to a head-only
// window: the first lines that fit the budget, followed by an explicit notice
// that reports how many lines were shown and omitted plus how to read the rest
// with a narrower call. The tail is intentionally not kept — a hole in the
// middle of the text cannot be paged by the model, while a "first N lines +
// next step" contract can.
func formatTruncatedToolTextForModel(content string, budget int) string {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if content == "" || budget <= 0 || len(content) <= budget {
		return content
	}

	totalLines := countTextLines(content)
	totalBytes := len(content)
	// These counts describe the tool-result text being folded here, which may
	// already have been truncated by the executor capture layer. The executor
	// keeps the raw "Total output lines/bytes" labels for the process output
	// (see internal/executor/output_capture.go), so use a distinct label to
	// avoid two different totals appearing under the same name.
	header := fmt.Sprintf("Tool result lines: %d\nTool result bytes: %d\n\n", totalLines, totalBytes)
	if firstErr := firstFailureLine(content); firstErr != "" {
		header += "First error line: " + firstErr + "\n\n"
	}

	// The budget is a hard ceiling: header + shown head + notice must never
	// exceed it. Reserve the header and the (exact, upper-bound) notice first,
	// then spend whatever remains on the head.
	bodyBudget := budget - len(header) - truncationMarkerReserve(totalLines, totalBytes)
	if bodyBudget <= 0 {
		return safePrefixByBytes(content, budget)
	}

	head := headLinesWithinBudget(content, bodyBudget)
	omittedBytes := totalBytes - len(head)
	if omittedBytes < 0 {
		omittedBytes = 0
	}
	// A head that stops mid-line means the first line alone overflowed the body
	// budget (headLinesWithinBudget only cuts mid-line in that fallback), so the
	// line-count notice would contradict itself; report the partial line and
	// the byte-range recovery path instead.
	if partialBytes := partialLineBytes(head); partialBytes > 0 {
		return header + head + truncationMarkerPartial(countShownLines(head)-1, partialBytes, omittedBytes)
	}
	shownLines := countShownLines(head)
	if shownLines > totalLines {
		shownLines = totalLines
	}
	return header + head + truncationMarker(shownLines, totalLines, omittedBytes)
}

// headLinesWithinBudget keeps the longest prefix of content that fits maxBytes,
// snapping down to the last line boundary while that still uses at least half
// the budget. Snapping keeps the "showing the first N lines" count exact and
// never hands the model a half line; content whose first line alone overflows
// the budget falls back to a byte prefix because no earlier boundary exists.
func headLinesWithinBudget(content string, maxBytes int) string {
	if maxBytes <= 0 || content == "" {
		return ""
	}
	if len(content) <= maxBytes {
		return content
	}
	head := safePrefixByBytes(content, maxBytes)
	if idx := strings.LastIndex(head, "\n"); idx >= 0 && idx+1 >= maxBytes/2 {
		return head[:idx+1]
	}
	return head
}

// countShownLines counts the content lines visible in a head segment. A head
// that ends mid-line still shows that partial line, so it is counted too;
// callers that must not conflate a partial line with a complete one use
// partialLineBytes to detect that case first.
func countShownLines(head string) int {
	if head == "" {
		return 0
	}
	lines := strings.Count(head, "\n")
	if !strings.HasSuffix(head, "\n") {
		lines++
	}
	return lines
}

// partialLineBytes returns how many bytes of a mid-line cut the head shows, or 0
// when the head is empty or ends exactly on a line boundary. The result is the
// length of the head's last, unterminated segment (the whole head when it holds
// no newline at all).
func partialLineBytes(head string) int {
	if head == "" || strings.HasSuffix(head, "\n") {
		return 0
	}
	if idx := strings.LastIndex(head, "\n"); idx >= 0 {
		return len(head) - idx - 1
	}
	return len(head)
}

// firstFailureLine returns the first content line that looks like a failure
// (error/failed/fatal/panic/exception or Chinese equivalents). Failures often
// live at the tail of a large output that the head-only fold drops entirely, so
// the truncated summary carries the earliest signal line explicitly. Returns ""
// when nothing matches.
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
