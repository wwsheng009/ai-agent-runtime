package supervision

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// C2-1（改动 #4）：obligation 账本投影。
//
// resume 上下文要回答两个问题（§16.4）：`pending_count` 是多少（I1 的收尾判据），
// 以及终态项的 rollup 与失败清单是什么（§6.10 的终局综合输入）。两个答案都来自
// durable batch/task 事实，本文件只把它们投影成 ObigationSource，不新建表、不新增
// 事件、不写任何行。
//
// 2026-10-09 真机修复：spawn_agent / spawn_team 的轻量义务不在 batch 表里，只按
// 批次投影会让 pending_count 恒为 0，把"子代理仍在运行"的挂起 turn 判成
// can_finalize=true（wait_feedback 唤醒消息在 l5-2-batch-b 运行中宣告"所有
// obligation 均已终态"）。投影因此必须同时读 §6.12 挂起记录里的
// agent_session:/team: 义务，并用与 settle/wait 判定完全相同的 resolver 判定其
// 终态；未接线或判读不到一律按非终态处理——"读不到"永远不能读成"已完成"。
type BatchObligationSource struct {
	Store subagentbatch.BatchStore
	// AgentSessions / Teams judge the durable state of the child-session and
	// team-run obligations parked in the §6.12 records. They are the same
	// resolvers the settle/wait predicates use, so the resume verdict and the
	// wake admission that scheduled it can never disagree. A nil resolver keeps
	// every such row non-terminal: an unreadable child is never evidence that
	// the work finished.
	AgentSessions subagentbatch.AgentSessionObligationResolver
	Teams         subagentbatch.TeamObligationResolver
	// MaxBatches bounds the per-read page. Non-positive uses the default (32,
	// the same bound the P0-B progress projection uses).
	MaxBatches int
	// MaxTasksPerBatch bounds how many task rows one batch contributes to the
	// failure list. Non-positive uses the default (20).
	MaxTasksPerBatch int
	// MaxPreviewRunes bounds every inline preview (result summaries). Non-positive
	// uses the default (200); full payloads stay behind ResultRef (I7).
	MaxPreviewRunes int
}

const (
	defaultObligationMaxBatches       = 32
	defaultObligationMaxTasksPerBatch = 20
	defaultObligationMaxPreviewRunes  = 200
)

// NewBatchObligationSource wires the durable batch control plane into the
// resume projection. A nil store returns nil so unwired hosts keep the legacy
// new-turn wake behavior exactly.
func NewBatchObligationSource(store subagentbatch.BatchStore) ObligationSource {
	return NewBatchObligationSourceWithResolvers(store, nil, nil)
}

// NewBatchObligationSourceWithResolvers is NewBatchObligationSource extended
// with the child-session / team control planes. Hosts that can park
// spawn_agent / spawn_team obligations must pass their resolvers here; a nil
// resolver keeps those rows non-terminal (conservative, never a guessed
// completion), so an unwired host can never finalize a turn over a live child.
func NewBatchObligationSourceWithResolvers(store subagentbatch.BatchStore, agentSessions subagentbatch.AgentSessionObligationResolver, teams subagentbatch.TeamObligationResolver) ObligationSource {
	if store == nil {
		return nil
	}
	return &BatchObligationSource{Store: store, AgentSessions: agentSessions, Teams: teams}
}

// ListObligations implements ObligationSource. Scope口径 与 progress 投影一致：
// 只读属于该父会话的批次（ParentSessionID 由宿主按自己的 scope 注入，模型不可
// 指定）。Rows are newest-first and bounded.
func (s *BatchObligationSource) ListObligations(ctx context.Context, parentSessionID string) ([]ObligationRef, error) {
	if s == nil || s.Store == nil {
		return nil, nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return nil, nil
	}
	limit := s.MaxBatches
	if limit <= 0 {
		limit = defaultObligationMaxBatches
	}
	batches, err := s.Store.ListBatches(ctx, subagentbatch.BatchFilter{
		ParentSessionID: parentSessionID,
		Limit:           limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]ObligationRef, 0, len(batches))
	seen := make(map[string]struct{}, len(batches))
	for _, batch := range batches {
		if strings.TrimSpace(batch.BatchID) == "" {
			continue
		}
		ref := s.obligationForBatch(ctx, batch)
		if _, dup := seen[ref.ID]; dup {
			continue
		}
		seen[ref.ID] = struct{}{}
		out = append(out, ref)
	}
	// §6.12 挂起记录里的 agent_session:/team: 义务：它们不在 batch 表里，但
	// 同样压住本 turn 的收尾判据（I1）。记录读取失败直接返回错误：宁可让
	// resume 降级成 "unknown"，也不能在没有这份账本时宣告 can_finalize=true。
	records, err := s.Store.ListTurnSuspensions(ctx, parentSessionID)
	if err != nil {
		return nil, err
	}
	for index, record := range records {
		if index >= limit {
			break
		}
		if record == nil {
			continue
		}
		turnID := strings.TrimSpace(record.TurnID)
		for _, sessionID := range record.ObligationAgentSessionIDs() {
			sessionID = strings.TrimSpace(sessionID)
			if sessionID == "" {
				continue
			}
			ref := s.obligationForAgentSession(ctx, sessionID, turnID)
			if _, dup := seen[ref.ID]; dup {
				continue
			}
			seen[ref.ID] = struct{}{}
			out = append(out, ref)
		}
		for _, teamID := range record.ObligationTeamIDs() {
			teamID = strings.TrimSpace(teamID)
			if teamID == "" {
				continue
			}
			ref := s.obligationForTeam(ctx, teamID, turnID)
			if _, dup := seen[ref.ID]; dup {
				continue
			}
			seen[ref.ID] = struct{}{}
			out = append(out, ref)
		}
	}
	return out, nil
}

