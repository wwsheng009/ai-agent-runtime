package runtimeserver

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// P0-4 改动 1/2：结果与产物读出口。
//
// 数据源优先级（plan §3.4）：
//  1. batch task：SubagentTaskRecord.ResultSummary（TaskResult JSON，兼容
//     agentresult.Result 投影）与 ArtifactRef；
//  2. 单子会话：execution run 的 ResultRef/ErrorCode（仅快照 include_results 行）；
//  3. 终态 mailbox completion payload（status/success/error/usage）兜底。
//
// 读顺序严格只读：不写任何 durable 行，不产生事件，不改变既有输出——默认
// include_results=false 或控制器 nil 时不经过这些代码路径。
const (
	// maxResultBatches bounds the per-scope batch scan of the result readers.
	maxResultBatches = 32
	// maxResultMailboxMessages bounds the completion-payload fallback scan.
	maxResultMailboxMessages = 64
	// maxRunResultLookups bounds per-row execution-run lookups in one snapshot.
	maxRunResultLookups = 32
)

// SupervisionResultSource reads bounded child results for one supervision
// scope. It is shared by the snapshot result decoration
// (ListDescendantResults) and the read_agent_result tool (LoadAgentResult).
type SupervisionResultSource struct {
	// Batches is the durable subagent batch control plane (primary source).
	Batches subagentbatch.BatchStore
	// Mailboxes is the durable team mailbox store used for the terminal
	// completion-payload fallback. nil disables the fallback.
	Mailboxes CompletionMailboxReader
}

// CompletionMailboxReader is the narrow session-mailbox read channel behind the
// completion-payload fallback. It is the same per-parent-session AgentControl
// mailbox the hosts deliver subagent completions into (chat
// SessionEventMailboxStore.AppendAgentControlMailbox), so the fallback reads
// exactly what the completion path wrote. team.MailMessage is reused as the row
// shape.
type CompletionMailboxReader interface {
	ListAgentControlMailbox(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]team.MailMessage, error)
}

// NewSupervisionResultSource builds the read-only result source. Either store
// may be nil: a host without durable batches still answers from the mailbox
// completion payload, and a host without both reports no_result_recorded.
func NewSupervisionResultSource(batches subagentbatch.BatchStore, mailboxes CompletionMailboxReader) *SupervisionResultSource {
	return &SupervisionResultSource{Batches: batches, Mailboxes: mailboxes}
}

// NewDescendantResultProvider decorates a descendant provider so
// include_results snapshots carry the bounded result projection. The returned
// provider implements both supervision.DescendantProvider and
// supervision.ResultSource; a nil base is returned unchanged so a host without
// a provider keeps behaving exactly as before.
func NewDescendantResultProvider(base supervision.DescendantProvider, source supervision.ResultSource, runs supervision.ExecutionRunStore, agents agentcontrol.AgentRegistryReader) supervision.DescendantProvider {
	if base == nil {
		return nil
	}
	return &resultDescendantProvider{base: base, source: source, runs: runs, agents: agents}
}

type resultDescendantProvider struct {
	base   supervision.DescendantProvider
	source supervision.ResultSource
	runs   supervision.ExecutionRunStore
	agents agentcontrol.AgentRegistryReader
}

func (p *resultDescendantProvider) ListDescendants(ctx context.Context, scope supervision.Scope) ([]supervision.DescendantState, error) {
	return p.base.ListDescendants(ctx, scope)
}

// ListDescendantsWithResults is the include_results read path: the base
// runtime projection plus bounded results (batch → execution run reference).
func (p *resultDescendantProvider) ListDescendantsWithResults(ctx context.Context, scope supervision.Scope) ([]supervision.DescendantState, error) {
	states, err := p.base.ListDescendants(ctx, scope)
	if err != nil {
		return nil, err
	}
	if p.source == nil || len(states) == 0 {
		return states, nil
	}
	results, err := p.source.ListDescendantResults(ctx, scope)
	if err != nil {
		return nil, err
	}
	lookups := 0
	for index := range states {
		sessionID := strings.TrimSpace(states[index].ID)
		if result, ok := results[sessionID]; ok {
			applyResultToState(&states[index], result)
			continue
		}
		if p.runs == nil || sessionID == "" || lookups >= maxRunResultLookups {
			continue
		}
		if states[index].Kind != supervision.SubjectAgentSession && states[index].Kind != supervision.SubjectAgentRun {
			continue
		}
		lookups++
		if result := p.executionRunResult(ctx, sessionID); result != nil {
			applyResultToState(&states[index], *result)
		}
	}
	return states, nil
}

