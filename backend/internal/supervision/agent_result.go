package supervision

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// P0-4 结果读出口的共享契约（plan §3.4 改动 1/2）。
//
// 边界值集中在这里，provider（snapshot 行）与 read_agent_result（工具输出）
// 使用同一组预算，避免两个出口各自漂移。
const (
	// MaxSnapshotResultSummaryRunes bounds one snapshot row's result_summary:
	// carried per row, so it must stay small (plan §3.4 改动 1: ≤512 rune).
	MaxSnapshotResultSummaryRunes = 512
	// MaxSnapshotResultArtifactRefs bounds artifact_refs per snapshot row.
	MaxSnapshotResultArtifactRefs = 3
	// MaxSnapshotArtifactRefChars bounds one artifact ref string.
	MaxSnapshotArtifactRefChars = 256

	// DefaultReadResultMaxChars is read_agent_result's default output budget.
	DefaultReadResultMaxChars = 4000
	// MinReadResultMaxChars keeps the structured status/next_action readable
	// even when a caller passes a tiny max_chars.
	MinReadResultMaxChars = 256
	// MaxReadResultMaxChars caps a caller-supplied budget so one tool call
	// cannot return an unbounded payload.
	MaxReadResultMaxChars = 20000

	// Read-result section caps (plan §3.4 改动 2).
	MaxReadResultFindings  = 3
	MaxReadResultChanges   = 8
	MaxReadResultArtifacts = 8
	MaxReadResultErrors    = 3

	// read_agent_result source vocabulary.
	ResultSourceTaskResult        = "task_result"
	ResultSourceCompletionPayload = "completion_payload"
	ResultSourceNone              = "none"

	// ReadResultStatusPendingBinding is the status of a task that is durable but
	// has not bound a child session / produced a result capsule yet. It is a
	// distinct status rather than an error so the caller can wait instead of
	// treating the target as missing.
	ReadResultStatusPendingBinding = "pending_binding"
)

// DescendantResult is the bounded result projection attached to one snapshot
// row (plan §3.4 改动 1). Providers fill it; BuildSnapshot re-applies the
// bounds before exposing a row.
type DescendantResult struct {
	Status       string
	Summary      string
	Truncated    bool
	ArtifactRefs []string
	ErrorClass   string
	FinishedAt   *time.Time
}

