package supervision

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// C2-1（改动 #4）：wake 升级为 resume。
//
// 设计稿 §6.1「resume 动作」：resume 不是"起一轮新 turn"，而是以**同一
// turn_id** 注入 resume 上下文（rollup digest + 可用动作清单），父 turn 继续；
// §6.10 / §16.4：`pending_count == 0` 时 runtime 必须自己组装"全终态 rollup"
// （含 completed_with_failures 的失败项清单）注入父 turn，模型只做"综合与决策"
// （H4：不要求模型回溯历史）。
//
// 本文件只做投影：读的是账本与 batch 控制面里既有的事实，不新建表、不新增事件。
// 预算与 digest 同源（DigestMaxItems=20 / DigestMaxChars=4000，AC-P1-1d）。

// Obligation states. The first five are terminal by contract (design §16.4: the
// terminal enumeration must be exhaustive or the join hangs); the last three
// are the non-terminal remainder.
const (
	ObligationStatePending               = "pending"
	ObligationStateRunning               = "running"
	ObligationStateCompleted             = "completed"
	ObligationStateCompletedWithFailures = "completed_with_failures"
	ObligationStateFailed                = "failed"
	ObligationStateCanceled              = "canceled"
	ObligationStateTimedOut              = "timed_out"
	ObligationStateOrphaned              = "orphaned"
	ObligationStateAbandoned             = "abandoned"
)

// obligationTerminal reports whether a state can no longer transition. It is
// the resume-side twin of the batch/task terminal predicates and deliberately
// answers true for the abnormal families too (I10: every obligation must reach
// a terminal state, otherwise the join never returns).
func obligationTerminal(state string) bool {
	switch strings.TrimSpace(state) {
	case ObligationStateCompleted, ObligationStateCompletedWithFailures,
		ObligationStateFailed, ObligationStateCanceled,
		ObligationStateTimedOut, ObligationStateOrphaned,
		ObligationStateAbandoned:
		return true
	default:
		return false
	}
}

// FailureItem is one failed terminal obligation item as much as the durable
// ledger can tell (§6.10 H2/H3: the receipt carries counts + failure summary +
// references only; the full payload stays behind ResultRef).
type FailureItem struct {
	ObligationID string `json:"obligation_id,omitempty"`
	BatchID      string `json:"batch_id,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	State        string `json:"state,omitempty"`
	ErrorClass   string `json:"error_class,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	// Retryable tells the parent which control action can still converge the
	// item (retry / reassign / abandon) without re-reading the transcript.
	Retryable bool `json:"retryable,omitempty"`
	// ResultRef points at the durable payload (batch id / task id / artifact),
	// never at the payload itself (I7: bounded output).
	ResultRef string `json:"result_ref,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

// ObligationRef is one ledger row as the resume projection sees it. It is
// host-neutral and bounded; sources must not put full child output in Summary
// (I7).
type ObligationRef struct {
	ID       string `json:"id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	Kind     string `json:"kind,omitempty"`
	// ParentTurnID is the turn that dispatched this obligation (G6 anchor).
	// The resume projection uses it to re-anchor a wake whose own turn hint
	// was lost (I3: resume 复用同一 turn_id).
	ParentTurnID string `json:"parent_turn_id,omitempty"`
	State        string `json:"state,omitempty"`
	Terminal     bool   `json:"terminal,omitempty"`
	Total        int    `json:"total,omitempty"`
	Completed    int    `json:"completed,omitempty"`
	Failed       int    `json:"failed,omitempty"`
	Skipped      int    `json:"skipped,omitempty"`
	// Failures lists the failed items of this obligation (already flattened
	// from task rows by the source).
	Failures []FailureItem `json:"failures,omitempty"`
	// ArtifactRefs are durable result references (batch/task/artifact ids).
	ArtifactRefs []string `json:"artifact_refs,omitempty"`
	// ResultSummary is the bounded result digest preview.
	ResultSummary  string    `json:"result_summary,omitempty"`
	LastProgressAt time.Time `json:"last_progress_at,omitempty"`
}

// ObligationSource is the read-only projection the resume builder needs. A nil
// source keeps the wake on the legacy "new turn" path, so unwired hosts behave
// exactly as before.
type ObligationSource interface {
	// ListObligations returns the parent session's ledger rows (non-terminal
	// and terminal). Implementations must be read-only and bounded.
	ListObligations(ctx context.Context, parentSessionID string) ([]ObligationRef, error)
}