// ListDescendantResults exposes the source directly (used by read_agent_result
// hosts that only hold the provider).
func (p *resultDescendantProvider) ListDescendantResults(ctx context.Context, scope supervision.Scope) (map[string]supervision.DescendantResult, error) {
	if p.source == nil {
		return nil, nil
	}
	return p.source.ListDescendantResults(ctx, scope)
}

// LoadAgentResult exposes the record lookup to read_agent_result.
func (p *resultDescendantProvider) LoadAgentResult(ctx context.Context, scope supervision.Scope, sessionID, taskID string) (supervision.AgentResultRecord, bool, error) {
	if p.source == nil {
		return supervision.AgentResultRecord{}, false, nil
	}
	record, found, err := p.source.LoadAgentResult(ctx, scope, sessionID, taskID)
	if err != nil || found {
		return record, found, err
	}
	// id may be an agent path ("/root/child"): resolve it through the
	// AgentControl graph and retry once, still inside the caller's scope.
	resolved := p.agentSessionID(ctx, scope, sessionID)
	if resolved == "" || strings.EqualFold(resolved, strings.TrimSpace(sessionID)) {
		return record, false, nil
	}
	return p.source.LoadAgentResult(ctx, scope, resolved, taskID)
}

// agentSessionID maps an agent id / session id / path inside the scope to the
// durable child session id. Best-effort: unknown targets return "".
func (p *resultDescendantProvider) agentSessionID(ctx context.Context, scope supervision.Scope, target string) string {
	target = strings.TrimSpace(target)
	rootSessionID := strings.TrimSpace(scope.RootSessionID)
	if p.agents == nil || target == "" || rootSessionID == "" {
		return ""
	}
	records, err := p.agents.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: rootSessionID,
		IncludeClosed: true,
	})
	if err != nil {
		return ""
	}
	targetPath := strings.Trim(target, "/")
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.SessionID), target) ||
			strings.EqualFold(strings.TrimSpace(record.AgentID), target) {
			return strings.TrimSpace(record.SessionID)
		}
		if targetPath != "" && strings.EqualFold(strings.Trim(record.AgentPath, "/"), targetPath) {
			return firstNonEmptyResultValue(record.SessionID, record.AgentID)
		}
	}
	return ""
}

func (p *resultDescendantProvider) executionRunResult(ctx context.Context, sessionID string) *supervision.DescendantResult {
	runs, err := p.runs.ListExecutionRunsBySession(ctx, sessionID, 1)
	if err != nil || len(runs) == 0 {
		return nil
	}
	run := runs[0]
	ref := strings.TrimSpace(run.ResultRef)
	errorClass := strings.TrimSpace(run.ErrorCode)
	if ref == "" && errorClass == "" {
		return nil
	}
	result := &supervision.DescendantResult{
		Status:     strings.TrimSpace(run.Status),
		ErrorClass: errorClass,
	}
	if ref != "" {
		result.ArtifactRefs = []string{ref}
	}
	if run.FinishedAt != nil {
		finished := run.FinishedAt.UTC()
		result.FinishedAt = &finished
	}
	return result
}

func applyResultToState(state *supervision.DescendantState, result supervision.DescendantResult) {
	if state == nil {
		return
	}
	if strings.TrimSpace(result.Status) != "" {
		state.ResultStatus = strings.TrimSpace(result.Status)
	}
	state.ResultSummary, state.ResultTruncated = supervision.BoundResultSummary(result.Summary, result.Truncated)
	state.ArtifactRefs = append(state.ArtifactRefs, supervision.BoundArtifactRefs(result.ArtifactRefs)...)
	if strings.TrimSpace(result.ErrorClass) != "" {
		state.ErrorClass = strings.TrimSpace(result.ErrorClass)
	}
	if result.FinishedAt != nil {
		finished := result.FinishedAt.UTC()
		state.FinishedAt = &finished
	}
}