// obligationForAgentSession projects one child-session obligation of a §6.12
// record. The resolver is the same durable judge the settle/wait predicates
// use. A missing row, an unwired resolver or a read error all keep the
// obligation non-terminal (state=pending): the parent must never be told to
// finalize over a child the control plane cannot vouch for.
func (s *BatchObligationSource) obligationForAgentSession(ctx context.Context, sessionID, turnID string) ObligationRef {
	ref := ObligationRef{
		ID:           subagentbatch.AgentSessionObligationID(sessionID),
		Kind:         "agent_session",
		ParentTurnID: turnID,
		State:        ObligationStatePending,
	}
	if s == nil || s.AgentSessions == nil {
		return ref
	}
	terminal, found, err := s.AgentSessions.AgentSessionTerminal(ctx, sessionID)
	if err != nil || !found {
		return ref
	}
	if terminal {
		ref.State = ObligationStateCompleted
		ref.Terminal = true
		return ref
	}
	ref.State = ObligationStateRunning
	return ref
}

// obligationForTeam is the team-run twin of obligationForAgentSession.
func (s *BatchObligationSource) obligationForTeam(ctx context.Context, teamID, turnID string) ObligationRef {
	ref := ObligationRef{
		ID:           subagentbatch.TeamObligationID(teamID),
		Kind:         "team",
		ParentTurnID: turnID,
		State:        ObligationStatePending,
	}
	if s == nil || s.Teams == nil {
		return ref
	}
	terminal, found, err := s.Teams.TeamTerminal(ctx, teamID)
	if err != nil || !found {
		return ref
	}
	if terminal {
		ref.State = ObligationStateCompleted
		ref.Terminal = true
		return ref
	}
	ref.State = ObligationStateRunning
	return ref
}

// obligationForBatch maps one durable batch (plus its task rows, best-effort)
// into an ObligationRef. A task read failure degrades to the batch-level
// counts: the join verdict must never depend on the richer projection.
func (s *BatchObligationSource) obligationForBatch(ctx context.Context, batch subagentbatch.SubagentBatch) ObligationRef {
	ref := ObligationRef{
		ID:             strings.TrimSpace(batch.BatchID),
		Kind:           "batch",
		ParentTurnID:   strings.TrimSpace(batch.ParentTurnID),
		State:          obligationStateForBatchStatus(batch.Status),
		Total:          batch.TaskCount,
		Completed:      batch.CompletedCount,
		Failed:         batch.FailedCount + batch.TimedOutCount,
		Skipped:        0,
		ResultSummary:  s.preview(batch.ResultSummary),
		LastProgressAt: batch.HeartbeatAt,
	}
	ref.Terminal = obligationTerminal(ref.State)
	if batch.Status == subagentbatch.BatchPartiallyCompleted && ref.Failed == 0 {
		// A partial batch with no counted failure still means some tasks did
		// not succeed; count the remainder so the rollup is not silently
		// green (the join verdict itself stays non-terminal).
		ref.Failed = maxInt(0, ref.Total-ref.Completed)
	}
	if batch.CanceledCount > 0 && ref.State == ObligationStateCanceled {
		ref.Skipped = batch.CanceledCount
	}
	if strings.TrimSpace(batch.ErrorClass) != "" && ref.Terminal {
		ref.Failures = append(ref.Failures, FailureItem{
			ObligationID: ref.ID,
			BatchID:      ref.ID,
			State:        ref.State,
			ErrorClass:   strings.TrimSpace(batch.ErrorClass),
			Retryable:    retryableFailure(ref.State, batch.ErrorDetail, batch.ErrorClass),
			ResultRef:    firstNonEmptyString(batch.ResultSummaryRef, "batch:"+ref.ID),
			Summary:      s.preview([]byte(batch.ErrorDetail)),
		})
	}
	if batch.ResultSummaryRef != "" {
		ref.ArtifactRefs = append(ref.ArtifactRefs, strings.TrimSpace(batch.ResultSummaryRef))
	}
	s.appendTaskDetails(ctx, &ref)
	return ref
}