// TurnHintFunc resolves the turn a wake belongs to at scheduling time (plan
// C2-1: 通知携带 turn_id，恢复同一 turn). It is only a hint: the delivery path
// re-derives the authoritative turn id from the obligation ledger, so a stale
// or empty hint can never resume a turn that already ended (I3 保底). Returning
// "" keeps the legacy "new turn" wake.
type TurnHintFunc func(ctx context.Context, parentSessionID string) string

// ResumeBudget bounds the resume context the same way the preflight digest is
// bounded (AC-P1-1d). Zero values fall back to DefaultConfig().
type ResumeBudget struct {
	MaxItems int
	MaxChars int
}

// withDefaults fills zero fields from the shipped defaults so a config-less
// host still gets 20 items / 4000 chars (the AC-P1-1d numbers).
func (b ResumeBudget) withDefaults() ResumeBudget {
	defaults := DefaultConfig()
	if b.MaxItems <= 0 {
		b.MaxItems = defaults.DigestMaxItems
	}
	if b.MaxChars <= 0 {
		b.MaxChars = defaults.DigestMaxChars
	}
	return b
}

// ResumeContext is the structured payload injected into the parent turn when a
// wake resumes it (design §6.1 step 3). Terminal=true is the §16.4 join
// verdict: every obligation of the turn reached a terminal state and the parent
// may produce the final report.
type ResumeContext struct {
	TurnID          string `json:"turn_id,omitempty"`
	ParentSessionID string `json:"session_id,omitempty"`
	RootScopeID     string `json:"root_scope_id,omitempty"`
	// TotalCount / PendingCount are the join inputs (pending_count == 0 ⇒
	// finalize). PendingCount is -1 when no obligation source was wired and the
	// verdict is therefore unknown ("assume not final" is the safe reading).
	TotalCount   int `json:"total,omitempty"`
	PendingCount int `json:"pending_count,omitempty"`
	// StandaloneCount counts the non-terminal rows without a turn_id (EC-G4:
	// migration-period lifecycle rows keep the legacy "standalone run"
	// semantics). They are reported, never gated: the I1 verdict stays
	// turn-scoped (design §6.2), so a stale unattributed row cannot block a
	// turn's finalize forever.
	StandaloneCount int  `json:"standalone_count,omitempty"`
	Terminal        bool `json:"terminal,omitempty"`
	// Rollup is the P0-B progress projection (bounded by MaxItems).
	Rollup []ProgressSummary `json:"rollup,omitempty"`
	// Obligations carries the terminal rollup rows with counts and failure
	// lists (bounded by MaxItems; failures additionally bounded per row).
	Obligations []ObligationRef `json:"obligations,omitempty"`
	// Failures is the flattened failure list of the whole turn (bounded).
	Failures []FailureItem `json:"failures,omitempty"`
	// ArtifactRefs are the durable references the final report can cite
	// (bounded).
	ArtifactRefs []string `json:"artifact_refs,omitempty"`
	// Status is the §16.4 aggregate enunciation:
	// completed | completed_with_failures | failed | canceled | timed_out |
	// orphaned | running | pending | unknown.
	Status string `json:"status,omitempty"`
	// Truncated marks that the budget cut content; the full detail stays
	// reachable through subagent_status(include_digest=true) / subagent_inspect_task.
	Truncated bool `json:"truncated,omitempty"`
	// SourceError is a best-effort diagnostic: the resume context must never
	// fail the wake, so a broken source degrades to the digest-only form.
	SourceError string `json:"source_error,omitempty"`
	// DigestText is the bounded lifecycle digest text (P0-B included).
	DigestText string `json:"digest_text,omitempty"`
	// Text is the final, budget-bounded resume block handed to the parent.
	Text string `json:"text,omitempty"`

	budget ResumeBudget
}

// ResumeContextRequest is the host-neutral input of the resume projection.
type ResumeContextRequest struct {
	ParentSessionID string
	RootScopeID     string
	// TurnID is the wake's turn hint; the resumed turn keeps this identity
	// (I3: resume 复用同一 turn_id).
	TurnID string
	// Digest is the claimed lifecycle digest (may be nil for a pure
	// progress/deadline resume).
	Digest *Digest
	Budget ResumeBudget
}