// ListDescendantResults returns bounded results keyed by child session id:
// batch task records first, mailbox completion payloads for the sessions the
// batch store does not cover.
func (s *SupervisionResultSource) ListDescendantResults(ctx context.Context, scope supervision.Scope) (map[string]supervision.DescendantResult, error) {
	if s == nil {
		return nil, nil
	}
	out := make(map[string]supervision.DescendantResult)
	refs, err := s.scopeTasks(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		sessionID := taskChildSessionID(ref.task)
		if sessionID == "" {
			continue
		}
		out[sessionID] = descendantResultFromTask(ref.task)
	}
	// Mailbox fallback: best-effort only. A missing/failed mailbox read must
	// not fail the snapshot when the batch source already answered.
	for _, record := range s.completionRecords(ctx, scope) {
		sessionID := strings.TrimSpace(record.SessionID)
		if sessionID == "" {
			continue
		}
		if _, exists := out[sessionID]; exists {
			continue
		}
		out[sessionID] = descendantResultFromRecord(record)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// LoadAgentResult resolves one target: batch task TaskResult first, terminal
// mailbox completion payload second, found=false ("no_result_recorded")
// otherwise.
func (s *SupervisionResultSource) LoadAgentResult(ctx context.Context, scope supervision.Scope, sessionID, taskID string) (supervision.AgentResultRecord, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	taskID = strings.TrimSpace(taskID)
	if s == nil || (sessionID == "" && taskID == "") {
		return supervision.AgentResultRecord{}, false, nil
	}
	if s.Batches != nil {
		refs, err := s.scopeTasks(ctx, scope)
		if err != nil {
			return supervision.AgentResultRecord{}, false, err
		}
		for _, ref := range refs {
			if taskID != "" && !strings.EqualFold(strings.TrimSpace(ref.task.TaskID), taskID) {
				continue
			}
			record, ok := loadBatchTaskResult(ref.task)
			if !ok {
				continue
			}
			if sessionID != "" && !strings.EqualFold(strings.TrimSpace(record.SessionID), sessionID) {
				continue
			}
			return record, true, nil
		}
	}
	for _, record := range s.completionRecords(ctx, scope) {
		if sessionID != "" && !strings.EqualFold(strings.TrimSpace(record.SessionID), sessionID) {
			continue
		}
		if taskID != "" && !strings.EqualFold(strings.TrimSpace(record.TaskID), taskID) {
			continue
		}
		return record, true, nil
	}
	return supervision.AgentResultRecord{}, false, nil
}

// batchTaskRef pairs a task with its batch for scope-bound reads.
type batchTaskRef struct {
	batch subagentbatch.SubagentBatch
	task  subagentbatch.SubagentTaskRecord
}

// scopeTasks lists the scope's batch tasks with two bounded reads: one keyed
// by parent session, one by durable root scope (team lead), deduped by batch.
func (s *SupervisionResultSource) scopeTasks(ctx context.Context, scope supervision.Scope) ([]batchTaskRef, error) {
	if s == nil || s.Batches == nil {
		return nil, nil
	}
	batches := make([]subagentbatch.SubagentBatch, 0, maxResultBatches)
	seen := make(map[string]bool)
	appendBatches := func(list []subagentbatch.SubagentBatch) {
		for _, batch := range list {
			batchID := strings.TrimSpace(batch.BatchID)
			if batchID == "" || seen[batchID] {
				continue
			}
			seen[batchID] = true
			batches = append(batches, batch)
		}
	}
	if parentSessionID := strings.TrimSpace(scope.RootSessionID); parentSessionID != "" {
		list, err := s.Batches.ListBatches(ctx, subagentbatch.BatchFilter{
			ParentSessionID: parentSessionID,
			Limit:           maxResultBatches,
		})
		if err != nil {
			return nil, err
		}
		appendBatches(list)
	}
	if rootScopeID := strings.TrimSpace(scope.RootTeamID); rootScopeID != "" {
		list, err := s.Batches.ListBatches(ctx, subagentbatch.BatchFilter{
			RootScopeID: rootScopeID,
			Limit:       maxResultBatches,
		})
		if err != nil {
			return nil, err
		}
		appendBatches(list)
	}
	refs := make([]batchTaskRef, 0, len(batches))
	for _, batch := range batches {
		tasks, err := s.Batches.ListTasks(ctx, batch.BatchID)
		if err != nil {
			return nil, err
		}
		for _, task := range tasks {
			refs = append(refs, batchTaskRef{batch: batch, task: task})
		}
	}
	return refs, nil
}

// completionRecords reads the caller session's terminal completion mailboxes
// and normalizes them to result records. The read is keyed by the scope's own
// parent session (the mailbox is per-session), with the durable
// parent_session_id / root_scope_id metadata re-checked as defense in depth.
func (s *SupervisionResultSource) completionRecords(ctx context.Context, scope supervision.Scope) []supervision.AgentResultRecord {
	parentSessionID := strings.TrimSpace(scope.RootSessionID)
	if s == nil || s.Mailboxes == nil || parentSessionID == "" {
		return nil
	}
	messages, err := s.Mailboxes.ListAgentControlMailbox(ctx, parentSessionID, 0, maxResultMailboxMessages)
	if err != nil {
		return nil
	}
	rootScopeID := strings.TrimSpace(scope.RootTeamID)
	if rootScopeID == "" {
		rootScopeID = parentSessionID
	}
	out := make([]supervision.AgentResultRecord, 0, len(messages))
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Kind), agentcontrol.MailboxKindSubagentCompleted) {
			continue
		}
		messageParentSessionID := mailboxString(message.Metadata, "parent_session_id")
		messageScopeID := mailboxString(message.Metadata, "root_scope_id")
		inScope := false
		switch {
		case messageParentSessionID != "" && strings.EqualFold(messageParentSessionID, parentSessionID):
			inScope = true
		case messageScopeID != "" && rootScopeID != "" && strings.EqualFold(messageScopeID, rootScopeID):
			inScope = true
		case messageParentSessionID == "" && messageScopeID == "":
			// Legacy rows without scope metadata: the mailbox itself is keyed by
			// the parent session, which is already the scope boundary.
			inScope = true
		}
		if !inScope {
			continue
		}
		record, ok := completionRecord(message)
		if !ok {
			continue
		}
		out = append(out, record)
	}
	return out
}