// AgentResultChange is one normalized change entry (TaskResult.Patches or
// agentresult.Change projection): path + status + artifact refs, per plan §3.4
// 改动 3 ("changes(status/artifact_refs)").
type AgentResultChange struct {
	Path         string   `json:"path,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	Status       string   `json:"status,omitempty"`
	ArtifactRefs []string `json:"artifact_refs,omitempty"`
}

// AgentResultError is one normalized error entry.
type AgentResultError struct {
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
	Retryable  bool   `json:"retryable,omitempty"`
	NextAction string `json:"next_action,omitempty"`
}

// AgentResultUsage mirrors agentresult.Usage with omitempty semantics so an
// unused usage block disappears from the bounded output.
type AgentResultUsage struct {
	InputTokens  int   `json:"input_tokens,omitempty"`
	OutputTokens int   `json:"output_tokens,omitempty"`
	TotalTokens  int   `json:"total_tokens,omitempty"`
	ToolCalls    int   `json:"tool_calls,omitempty"`
	DurationMS   int64 `json:"duration_ms,omitempty"`
}

// AgentResultRecord is the normalized, still-unbounded record a ResultSource
// returns for one child session / batch task. BuildReadResultPayload applies
// the section and size budgets.
type AgentResultRecord struct {
	// Source is task_result | completion_payload.
	Source     string
	TaskID     string
	SessionID  string
	Status     string
	Success    bool
	Summary    string
	Findings   []string
	Changes    []AgentResultChange
	Artifacts  []string
	Errors     []AgentResultError
	Usage      AgentResultUsage
	FinishedAt *time.Time
}

// ResultSource reads bounded child results for one supervision scope (P0-4).
// Scope always comes from the host's own resolution; a model can never widen
// it, so implementations must treat scope as the only query root.
type ResultSource interface {
	// ListDescendantResults returns bounded results keyed by child session id
	// for the scope's descendants. Best-effort: missing rows are absent.
	ListDescendantResults(ctx context.Context, scope Scope) (map[string]DescendantResult, error)
	// LoadAgentResult returns the full (still bounded by the caller) record for
	// one child session id or batch task id. found=false means
	// no_result_recorded.
	LoadAgentResult(ctx context.Context, scope Scope, sessionID, taskID string) (AgentResultRecord, bool, error)
}

// applySnapshotResult copies one provider-side projection onto a snapshot row,
// re-applying the row bounds so the read model cannot be widened by a provider.
func applySnapshotResult(item *SnapshotItem, state DescendantState) {
	if item == nil {
		return
	}
	item.ResultStatus = strings.TrimSpace(state.ResultStatus)
	item.ResultSummary, item.ResultTruncated = BoundResultSummary(state.ResultSummary, state.ResultTruncated)
	item.ArtifactRefs = BoundArtifactRefs(state.ArtifactRefs)
	item.ErrorClass = strings.TrimSpace(state.ErrorClass)
	if state.FinishedAt != nil {
		finished := state.FinishedAt.UTC()
		item.FinishedAt = &finished
	}
}

// BoundResultSummary truncates a result summary to the snapshot row budget
// (≤512 rune) and reports whether anything was cut.
func BoundResultSummary(summary string, truncated bool) (string, bool) {
	summary = strings.TrimSpace(summary)
	runes := []rune(summary)
	if len(runes) <= MaxSnapshotResultSummaryRunes {
		return summary, truncated
	}
	return string(runes[:MaxSnapshotResultSummaryRunes]), true
}

// BoundArtifactRefs trims, dedupes and caps artifact refs (≤3 items, each
// ≤256 runes) for one snapshot row.
func BoundArtifactRefs(refs []string) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, MaxSnapshotResultArtifactRefs)
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		if runes := []rune(ref); len(runes) > MaxSnapshotArtifactRefChars {
			ref = string(runes[:MaxSnapshotArtifactRefChars])
		}
		out = append(out, ref)
		if len(out) >= MaxSnapshotResultArtifactRefs {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ReadResultArgs is the parsed read_agent_result input after the toolbroker
// and the host controller agree on it.
type ReadResultArgs struct {
	SessionID string
	TaskID    string
	Sections  []string
	MaxChars  int
	// Offset/Limit page the summary text in runes, with artifact_read's
	// offset/limit/eof semantics (H4). Both zero keeps the bounded default
	// view (MaxSnapshotResultSummaryRunes) but still reports how to page the
	// remainder, so the 512-rune cap is a default view instead of the only
	// exit.
	Offset int
	Limit  int
}

// ReadResultSection vocabulary.
const (
	ReadResultSectionSummary = "summary"
	// ReadResultSectionOutput is the alias the model most often reaches for
	// when it wants the deliverable body. It resolves to summary instead of
	// being rejected (H1: sections=["output"] failed the whole call).
	ReadResultSectionOutput    = "output"
	ReadResultSectionFindings  = "findings"
	ReadResultSectionChanges   = "changes"
	ReadResultSectionArtifacts = "artifacts"
	ReadResultSectionErrors    = "errors"
	ReadResultSectionUsage     = "usage"
)

// ReadResultPayload is the bounded, model-facing read_agent_result output
// (plan §3.4 改动 2). Sections not requested by the caller are omitted.
type ReadResultPayload struct {
	SessionID  string              `json:"session_id,omitempty"`
	TaskID     string              `json:"task_id,omitempty"`
	Status     string              `json:"status,omitempty"`
	Summary    string              `json:"summary,omitempty"`
	Findings   []string            `json:"findings,omitempty"`
	Changes    []AgentResultChange `json:"changes,omitempty"`
	Artifacts  []string            `json:"artifacts,omitempty"`
	Errors     []AgentResultError  `json:"errors,omitempty"`
	Usage      *AgentResultUsage   `json:"usage,omitempty"`
	Truncated  bool                `json:"truncated,omitempty"`
	Source     string              `json:"source"`
	ErrorCode  string              `json:"error_code,omitempty"`
	NextAction string              `json:"next_action,omitempty"`
	// ArtifactNextActions carries one artifact_read(id=...) dereference per
	// entry in Artifacts, so an artifacts-only read is actionable instead of
	// a list of opaque refs (H4).
	ArtifactNextActions []string `json:"artifact_next_actions,omitempty"`
	// Offset/Limit/EOF/NextOffset/TotalRunes expose summary pagination with
	// artifact_read's semantics (H4). EOF is nil unless a summary was read.
	Offset     int   `json:"offset,omitempty"`
	Limit      int   `json:"limit,omitempty"`
	EOF        *bool `json:"eof,omitempty"`
	NextOffset int   `json:"next_offset,omitempty"`
	TotalRunes int   `json:"total_runes,omitempty"`
	// ResultAvailable reports that a failed/canceled task still carries a
	// usable deliverable, so the parent must read it instead of re-dispatching
	// (H10).
	ResultAvailable bool `json:"result_available,omitempty"`
	DoNotRetry      bool `json:"do_not_retry,omitempty"`
	// FinishedAt is the durable completion time when the source recorded one.
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// NormalizeReadResultSections validates and normalizes the sections argument.
// Empty means "all sections" (the tool default). An unknown section is
// rejected instead of silently ignored: a typo must not turn a filtered read
// into a full one (or vice versa).
func NormalizeReadResultSections(sections []string) ([]string, error) {
	if len(sections) == 0 {
		return nil, nil
	}
	valid := map[string]bool{
		ReadResultSectionSummary:   true,
		ReadResultSectionOutput:    true,
		ReadResultSectionFindings:  true,
		ReadResultSectionChanges:   true,
		ReadResultSectionArtifacts: true,
		ReadResultSectionErrors:    true,
		ReadResultSectionUsage:     true,
	}
	out := make([]string, 0, len(sections))
	seen := make(map[string]bool, len(sections))
	for _, section := range sections {
		section = strings.ToLower(strings.TrimSpace(section))
		if section == "" {
			continue
		}
		if !valid[section] {
			return nil, &UnknownReadResultSectionError{Section: section}
		}
		// "output" is an accepted alias for the deliverable body; normalize it
		// so every downstream switch keeps a single canonical key.
		if section == ReadResultSectionOutput {
			section = ReadResultSectionSummary
		}
		if seen[section] {
			continue
		}
		seen[section] = true
		out = append(out, section)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// UnknownReadResultSectionError reports one unsupported sections entry.
type UnknownReadResultSectionError struct {
	Section string
}

func (e *UnknownReadResultSectionError) Error() string {
	return "unsupported section " + e.Section + " (want summary|output|findings|changes|artifacts|errors|usage)"
}

// NextAction states the single next step for a rejected sections value, so the
// model can recover in one turn instead of retrying the same call (main plan
// P1-7 collaboration guidance).
func (e *UnknownReadResultSectionError) NextAction() string {
	return "retry subagent_inspect_task with sections=[\"summary\"] (alias: \"output\") for the deliverable body, " +
		"or sections=[\"artifacts\"] then artifact_read(id=<ref>, offset, limit) to page a large artifact"
}

// ReadResultMaxChars resolves the caller budget: zero falls back to the
// default, everything else is clamped into [MinReadResultMaxChars,
// MaxReadResultMaxChars].
func ReadResultMaxChars(requested int) int {
	if requested <= 0 {
		return DefaultReadResultMaxChars
	}
	if requested < MinReadResultMaxChars {
		return MinReadResultMaxChars
	}
	if requested > MaxReadResultMaxChars {
		return MaxReadResultMaxChars
	}
	return requested
}

// NoResultRecordedPayload is the structured "nothing durable was recorded"
// answer: it stays a successful read (source=none) so the model can follow
// next_action instead of retrying the same call.
func NoResultRecordedPayload(sessionID, taskID string) ReadResultPayload {
	return ReadResultPayload{
		SessionID: strings.TrimSpace(sessionID),
		TaskID:    strings.TrimSpace(taskID),
		Source:    ResultSourceNone,
		ErrorCode: "no_result_recorded",
		NextAction: "no durable result is recorded for this target yet: call read_agent_events " +
			"(or wait_agent) to observe the child, then retry subagent_inspect_task once it reaches a terminal state",
	}
}

// BuildReadResultPayload renders one normalized record into the bounded
// read_agent_result payload: section selection, count caps and a total
// max_chars budget, with truncated=true whenever anything was cut.
func BuildReadResultPayload(record AgentResultRecord, args ReadResultArgs) ReadResultPayload {
	sections := sectionSet(args.Sections)
	wants := func(name string) bool { return sections[""] || sections[name] }
	payload := ReadResultPayload{
		SessionID: strings.TrimSpace(firstNonEmpty(record.SessionID, args.SessionID)),
		TaskID:    strings.TrimSpace(firstNonEmpty(record.TaskID, args.TaskID)),
		Source:    normalizeResultSource(record.Source),
	}
	if record.FinishedAt != nil {
		finished := record.FinishedAt.UTC()
		payload.FinishedAt = &finished
	}
	status := strings.TrimSpace(record.Status)
	if status == "" {
		switch {
		case record.Success:
			status = "succeeded"
		case len(record.Errors) > 0 || strings.TrimSpace(record.Summary) == "":
			status = "failed"
		}
	}
	payload.Status = status
	// P1-1 / H7: a task that exists but whose child-session binding (or result)
	// is not durable yet is not "missing". Without this branch the model sees a
	// successful read with no status and falls back to re-dispatching work that
	// is already in flight.
	if status == ReadResultStatusPendingBinding {
		payload.ErrorCode = ReadResultStatusPendingBinding
		payload.NextAction = "the task is still in flight and no durable result exists yet: " +
			"call wait_agent (or read_agent_events) to observe it, then retry subagent_inspect_task after it settles; " +
			"do not re-dispatch the same task"
	}

	if wants(ReadResultSectionSummary) {
		applyReadResultSummaryPage(&payload, record.Summary, args)
	}
	if wants(ReadResultSectionFindings) {
		for _, finding := range record.Findings {
			if len(payload.Findings) >= MaxReadResultFindings {
				payload.Truncated = true
				break
			}
			finding = strings.TrimSpace(finding)
			if finding == "" {
				continue
			}
			text, cut := truncateReadResultText(finding, MaxSnapshotResultSummaryRunes, false)
			payload.Truncated = payload.Truncated || cut
			payload.Findings = append(payload.Findings, text)
		}
	}
	if wants(ReadResultSectionChanges) {
		for _, change := range record.Changes {
			if len(payload.Changes) >= MaxReadResultChanges {
				payload.Truncated = true
				break
			}
			entry := AgentResultChange{
				Path:         strings.TrimSpace(change.Path),
				Status:       strings.TrimSpace(change.Status),
				ArtifactRefs: BoundArtifactRefs(change.ArtifactRefs),
			}
			var cut bool
			entry.Summary, cut = truncateReadResultText(change.Summary, MaxSnapshotResultSummaryRunes, false)
			payload.Truncated = payload.Truncated || cut
			if entry.Path == "" && entry.Summary == "" && entry.Status == "" && len(entry.ArtifactRefs) == 0 {
				continue
			}
			payload.Changes = append(payload.Changes, entry)
		}
	}
	if wants(ReadResultSectionArtifacts) {
		refs := BoundReadResultArtifacts(record.Artifacts)
		if len(refs) > 0 {
			payload.Artifacts = refs
			payload.ArtifactNextActions = artifactDereferenceActions(refs)
		}
	}
	if wants(ReadResultSectionErrors) {
		for _, item := range record.Errors {
			if len(payload.Errors) >= MaxReadResultErrors {
				payload.Truncated = true
				break
			}
			entry := AgentResultError{
				Code:       strings.TrimSpace(item.Code),
				Retryable:  item.Retryable,
				NextAction: strings.TrimSpace(item.NextAction),
			}
			var cut bool
			entry.Message, cut = truncateReadResultText(item.Message, MaxSnapshotResultSummaryRunes, false)
			payload.Truncated = payload.Truncated || cut
			if entry.Code == "" && entry.Message == "" && entry.NextAction == "" {
				continue
			}
			payload.Errors = append(payload.Errors, entry)
		}
	}
	if wants(ReadResultSectionUsage) {
		usage := record.Usage
		if usage != (AgentResultUsage{}) {
			payload.Usage = &usage
		}
	}
	applyFailedWithResultGuidance(&payload, record)
	enforceReadResultBudget(&payload, ReadResultMaxChars(args.MaxChars))
	return payload
}

// applyReadResultSummaryPage renders the summary either as the bounded default
// view or as one explicit offset/limit page. Both modes report eof/next_offset
// so the caller always learns how to reach the rest of the deliverable (H4:
// the 512-rune cap used to be the only exit and the remainder was unreachable).
func applyReadResultSummaryPage(payload *ReadResultPayload, summary string, args ReadResultArgs) {
	if payload == nil {
		return
	}
	summary = strings.TrimSpace(summary)
	total := utf8.RuneCountInString(summary)
	payload.TotalRunes = total
	paging := args.Offset > 0 || args.Limit > 0
	if !paging {
		payload.Summary, payload.Truncated = truncateReadResultText(summary, MaxSnapshotResultSummaryRunes, payload.Truncated)
		eof := total <= MaxSnapshotResultSummaryRunes
		payload.EOF = &eof
		payload.Offset = 0
		payload.Limit = MaxSnapshotResultSummaryRunes
		if !eof {
			payload.NextOffset = MaxSnapshotResultSummaryRunes
		}
		return
	}
	offset := args.Offset
	if offset < 0 {
		offset = 0
	}
	limit := args.Limit
	if limit <= 0 {
		limit = DefaultReadResultMaxChars
	}
	runes := []rune(summary)
	if offset > len(runes) {
		offset = len(runes)
	}
	end := offset + limit
	if end > len(runes) {
		end = len(runes)
	}
	payload.Summary = strings.TrimSpace(string(runes[offset:end]))
	payload.Offset = offset
	payload.Limit = limit
	eof := end >= len(runes)
	payload.EOF = &eof
	if !eof {
		payload.NextOffset = end
		payload.Truncated = true
	}
}

// artifactDereferenceActions turns each artifact ref into the concrete
// artifact_read call that resolves it, so an artifacts-only read is a pointer
// the model can follow instead of a dead-end list (H4).
func artifactDereferenceActions(refs []string) []string {
	if len(refs) == 0 {
		return nil
	}
	actions := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		actions = append(actions, "artifact_read(id="+ref+", offset=0, limit=20000)")
	}
	if len(actions) == 0 {
		return nil
	}
	return actions
}

// applyFailedWithResultGuidance marks a failed/canceled read that still carries
// a deliverable. Without it the parent sees a bare failure status and redoes
// work that already exists (H10: 6817/6113-character deliverables were hidden
// behind status=failed).
func applyFailedWithResultGuidance(payload *ReadResultPayload, record AgentResultRecord) {
	if payload == nil {
		return
	}
	if record.Success {
		return
	}
	hasDeliverable := strings.TrimSpace(record.Summary) != "" || len(record.Findings) > 0 || len(record.Artifacts) > 0
	if !hasDeliverable {
		return
	}
	payload.ResultAvailable = true
	payload.DoNotRetry = true
	note := "⚠ this task reported failure but already produced a deliverable: read the summary/artifacts above " +
		"and do NOT re-dispatch the same task"
	if payload.NextAction == "" {
		payload.NextAction = note
	} else {
		payload.NextAction = note + "; " + payload.NextAction
	}
}

// BoundReadResultArtifacts caps the read tool's artifacts list (≤8 items, each
// ≤256 runes) and reports nothing when the record carried none.
func BoundReadResultArtifacts(refs []string) []string {
	if len(refs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(refs))
	out := make([]string, 0, MaxReadResultArtifacts)
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		if runes := []rune(ref); len(runes) > MaxSnapshotArtifactRefChars {
			ref = string(runes[:MaxSnapshotArtifactRefChars])
		}
		out = append(out, ref)
		if len(out) >= MaxReadResultArtifacts {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sectionSet(sections []string) map[string]bool {
	set := make(map[string]bool, len(sections)+1)
	if len(sections) == 0 {
		set[""] = true
		return set
	}
	for _, section := range sections {
		section = strings.ToLower(strings.TrimSpace(section))
		// Accept the "output" alias even when a caller bypassed
		// NormalizeReadResultSections, so the alias can never silently select
		// an empty section set.
		if section == ReadResultSectionOutput {
			section = ReadResultSectionSummary
		}
		set[section] = true
	}
	return set
}

func normalizeResultSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case ResultSourceTaskResult:
		return ResultSourceTaskResult
	case ResultSourceCompletionPayload:
		return ResultSourceCompletionPayload
	case "", ResultSourceNone:
		return ResultSourceNone
	default:
		return strings.TrimSpace(source)
	}
}

// truncateReadResultText applies a rune budget with a "..." marker so the model
// can see that the value was cut (the boolean is the machine-readable signal).
func truncateReadResultText(text string, limit int, truncated bool) (string, bool) {
	text = strings.TrimSpace(text)
	if limit <= 0 {
		return text, truncated
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text, truncated
	}
	keep := limit - 3
	if keep < 0 {
		keep = 0
	}
	return strings.TrimSpace(string(runes[:keep])) + "...", true
}

// enforceReadResultBudget keeps the serialized payload within max_chars. It
// trims the summary first (the dominant field) and then drops trailing list
// entries; the status/source/next_action skeleton is never dropped.
func enforceReadResultBudget(payload *ReadResultPayload, maxChars int) {
	if payload == nil || maxChars <= 0 {
		return
	}
	if jsonRuneLen(payload) <= maxChars {
		return
	}
	for jsonRuneLen(payload) > maxChars && payload.Summary != "" {
		excess := jsonRuneLen(payload) - maxChars + 8
		runes := []rune(payload.Summary)
		if excess >= len(runes) {
			payload.Summary = ""
		} else {
			payload.Summary = strings.TrimSpace(string(runes[:len(runes)-excess]))
		}
		payload.Truncated = true
	}
	for jsonRuneLen(payload) > maxChars {
		if !dropTrailingReadResultEntry(payload) {
			break
		}
		payload.Truncated = true
	}
}

func dropTrailingReadResultEntry(payload *ReadResultPayload) bool {
	switch {
	case len(payload.Findings) > 0:
		payload.Findings = payload.Findings[:len(payload.Findings)-1]
	case len(payload.Changes) > 0:
		payload.Changes = payload.Changes[:len(payload.Changes)-1]
	case len(payload.Artifacts) > 0:
		payload.Artifacts = payload.Artifacts[:len(payload.Artifacts)-1]
		// ArtifactNextActions is the parallel dereference list: dropping one
		// without the other would leave pointers that name no ref.
		if len(payload.ArtifactNextActions) > len(payload.Artifacts) {
			payload.ArtifactNextActions = payload.ArtifactNextActions[:len(payload.Artifacts)]
		}
	case len(payload.Errors) > 0:
		payload.Errors = payload.Errors[:len(payload.Errors)-1]
	case payload.Usage != nil:
		payload.Usage = nil
	default:
		return false
	}
	return true
}

func jsonRuneLen(value interface{}) int {
	raw, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return utf8.RuneCount(raw)
}