// BuildResumeContext assembles the same-turn resume payload. It is best-effort
// by construction (same rule as the wake budget projection): a failing ledger
// read must degrade the resume context, never swallow the wake.
func BuildResumeContext(ctx context.Context, source ObligationSource, progress ProgressSource, req ResumeContextRequest) *ResumeContext {
	budget := req.Budget.withDefaults()
	rc := &ResumeContext{
		TurnID:          strings.TrimSpace(req.TurnID),
		ParentSessionID: strings.TrimSpace(req.ParentSessionID),
		RootScopeID:     strings.TrimSpace(req.RootScopeID),
		PendingCount:    -1,
		Status:          "unknown",
		budget:          budget,
	}
	if req.Digest != nil {
		rc.DigestText = req.Digest.Text
	}

	// Progress rollup: reuse the exact projection the preflight digest uses so a
	// resumed turn sees the same slice of truth it would have seen on preflight.
	if progress != nil {
		summaries, truncated, err := BuildProgressSummary(ctx, progress, ProgressRequest{
			RootScopeID:     rc.RootScopeID,
			ParentSessionID: rc.ParentSessionID,
			RowLimit:        budget.MaxItems,
		}, budget.MaxItems, time.Now().UTC())
		if err != nil {
			if rc.SourceError == "" {
				rc.SourceError = err.Error()
			}
		} else {
			rc.Rollup = summaries
			rc.Truncated = rc.Truncated || truncated
		}
	}

	if source != nil && rc.ParentSessionID != "" {
		obligations, err := source.ListObligations(ctx, rc.ParentSessionID)
		if err != nil {
			if rc.SourceError == "" {
				rc.SourceError = err.Error()
			}
		} else {
			rc.applyObligations(obligations)
		}
	}

	rc.Text = formatResumeText(rc, budget)
	return rc
}

// applyObligations folds the ledger rows into counts, the §16.4 aggregate
// status and the bounded failure/artifact lists.
func (rc *ResumeContext) applyObligations(obligations []ObligationRef) {
	if rc == nil {
		return
	}
	maxItems := rc.budget.withDefaults().MaxItems
	rc.PendingCount = 0
	rc.TotalCount = len(obligations)
	kept := make([]ObligationRef, 0, len(obligations))
	failures := 0
	pendingTurnID := ""
	ledgerTurnID := ""
	standalone := 0
	for _, ob := range obligations {
		ob.State = strings.TrimSpace(ob.State)
		if ob.State == "" {
			ob.State = ObligationStatePending
		}
		ob.Terminal = ob.Terminal || obligationTerminal(ob.State)
		if !ob.Terminal {
			// EC-G4（迁移兼容）：没有 turn_id 的历史行走"独立 run"语义。
			// design §6.2 的 I1 判据是 count(obligations where turn_id=? and
			// not terminal) == 0：无归因的行不属于任何 turn，因此不参与账本
			// 清空判定 —— 它们仍进入 rollup/失败清单供模型参考，但不会把
			// can_finalize 压成 false（否则迁移期的陈旧行会永久堵死收尾）。
			if strings.TrimSpace(ob.ParentTurnID) == "" {
				standalone++
			} else {
				rc.PendingCount++
			}
		}
		if turnID := strings.TrimSpace(ob.ParentTurnID); turnID != "" {
			if ledgerTurnID == "" {
				ledgerTurnID = turnID
			}
			if !ob.Terminal && pendingTurnID == "" {
				pendingTurnID = turnID
			}
		}
		for _, failure := range ob.Failures {
			if failures >= maxItems {
				rc.Truncated = true
				break
			}
			if failure.ObligationID == "" {
				failure.ObligationID = ob.ID
			}
			if failure.BatchID == "" {
				// A failure with no explicit attribution belongs to the
				// obligation it came from; for a batch obligation the
				// obligation id IS the batch id.
				failure.BatchID = ob.ID
			}
			if failure.State == "" {
				failure.State = ob.State
			}
			rc.Failures = append(rc.Failures, failure)
			failures++
		}
		for _, ref := range ob.ArtifactRefs {
			if len(rc.ArtifactRefs) >= maxItems {
				rc.Truncated = true
				break
			}
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			rc.ArtifactRefs = append(rc.ArtifactRefs, ref)
		}
		if len(kept) >= maxItems {
			rc.Truncated = true
			continue
		}
		kept = append(kept, ob)
	}
	rc.Obligations = kept
	rc.StandaloneCount = standalone
	// I3（resume 复用同一 turn_id）的锚定优先级：仍有非终态 obligation 时，它们
	// 所属的 turn 就是必须被 resume 的 turn（ledger 优先于调度时的提示值，避免
	// 用陈旧提示去续跑另一个 turn）；没有非终态项时才退回提示值，最后才用账本里
	// 残留的终态 turn 兜底。三者都为空 ⇒ 由宿主按"新开 turn"保底（告警）。
	switch {
	case pendingTurnID != "":
		rc.TurnID = pendingTurnID
	case rc.TurnID == "":
		rc.TurnID = ledgerTurnID
	}
	rc.Terminal = rc.PendingCount == 0
	rc.Status = aggregateObligationStatus(obligations, rc.Terminal)
}