// loadBatchTaskResult decodes one durable task result capsule. A terminal task
// with a recorded artifact ref or error class counts as a result even when the
// inline capsule is empty.
func loadBatchTaskResult(task subagentbatch.SubagentTaskRecord) (supervision.AgentResultRecord, bool) {
	record := batchTaskRecord(task)
	if len(task.ResultSummary) == 0 && strings.TrimSpace(task.ArtifactRef) == "" && strings.TrimSpace(task.ErrorClass) == "" {
		return record, false
	}
	return record, true
}

func batchTaskRecord(task subagentbatch.SubagentTaskRecord) supervision.AgentResultRecord {
	record := supervision.AgentResultRecord{
		Source:     supervision.ResultSourceTaskResult,
		TaskID:     strings.TrimSpace(task.TaskID),
		SessionID:  strings.TrimSpace(task.ChildSessionID),
		Status:     taskStatusText(task.Status),
		FinishedAt: task.FinishedAt,
	}
	capsule, contract := decodeStoredResult(task.ResultSummary)
	switch {
	case contract != nil:
		record.Summary = strings.TrimSpace(contract.Summary)
		if status := strings.TrimSpace(string(contract.Status)); status != "" {
			record.Status = status
		}
		for _, finding := range contract.Findings {
			if summary := strings.TrimSpace(finding.Summary); summary != "" {
				record.Findings = append(record.Findings, summary)
			}
		}
		for _, change := range contract.Changes {
			record.Changes = append(record.Changes, supervision.AgentResultChange{
				Path:         strings.TrimSpace(change.Path),
				Summary:      strings.TrimSpace(change.Summary),
				Status:       strings.TrimSpace(change.Status),
				ArtifactRefs: append([]string(nil), change.ArtifactRefs...),
			})
		}
		for _, artifact := range contract.Artifacts {
			if ref := firstNonEmptyResultValue(artifact.URI, artifact.ID); ref != "" {
				record.Artifacts = append(record.Artifacts, ref)
			}
		}
		for _, item := range contract.Errors {
			record.Errors = append(record.Errors, supervision.AgentResultError{
				Code:       strings.TrimSpace(item.Code),
				Message:    strings.TrimSpace(item.Message),
				Retryable:  item.Retryable,
				NextAction: strings.TrimSpace(item.NextAction),
			})
		}
		record.Usage = supervision.AgentResultUsage{
			InputTokens:  contract.Usage.InputTokens,
			OutputTokens: contract.Usage.OutputTokens,
			TotalTokens:  contract.Usage.TotalTokens,
			ToolCalls:    contract.Usage.ToolCalls,
			DurationMS:   contract.Usage.DurationMS,
		}
		record.Success = contract.Status == agentresult.StatusSucceeded || contract.Status == agentresult.StatusPartiallyCompleted
	case capsule != nil:
		record.Success = capsule.Success
		record.Summary = strings.TrimSpace(capsule.Summary)
		for _, finding := range capsule.Findings {
			if finding = strings.TrimSpace(finding); finding != "" {
				record.Findings = append(record.Findings, finding)
			}
		}
		for _, patch := range capsule.Patches {
			record.Changes = append(record.Changes, supervision.AgentResultChange{
				Path:         strings.TrimSpace(patch.Path),
				Summary:      strings.TrimSpace(patch.Summary),
				Status:       strings.TrimSpace(patch.ApplyStatus),
				ArtifactRefs: append([]string(nil), patch.ArtifactRefs...),
			})
		}
		if capsule.UsageTotal > 0 {
			record.Usage.TotalTokens = capsule.UsageTotal
		}
		if errorText := strings.TrimSpace(capsule.Error); errorText != "" {
			record.Errors = append(record.Errors, supervision.AgentResultError{
				Code:    strings.TrimSpace(task.ErrorCode),
				Message: errorText,
			})
		}
		if ref := strings.TrimSpace(capsule.ArtifactRef); ref != "" {
			record.Artifacts = append(record.Artifacts, ref)
		}
	}
	if ref := strings.TrimSpace(task.ArtifactRef); ref != "" {
		record.Artifacts = append(record.Artifacts, ref)
	}
	if record.SessionID == "" && capsule != nil {
		// Production note (2026-09-17): SubagentTaskRecord.ChildSessionID has no
		// writer in the batch coordinator; the per-task capsule carries the child
		// session id (SubagentResult.SessionID → TaskResult.SessionID).
		record.SessionID = strings.TrimSpace(capsule.SessionID)
	}
	if len(record.Errors) == 0 && strings.TrimSpace(task.ErrorClass) != "" {
		record.Errors = append(record.Errors, supervision.AgentResultError{
			Code:    strings.TrimSpace(task.ErrorCode),
			Message: strings.TrimSpace(task.ErrorClass),
		})
	}
	if record.Status == "" && record.Success {
		record.Status = string(agentresult.StatusSucceeded)
	}
	if record.Status == "" && (len(record.Errors) > 0 || task.Status.Terminal()) && !record.Success {
		record.Status = taskStatusText(task.Status)
	}
	return record
}