// appendTaskDetails adds the per-task failure evidence (H3: error class +
// retryable flag) and the artifact refs the final report can cite.
func (s *BatchObligationSource) appendTaskDetails(ctx context.Context, ref *ObligationRef) {
	if ref == nil || s == nil || s.Store == nil || !ref.Terminal {
		return
	}
	tasks, err := s.Store.ListTasks(ctx, ref.ID)
	if err != nil || len(tasks) == 0 {
		return
	}
	maxTasks := s.MaxTasksPerBatch
	if maxTasks <= 0 {
		maxTasks = defaultObligationMaxTasksPerBatch
	}
	skipped := 0
	for _, task := range tasks {
		state := obligationStateForTaskStatus(task.Status)
		switch state {
		case ObligationStateFailed, ObligationStateTimedOut, ObligationStateCanceled, ObligationStateAbandoned:
			if len(ref.Failures) < maxTasks {
				ref.Failures = append(ref.Failures, FailureItem{
					ObligationID: ref.ID,
					BatchID:      ref.ID,
					TaskID:       strings.TrimSpace(task.TaskID),
					State:        state,
					ErrorClass:   strings.TrimSpace(task.ErrorClass),
					ErrorCode:    strings.TrimSpace(task.ErrorCode),
					Retryable:    retryableFailure(state, task.ErrorCode, task.ErrorClass),
					ResultRef:    firstNonEmptyString(task.ArtifactRef, "batch:"+ref.ID+"/task:"+strings.TrimSpace(task.TaskID)),
					Summary:      s.preview(task.ResultSummary),
				})
			}
		case ObligationStateSkipped:
			skipped++
		}
		// Every terminal task's artifact stays citable by the final report —
		// including the successful ones, whose deliverables are exactly what
		// §6.10 H2 asks the parent to reference. Bounded by the same per-batch
		// page as the failure list (I7).
		if artifact := strings.TrimSpace(task.ArtifactRef); artifact != "" && len(ref.ArtifactRefs) < maxTasks {
			ref.ArtifactRefs = appendUniqueString(ref.ArtifactRefs, artifact)
		}
	}
	if skipped > 0 {
		ref.Skipped += skipped
	}
}

// preview bounds an inline payload preview (I7: bounded output; the payload
// itself stays behind a ResultRef).
func (s *BatchObligationSource) preview(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	maxRunes := s.MaxPreviewRunes
	if maxRunes <= 0 {
		maxRunes = defaultObligationMaxPreviewRunes
	}
	text := strings.TrimSpace(string(raw))
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "…"
}

// obligationStateForBatchStatus maps the batch vocabulary onto the §16.4
// terminal enumeration. partially_completed is deliberately NOT terminal — it
// stays a live state until the batch finalizes into completed/failed — so a
// parent can never finalize a turn while a batch is still converging.
func obligationStateForBatchStatus(status subagentbatch.BatchStatus) string {
	switch status {
	case subagentbatch.BatchCompleted:
		return ObligationStateCompleted
	case subagentbatch.BatchFailed:
		return ObligationStateFailed
	case subagentbatch.BatchCanceled:
		return ObligationStateCanceled
	case subagentbatch.BatchTimedOut:
		return ObligationStateTimedOut
	case subagentbatch.BatchOrphaned:
		return ObligationStateOrphaned
	case subagentbatch.BatchRunning, subagentbatch.BatchPartiallyCompleted:
		return ObligationStateRunning
	default:
		return ObligationStatePending
	}
}

// ObligationStateSkipped is the resume-side name of a task that was never run
// (dependency failed or explicitly skipped). It is terminal but not a failure.
const ObligationStateSkipped = "skipped"

func obligationStateForTaskStatus(status subagentbatch.TaskStatus) string {
	switch status {
	case subagentbatch.TaskSucceeded:
		return ObligationStateCompleted
	case subagentbatch.TaskFailed, subagentbatch.TaskFailedWithResult:
		return ObligationStateFailed
	case subagentbatch.TaskCanceled:
		return ObligationStateCanceled
	case subagentbatch.TaskTimedOut:
		return ObligationStateTimedOut
	case subagentbatch.TaskSkipped:
		return ObligationStateSkipped
	case subagentbatch.TaskRunning, subagentbatch.TaskReady:
		return ObligationStateRunning
	default:
		return ObligationStatePending
	}
}

// retryableFailure classifies a failed item for the parent's next decision
// (§6.10 H3: retry / reassign / abandon). Structural causes (superseded runs,
// policy/approval denials, invalid arguments) are not retryable by repeating
// the same work; everything else is.
func retryableFailure(state, errorCode, errorClass string) bool {
	switch strings.TrimSpace(state) {
	case ObligationStateCanceled, ObligationStateAbandoned, ObligationStateOrphaned:
		// A canceled/abandoned item was stopped on purpose: re-running it is a
		// new decision, not a retry.
		return false
	}
	code := strings.ToLower(strings.TrimSpace(errorCode))
	class := strings.ToLower(strings.TrimSpace(errorClass))
	for _, marker := range []string{"superseded", "permission", "denied", "invalid", "policy_conflict", "aborted"} {
		if strings.Contains(code, marker) || strings.Contains(class, marker) {
			return false
		}
	}
	return true
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