// aggregateObligationStatus derives the §16.4 single-word status for the turn:
// a fully terminal ledger with failures is `completed_with_failures`; terminal
// without failures is `completed`; otherwise the batch-family status wins by
// severity so a canceled turn is not reported as merely "running".
func aggregateObligationStatus(obligations []ObligationRef, terminal bool) string {
	if len(obligations) == 0 {
		return ObligationStateCompleted
	}
	if !terminal {
		worst := ObligationStatePending
		for _, ob := range obligations {
			if ob.Terminal {
				continue
			}
			if ob.State == ObligationStateRunning {
				worst = ObligationStateRunning
				break
			}
		}
		return worst
	}
	failed := false
	worst := ObligationStateCompleted
	rank := map[string]int{
		ObligationStateCompleted:             0,
		ObligationStateCompletedWithFailures: 1,
		ObligationStateAbandoned:             2,
		ObligationStateCanceled:              2,
		ObligationStateTimedOut:              3,
		ObligationStateOrphaned:              3,
		ObligationStateFailed:                4,
	}
	worstRank := -1
	for _, ob := range obligations {
		if ob.State == ObligationStateCompletedWithFailures || ob.Failed > 0 ||
			ob.State == ObligationStateFailed || ob.State == ObligationStateCanceled ||
			ob.State == ObligationStateTimedOut || ob.State == ObligationStateOrphaned ||
			ob.State == ObligationStateAbandoned {
			failed = true
		}
		if r, ok := rank[ob.State]; ok && r > worstRank {
			worstRank = r
			worst = ob.State
		}
	}
	if failed && worst == ObligationStateCompleted {
		return ObligationStateCompletedWithFailures
	}
	return worst
}