// taskChildSessionID resolves the child session id of one durable task: the
// explicit column when a host recorded it, otherwise the id inside the
// TaskResult capsule (see batchTaskRecord).
func taskChildSessionID(task subagentbatch.SubagentTaskRecord) string {
	if id := strings.TrimSpace(task.ChildSessionID); id != "" {
		return id
	}
	if capsule, _ := decodeStoredResult(task.ResultSummary); capsule != nil {
		return strings.TrimSpace(capsule.SessionID)
	}
	return ""
}

func descendantResultFromTask(task subagentbatch.SubagentTaskRecord) supervision.DescendantResult {
	record := batchTaskRecord(task)
	result := descendantResultFromRecord(record)
	if result.Status == "" {
		result.Status = taskStatusText(task.Status)
	}
	if result.ErrorClass == "" {
		result.ErrorClass = strings.TrimSpace(task.ErrorClass)
	}
	return result
}

func descendantResultFromRecord(record supervision.AgentResultRecord) supervision.DescendantResult {
	summary, truncated := supervision.BoundResultSummary(record.Summary, false)
	result := supervision.DescendantResult{
		Status:     strings.TrimSpace(record.Status),
		Summary:    summary,
		Truncated:  truncated,
		ErrorClass: "",
		FinishedAt: record.FinishedAt,
	}
	refs := append([]string(nil), record.Artifacts...)
	for _, change := range record.Changes {
		refs = append(refs, change.ArtifactRefs...)
	}
	result.ArtifactRefs = supervision.BoundArtifactRefs(refs)
	if len(record.Errors) > 0 {
		result.ErrorClass = firstNonEmptyResultValue(record.Errors[0].Code, record.Errors[0].Message)
	}
	return result
}

// completionRecord normalizes a terminal mailbox completion payload
// (BuildSubagentCompletionMailboxMessage status/success/error/usage metadata).
func completionRecord(message team.MailMessage) (supervision.AgentResultRecord, bool) {
	metadata := message.Metadata
	if len(metadata) == 0 {
		return supervision.AgentResultRecord{}, false
	}
	record := supervision.AgentResultRecord{
		Source:    supervision.ResultSourceCompletionPayload,
		SessionID: mailboxString(metadata, "session_id"),
		TaskID:    mailboxString(metadata, "task_id"),
		Status:    mailboxString(metadata, "status"),
	}
	if success, ok := metadata["success"].(bool); ok {
		record.Success = success
		if record.Status == "" {
			if success {
				record.Status = string(agentresult.StatusSucceeded)
			} else {
				record.Status = string(agentresult.StatusFailed)
			}
		}
	}
	if errorText := mailboxString(metadata, "error"); errorText != "" {
		record.Errors = append(record.Errors, supervision.AgentResultError{Message: errorText})
		record.Summary = errorText
	} else if body := strings.TrimSpace(message.Body); body != "" {
		record.Summary = body
	}
	usage := supervision.AgentResultUsage{
		InputTokens:  mailboxInt(metadata, "usage_prompt_tokens"),
		OutputTokens: mailboxInt(metadata, "usage_completion_tokens"),
		TotalTokens:  mailboxInt(metadata, "usage_total_tokens"),
	}
	if usage != (supervision.AgentResultUsage{}) {
		record.Usage = usage
	}
	if !message.CreatedAt.IsZero() {
		createdAt := message.CreatedAt.UTC()
		record.FinishedAt = &createdAt
	}
	if record.SessionID == "" && record.TaskID == "" && record.Status == "" && len(record.Errors) == 0 {
		return supervision.AgentResultRecord{}, false
	}
	return record, true
}

// decodeStoredResult reads the inline durable capsule, accepting both the
// TaskResult shape and a projected agentresult.Result JSON.
func decodeStoredResult(raw []byte) (*subagentbatch.TaskResult, *agentresult.Result) {
	if len(raw) == 0 {
		return nil, nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, nil
	}
	if _, ok := probe["status"]; ok {
		var contract agentresult.Result
		if err := json.Unmarshal(raw, &contract); err == nil && strings.TrimSpace(contract.Summary) != "" {
			return nil, &contract
		}
	}
	var capsule subagentbatch.TaskResult
	if err := json.Unmarshal(raw, &capsule); err == nil {
		return &capsule, nil
	}
	return nil, nil
}

func taskStatusText(status subagentbatch.TaskStatus) string {
	switch status {
	case subagentbatch.TaskSucceeded:
		return string(agentresult.StatusSucceeded)
	case subagentbatch.TaskFailed:
		return string(agentresult.StatusFailed)
	case subagentbatch.TaskCanceled:
		return string(agentresult.StatusCanceled)
	case subagentbatch.TaskTimedOut:
		return string(agentresult.StatusTimedOut)
	case subagentbatch.TaskSkipped:
		return "skipped"
	default:
		return ""
	}
}

func mailboxString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	switch value := metadata[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	case time.Time:
		return value.UTC().Format(time.RFC3339)
	default:
		return ""
	}
}

func mailboxInt(metadata map[string]interface{}, key string) int {
	if len(metadata) == 0 {
		return 0
	}
	switch value := metadata[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	default:
		return 0
	}
}

func firstNonEmptyResultValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