// formatResumeText renders the deterministic, budget-bounded resume block. The
// shape is stable so hosts and tests can assert on it, and so the model can
// parse the join verdict without re-reading history (H4).
func formatResumeText(rc *ResumeContext, budget ResumeBudget) string {
	if rc == nil {
		return ""
	}
	budget = budget.withDefaults()
	var b strings.Builder
	b.WriteString("[Child lifecycle resume]\n")
	if rc.TurnID != "" {
		fmt.Fprintf(&b, "turn_id: %s\n", rc.TurnID)
	}
	if rc.PendingCount >= 0 {
		fmt.Fprintf(&b, "obligations: total=%d pending=%d can_finalize=%t\n",
			rc.TotalCount, rc.PendingCount, rc.PendingCount == 0)
		if rc.StandaloneCount > 0 {
			fmt.Fprintf(&b, "standalone_obligations: %d (no turn_id; legacy standalone runs, not gating can_finalize)\n", rc.StandaloneCount)
		}
	} else {
		b.WriteString("obligations: unknown (no ledger projection wired); can_finalize=unknown\n")
	}
	if status := strings.TrimSpace(rc.Status); status != "" {
		fmt.Fprintf(&b, "rollup_status: %s\n", status)
	}
	if len(rc.Rollup) > 0 {
		b.WriteString("\nprogress:\n")
		for _, group := range rc.Rollup {
			fmt.Fprintf(&b, "- %s: %d/%d completed", group.GroupID, group.Completed, group.Total)
			if group.Failed > 0 {
				fmt.Fprintf(&b, ", %d failed", group.Failed)
			}
			if group.Running > 0 {
				fmt.Fprintf(&b, ", %d running", group.Running)
			}
			if group.Pending > 0 {
				fmt.Fprintf(&b, ", %d pending", group.Pending)
			}
			if group.Skipped > 0 {
				fmt.Fprintf(&b, ", %d skipped", group.Skipped)
			}
			if group.Terminal {
				b.WriteString(", terminal")
			}
			b.WriteString("\n")
		}
	}
	if len(rc.Obligations) > 0 {
		b.WriteString("\nterminal rollup:\n")
		for _, ob := range rc.Obligations {
			fmt.Fprintf(&b, "- %s %s: status=%s", ob.Kind, ob.ID, ob.State)
			if ob.Total > 0 {
				fmt.Fprintf(&b, " total=%d completed=%d failed=%d", ob.Total, ob.Completed, ob.Failed)
				if ob.Skipped > 0 {
					fmt.Fprintf(&b, " skipped=%d", ob.Skipped)
				}
			}
			if !ob.Terminal {
				b.WriteString(" (not terminal)")
			}
			if strings.TrimSpace(ob.ParentTurnID) == "" {
				b.WriteString(" standalone=true")
			}
			if summary := strings.TrimSpace(ob.ResultSummary); summary != "" && ob.Terminal {
				fmt.Fprintf(&b, "; result: %s", summary)
			}
			b.WriteString("\n")
		}
	}
	if len(rc.Failures) > 0 {
		b.WriteString("\nfailed items:\n")
		for _, item := range rc.Failures {
			fmt.Fprintf(&b, "- %s", firstNonEmptyString(item.TaskID, item.ObligationID, item.BatchID))
			if item.BatchID != "" && item.BatchID != item.TaskID {
				fmt.Fprintf(&b, " (batch %s)", item.BatchID)
			}
			fmt.Fprintf(&b, ": state=%s", item.State)
			if item.ErrorClass != "" {
				fmt.Fprintf(&b, " error_class=%s", item.ErrorClass)
			}
			if item.ErrorCode != "" {
				fmt.Fprintf(&b, " error_code=%s", item.ErrorCode)
			}
			fmt.Fprintf(&b, " retryable=%t", item.Retryable)
			if item.ResultRef != "" {
				fmt.Fprintf(&b, " result_ref=%s", item.ResultRef)
			}
			if item.Summary != "" {
				fmt.Fprintf(&b, "; %s", item.Summary)
			}
			b.WriteString("\n")
		}
	}
	if len(rc.ArtifactRefs) > 0 {
		fmt.Fprintf(&b, "\nartifact_refs: %s\n", strings.Join(rc.ArtifactRefs, ", "))
	}
	if rc.Truncated {
		b.WriteString("\ntruncated: true (budget reached; full detail via subagent_status(include_digest=true) / subagent_inspect_task)\n")
	}
	if rc.SourceError != "" {
		fmt.Fprintf(&b, "ledger_projection_error: %s\n", rc.SourceError)
	}
	if text := strings.TrimSpace(rc.DigestText); text != "" {
		b.WriteString("\n")
		b.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			b.WriteString("\n")
		}
	}
	return truncateResumeText(b.String(), budget.MaxChars)
}

// truncateResumeText enforces the byte budget the same way the digest budget
// does: cut on a rune boundary and leave a marker instead of a silent cut.
func truncateResumeText(text string, maxChars int) string {
	if maxChars <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	const marker = "\n[truncated: resume context exceeds budget; use subagent_status(include_digest=true) / subagent_inspect_task for full detail]"
	markerRunes := []rune(marker)
	keep := maxChars - len(markerRunes)
	if keep < 0 {
		keep = 0
	}
	if keep > len(runes) {
		keep = len(runes)
	}
	return strings.TrimRight(string(runes[:keep]), " \n") + marker
}

// ResumePrompt renders the prompt submitted for a same-turn resume (design
// §6.1 step 3: "以同一 turn_id 注入 resume 上下文（rollup digest + 可用动作
// 清单）"). The rollup is inlined because a resumed turn must not have to
// re-derive it from history (H4); the text is already budget-bounded.
func ResumePrompt(rc *ResumeContext) string {
	var b strings.Builder
	b.WriteString("[supervision] resume：子任务生命周期事件触发**同一 turn 续跑**")
	if rc != nil && strings.TrimSpace(rc.TurnID) != "" {
		fmt.Fprintf(&b, "（turn_id=%s）", strings.TrimSpace(rc.TurnID))
	}
	b.WriteString("。以下 rollup 由 runtime 组装，无需回溯历史。\n")
	if rc != nil {
		if rc.PendingCount == 0 {
			b.WriteString("所有 obligation 均已终态：请直接产出终局报告并结束本 turn（不要重复派发同一工作）。\n")
		} else if rc.PendingCount > 0 {
			b.WriteString("仍有未终态 obligation：不得收尾（I1）；按可用动作继续巡检 / 有界等待 / 延长 / 取消。\n")
		}
		b.WriteString("\n")
		b.WriteString(rc.Text)
		if !strings.HasSuffix(rc.Text, "\n") {
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
