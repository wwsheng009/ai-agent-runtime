package ui

import (
	"errors"
	"time"
)

var (
	ErrHistoryCommitFrozen          = errors.New("history commit queue is frozen by alternate screen lease")
	ErrHistoryProjectionUnknown     = errors.New("history projection is unknown; recovery is required before a new commit")
	ErrHistoryCommitRecoveryPending = errors.New("history commit has unresolved terminal delivery")
)

// HistoryEffectQueueState is the AppState-owned lifecycle record for native
// scrollback effects. Semantic transcript data remains in TranscriptState; the
// queue records only terminal delivery state and can be discarded/rebuilt with
// a controlled projection recovery.
type HistoryEffectQueueState struct {
	NextToken              uint64
	TerminalEpoch          uint64
	Frozen                 bool
	ProjectionUnknown      bool
	ReconciliationRequired bool
	// DeferHistoryDelivery holds native-scrollback delivery while a windowed
	// session load is still backfilling earlier pages (see
	// ReplaceTranscriptAction.DeferHistoryDelivery). While set, the planner
	// mints no history commit, so the loaded generation reaches the
	// append-only scrollback as one ordered pass instead of newest-page-first.
	DeferHistoryDelivery bool
	// claimSkipsStaleAction / claimRejects* keep reducer-side BeginHistoryCommit
	// refusals observable. A refusal is correct (the queue is ordered and the
	// gates own recovery) and must stay harmless to state, but it was completely
	// silent: a rejected claim and a claim that never arrived produced identical
	// diagnostics. That is how the 2026-10-05 stranded-claim incident stayed
	// invisible while the ordering guard rejected every later claim.
	claimSkipsStaleAction  uint64 // action generation != state generation; the reducer never attempts markInFlight
	claimRejectsOutOfOrder uint64 // an older Queued token fences this claim
	claimRejectsGate       uint64 // frozen / projection unknown / unresolved terminal delivery
	claimRejectsStale      uint64 // token entry missing, or its commit generation no longer matches
	claimRejectsInvalid    uint64 // anything else (not queued, invalid commit)
	// WriteCursor 是"可能正在被物理写入"的 token（0 = 无）。claim 协议从逐条
	// 状态收敛为这个单调标量：同一时刻至多一个 token 可能已在写，
	// 由单 worker 严格 FIFO 保证顺序。条目保持 Queued，直到 Ack/Fail/Invalidate
	// 给出终态；planner 对 WriteCursor 指向的 token 绝不 rebase（写入在途，
	// 改写载荷会让旧字节按新语义被 ack）。
	//
	// G4/C1：claimed 且 presentation 已漂移（generation/DisplayRange 变化、
	// identity 仍有效）时，收敛路径是执行器 generation 闸门 → Deferred 释放
	// 游标 → 下一次规划 rebase；规划期用 ClaimedPresentationDrift 计数让此前
	// 的静默跳过可观测（不在此处 invalidate：那会把每次写中 resize 升级成
	// ProjectionUnknown/Reconciliation）。
	WriteCursor uint64
	// ClaimedPresentationDrift 记录规划期发现的 claimed×presentation 漂移次数
	// （G4/C1 诊断计数；只读快照可见）。
	ClaimedPresentationDrift uint64
	ledger                   *HistoryCommitLedger
	// Transcript-plan memo for syncHistoryEffectsForTranscript. The full
	// transcript plan re-lays-out and re-wraps every finalized cell, which on
	// resumed sessions costs O(entire history) per stream chunk even though
	// the chunk only grows the still-mutable active cell. The fingerprint
	// covers every planEligibleHistoryCommits input: the transcript version
	// fence (same trust level as transcriptSnapshotAlreadyInstalled),
	// geometry/layout/theme identity, the semantic-projection flag, and the
	// terminal epoch (ledger replacement).
	lastPlannedTranscriptValid   bool
	lastPlannedTranscriptSceneID uint64
	// lastPlannedTranscriptFence fingerprints every finalized transcript cell
	// (ID/Revision/Phase/Kind/blocked/boundary/source length). The scene-wide
	// Revision/ContentVersion counters are NOT used: they advance on every
	// cell mutation, including the still-mutable active cell's stream growth,
	// so a memo keyed on them misses on every chunk and re-lays-out the whole
	// finalized history (the exact O(entire history) cost this memo exists to
	// avoid). Cell Revision is the scene's own per-cell mutation fence
	// (update/finalize require a strictly greater revision), so the
	// fingerprint has the same trust level as transcriptCellVersionEqual.
	lastPlannedTranscriptFence uint64
	// lastPlannedTranscriptCells counts the FINALIZED cells (Phase != mutable)
	// covered by the last complete plan. The total cell count is not used: a
	// busy turn appends mutable tail cells (reasoning/tool-chain boundaries)
	// that the finalized-prefix plan never consumes, and invalidating on them
	// re-planned the entire history per appended cell (723ms/op benchmark on a
	// 2000-cell transcript; live plan-last-ms 4.7-6.9s, ~9 plans/min).
	lastPlannedTranscriptCells     int
	lastPlannedTranscriptLayoutGen uint64
	lastPlannedWidth               int
	lastPlannedHeight              int
	lastPlannedProjection          bool
	lastPlannedThemeKey            string
	lastPlannedTerminalEpoch       uint64
	// lastPlannedCandidateCount is how many commits the last COMPLETE plan
	// produced. It exists because the memo fingerprints only plan *inputs*: a
	// ledger that lost the plan it was reconciled into (an external wholesale
	// replacement, or a load re-proof whose plan was never minted) leaves every
	// input unchanged, so the memo would keep claiming "already planned" over an
	// empty ledger and the loaded generation would never be appended. A memo hit
	// now additionally requires the ledger to still hold a lifecycle whenever
	// the last plan produced candidates.
	lastPlannedCandidateCount int
	// PlanCount / LastPlanDuration / MaxPlanDuration 是 P16 的归因读数（方案
	// docs/plan/resume-large-session-optimization-plan-20260924.md §7.3）：
	// P12（端点冻结）只说「用户被卡住」，这三个标量说明冻结是不是花在**规划**
	// （screening + 铸 commit）上，而不是布局或终端写。只由
	// syncHistoryEffectsForTranscriptWithin 记录；纯诊断标量，不参与任何投递
	// 判定，因此不会改变规划/交付语义。
	PlanCount        uint64
	LastPlanDuration time.Duration
	MaxPlanDuration  time.Duration
	// LastPlanPhases / MaxPlanPhases 把一次规划 pass 拆成 screen（快照 + 全量布局，
	// 含并行首渲染的墙钟）/ mint（铸 commit）/ apply（membership reconcile + 入队）
	// 三段并带上 cell 数：P16 归因必须能回答「最长那次规划的时间花在哪一段」，
	// 否则增量 screening 的取舍没有依据。纯诊断标量，不参与任何投递判定。
	LastPlanPhases transcriptPlanPhaseTiming
	MaxPlanPhases  transcriptPlanPhaseTiming
}

// transcriptPlanPhaseTiming 是一次 transcript 规划 pass 的分相位耗时。
type transcriptPlanPhaseTiming struct {
	cells    int
	screenMs int64
	mintMs   int64
	applyMs  int64
}

func (s HistoryEffectQueueState) Clone() HistoryEffectQueueState {
	s.ledger = s.ledger.Clone()
	return s
}

// cloneForDiagnostics returns a detached queue without the commit ledger.
//
// The ledger is the expensive part of this struct: on a resumed session it
// holds one entry per display fragment (measured: 100,000 entries for a
// 4,000-cell page), and copying it costs ~79 MB and ~130 ms per snapshot even
// with the render payload dropped — the per-entry maps, not the lines, are the
// floor. Diagnostic consumers read queue scalars and counters, never an entry,
// so the snapshot drops the ledger outright and the counters come from
// UIController.HistoryEffectDiagnostics under the same mutex.
//
// A ledger-less queue is indistinguishable from an empty one, so Entries() and
// Pending() on this snapshot return nothing: never use it for delivery
// decisions.
func (s HistoryEffectQueueState) cloneForDiagnostics() HistoryEffectQueueState {
	s.ledger = nil
	return s
}

// Entries returns a token-ordered detached view for diagnostics, tests, and a
// future presenter. Callers cannot mutate the actor-owned ledger through it.
func (s HistoryEffectQueueState) Entries() []HistoryCommitEntry {
	return s.ledger.Entries()
}

// Entry returns a detached copy of a single ledger entry without cloning the
// whole history. The executor previously called Entries() (a full deep copy
// of every commit's render lines) just to read one token's state; on resumed
// sessions that repeated an O(entire history) allocation per commit step.
func (s HistoryEffectQueueState) Entry(token uint64) (HistoryCommitEntry, bool) {
	if s.ledger == nil {
		return HistoryCommitEntry{}, false
	}
	entry, ok := s.ledger.Entry(token)
	return entry.Clone(), ok
}

// Pending returns the oldest-first commits eligible for a primary presenter.
// A frozen queue intentionally has no eligible effects even if semantic events
// keep enqueuing work while the alternate screen is visible.
func (s HistoryEffectQueueState) Pending() []HistoryCommit {
	if s.Frozen || s.ProjectionUnknown || s.hasUnresolvedTerminalDelivery() {
		return nil
	}
	if s.ledger == nil || !s.ledger.HasPending() {
		return nil
	}
	commits := make([]HistoryCommit, 0, len(s.ledger.byToken))
	for _, token := range s.ledger.orderedTokens() {
		entry, ok := s.ledger.byToken[token]
		if ok && entry.State == HistoryCommitQueued {
			commits = append(commits, entry.Commit.Clone())
		}
	}
	return commits
}

// HasPending is the cheap scheduler predicate. Unlike Pending it neither
// copies payload lines nor builds a detached view.
func (s HistoryEffectQueueState) HasPending() bool {
	return !s.Frozen && !s.ProjectionUnknown && !s.hasUnresolvedTerminalDelivery() &&
		s.ledger != nil && s.ledger.HasPending()
}

// hasSettledRecordForSource reports whether every ledger record for this source
// identity is settled for reconciliation purposes.
//
// 规划器用它跳过「已经交付/已终结」的分片，避免在每次 transcript 迁移时把
// 整段历史的 render payload 重新物化一遍（pprof：planMarkdownCellHistoryCommits
// 累计 211GB，其中绝大多数分片早已 Delivered）。只有 Queued 条目参与
// syncHistoryEffectCandidates 的 payload 比对与 rebase，因此只要存在这类条目
// 就必须继续发射候选；全部为终态（Delivered/Quarantined）时
// 发射与否不影响投递语义，跳过纯属省分配。
func (s HistoryEffectQueueState) hasSettledRecordForSource(key historyCommitSourceKey) bool {
	if s.ledger == nil {
		return false
	}
	// A1-3 步 3b：未铸造 ⇒ 无任何记录，必须物化并铸新 commit。
	if !s.ledger.mintedThrough(key) {
		return false
	}
	tokens := s.ledger.bySource[key]
	if len(tokens) == 0 {
		// 已铸造且无 live 记录：唯一可能是「已压缩的终态」（墓碑存在时其全部
		// 记录都已终态且不再参与 reconcile——条目已从 byToken 剪除，发射与否
		// 不影响投递语义，跳过纯属省分配；旧实现会先白物化一次 payload，随后
		// 在入队处以墓碑跳过）。无墓碑则只可能是 remintable 的
		// invalidated-clean（pruneEntry 不留墓碑）：必须物化以允许再铸。
		_, blocked := s.ledger.compactedTerminalSources[key]
		return blocked
	}
	for token := range tokens {
		entry, ok := s.ledger.byToken[token]
		if !ok {
			continue
		}
		if entry.State == HistoryCommitQueued {
			return false
		}
	}
	return true
}

// retainedQueuedCommitForSource 判定某来源能否在复用段分拣中被整行保留，
// 并返回需要并回完整计划的已排队提交（复用段不重铸，但 replay/rebase 消费方
// 需要"完整有效集合"，缺了它们会把在途条目误判为缺席）。
//
// 语义（与旧候选口径逐字对齐）：
//   - terminal=false：来源尚无任何记录，必须铸新 commit；
//   - terminal=true, safe=false：存在 Queued 记录但呈现代（LayoutGeneration）
//     不一致 —— 必须回退重铸以触发 rebase，否则执行器 generation 闸门会一直
//     Defer 旧条目；
//   - terminal=true, safe=true：可整行保留；hasQueued=true 仅当存在 Queued 且
//     代一致（Delivered/已结算/压缩墓碑只标记 terminal，不并回 —— 旧实现同样把
//     已结算行排除在候选之外）。
func (s HistoryEffectQueueState) retainedQueuedCommitForSource(key historyCommitSourceKey, generation uint64) (commit HistoryCommit, hasQueued bool, terminal bool, safe bool) {
	if s.ledger == nil {
		return HistoryCommit{}, false, false, false
	}
	// A1-3 步 3b：未铸造 ⇒ 无任何记录（含压缩墓碑），必须铸新 commit。
	if !s.ledger.mintedThrough(key) {
		return HistoryCommit{}, false, false, false
	}
	if _, blocked := s.ledger.compactedTerminalSources[key]; blocked {
		return HistoryCommit{}, false, true, true
	}
	tokens := s.ledger.bySource[key]
	if len(tokens) == 0 {
		return HistoryCommit{}, false, false, false
	}
	terminal = false
	for token := range tokens {
		entry, ok := s.ledger.byToken[token]
		if !ok {
			continue
		}
		if !entry.BlocksRemint() {
			continue
		}
		terminal = true
		if entry.State == HistoryCommitQueued {
			if entry.Commit.LayoutGeneration != generation {
				return HistoryCommit{}, false, true, false
			}
			commit = entry.Commit
			hasQueued = true
		}
	}
	if !terminal {
		return HistoryCommit{}, false, false, false
	}
	return commit, hasQueued, true, true
}

// pruneCompactedSourcesNotInTranscript 转发 ledger 的装载边界墓碑剪枝：
// transcript 整体替换后，只有仍在当前 transcript 的 cell 需要保留阻断身份
// （见 HistoryCommitLedger.pruneCompactedSourcesNotInTranscript / A1-3 R5）。
func (s *HistoryEffectQueueState) pruneCompactedSourcesNotInTranscript(transcript TranscriptState) {
	if s == nil || s.ledger == nil {
		return
	}
	s.ledger.pruneCompactedSourcesNotInTranscript(transcript.Cells)
}

// recordTranscriptPlanTiming 记录一次 transcript 规划 pass 的耗时（P16 归因）。
// 耗时与计数分列：PlanCount>0 且 MaxPlanMs 接近冻结窗口，才说明 P12 的卡顿
// 花在规划器上。
func (s *HistoryEffectQueueState) recordTranscriptPlanTiming(duration time.Duration, phases transcriptPlanPhaseTiming) {
	if s == nil {
		return
	}
	s.PlanCount++
	s.LastPlanDuration = duration
	s.LastPlanPhases = phases
	if duration > s.MaxPlanDuration {
		s.MaxPlanDuration = duration
		s.MaxPlanPhases = phases
	}
}

// recordClaimRefusal classifies one refused BeginHistoryCommit claim. It is a
// pure counter update: the refusal must never raise recovery here (see the
// reducer's BeginHistoryCommit comment), but it must remain observable.
func (s *HistoryEffectQueueState) recordClaimRefusal(err error) {
	if s == nil || err == nil {
		return
	}
	switch {
	case errors.Is(err, ErrHistoryCommitOutOfOrder):
		s.claimRejectsOutOfOrder++
	case errors.Is(err, ErrHistoryCommitFrozen),
		errors.Is(err, ErrHistoryProjectionUnknown),
		errors.Is(err, ErrHistoryCommitRecoveryPending):
		s.claimRejectsGate++
	case errors.Is(err, ErrStaleLayoutGeneration):
		s.claimRejectsStale++
	default:
		s.claimRejectsInvalid++
	}
}

// HistoryEffectQueueSummary is a payload-free projection of the queue's
// delivery lifecycle for diagnostics.
type HistoryEffectQueueSummary struct {
	// LedgerEntries is the live ledger inventory (len(byToken)) *after* P2-1
	// terminal compaction: resolved entries whose payload no reader consumes are
	// pruned. It is the size driver of every ledger copy, so a diagnostic that
	// reports queue counters without it cannot tell "a short queue" from "a
	// long queue whose entries are all terminal".
	LedgerEntries int
	// LedgerCompacted counts entries retired by terminal compaction (monotonic),
	// and LedgerTerminalSources is the retained tombstone set: source identities
	// that must keep blocking a second mint within this terminal epoch. Together
	// they let a soak distinguish "compaction is happening" from "the ledger is
	// still growing without bound".
	LedgerCompacted       uint64
	LedgerTerminalSources int
	Queued                int
	Delivered             int
	// Quarantined counts every terminal non-delivery; the sub-counters keep the
	// two behaviours a soak must tell apart: unresolved entries still gate
	// recovery, settled entries no longer do. A1-3 步 3d 折叠后不再单列
	// "failed"：failed 与 partial-invalidated 在两轴上同形（未决、阻断重铸），
	// 统一计入 QuarantinedUnresolved。
	Quarantined            int
	QuarantinedUnresolved  int
	QuarantinedSettled     int
	OldestQueuedToken      uint64
	OldestQueuedGeneration uint64
	// ClaimedToken/Generation identify the handoff the single writer currently
	// holds (HistoryEffectQueueState.WriteCursor), if any. A claimed generation
	// older than the live layout generation is the stranded-claim signature (an
	// accepted claim whose detached batch could not be composed; see
	// TerminalSessionExecutor.releaseClaimMiss). Without the identity, a stuck
	// queue is indistinguishable from a healthy write in progress: the
	// counters alone read the same.
	ClaimedToken      uint64
	ClaimedGeneration uint64
	// PlanCount / LastPlanMs / MaxPlanMs 归因单次规划耗时（P16）：冻结窗口内
	// 只要有一次规划接近窗口时长，P12 的卡顿就应归到规划器，而不是布局/写。
	PlanCount  uint64
	LastPlanMs int64
	MaxPlanMs  int64
	// PlanCells/PlanScreenMs/PlanMintMs/PlanApplyMs 是**最大那次**规划的拆分
	// （cells = 规划时的 cell 数）：screen 含并行首渲染墙钟，mint 是铸 commit，
	// apply 是 membership reconcile + 入队。P16 的直接归因读数。
	PlanCells    int
	PlanScreenMs int64
	PlanMintMs   int64
	PlanApplyMs  int64
	// Claim-refusal counters mirror the same reducer-side events as the private
	// fields above; they are queue scalars, not ledger-derived (copied like the
	// plan timing in Summary).
	ClaimSkipsStaleAction  uint64
	ClaimRejectsOutOfOrder uint64
	ClaimRejectsGate       uint64
	ClaimRejectsStale      uint64
	ClaimRejectsInvalid    uint64
}

// Summary scans the ledger once and allocates nothing. It replaces the
// diagnostic Entries() walk, which detached every entry — render payload
// included — purely to count states.
func (s HistoryEffectQueueState) Summary() HistoryEffectQueueSummary {
	summary := HistoryEffectQueueSummary{}
	summary.PlanCount = s.PlanCount
	summary.LastPlanMs = s.LastPlanDuration.Milliseconds()
	summary.MaxPlanMs = s.MaxPlanDuration.Milliseconds()
	summary.PlanCells = s.MaxPlanPhases.cells
	summary.PlanScreenMs = s.MaxPlanPhases.screenMs
	summary.PlanMintMs = s.MaxPlanPhases.mintMs
	summary.PlanApplyMs = s.MaxPlanPhases.applyMs
	summary.ClaimSkipsStaleAction = s.claimSkipsStaleAction
	summary.ClaimRejectsOutOfOrder = s.claimRejectsOutOfOrder
	summary.ClaimRejectsGate = s.claimRejectsGate
	summary.ClaimRejectsStale = s.claimRejectsStale
	summary.ClaimRejectsInvalid = s.claimRejectsInvalid
	if s.ledger == nil {
		return summary
	}
	summary.LedgerEntries = len(s.ledger.byToken)
	summary.LedgerCompacted = s.ledger.compactedEntries
	summary.LedgerTerminalSources = len(s.ledger.compactedTerminalSources)
	if s.WriteCursor != 0 {
		summary.ClaimedToken = s.WriteCursor
		if entry, ok := s.ledger.byToken[s.WriteCursor]; ok {
			summary.ClaimedGeneration = entry.Commit.LayoutGeneration
		}
	}
	for token, entry := range s.ledger.byToken {
		switch entry.State {
		case HistoryCommitQueued:
			summary.Queued++
			// Tokens are minted ascending, so the smallest queued token is the
			// oldest eligible claim — the same head Pending() returns.
			if summary.OldestQueuedToken == 0 || token < summary.OldestQueuedToken {
				summary.OldestQueuedToken = token
				summary.OldestQueuedGeneration = entry.Commit.LayoutGeneration
			}
		case HistoryCommitDelivered:
			summary.Delivered++
		case HistoryCommitQuarantined:
			summary.Quarantined++
			if entry.Unresolved() {
				summary.QuarantinedUnresolved++
			} else if !entry.MayRemint {
				// settled 终态（已解决且永久阻断重铸）；invalidated-clean 是
				// 唯一"已解决且允许重铸"的隔离态，不计入 settled。
				summary.QuarantinedSettled++
			}
		}
	}
	return summary
}

// HistoryEffectDiagnostics is the ledger-free projection the debug endpoints
// read: queue scalars plus the delivery counters. This is everything
// /debug/chat/status needs from the queue, and building it allocates nothing.
type HistoryEffectDiagnostics struct {
	Frozen                 bool
	ProjectionUnknown      bool
	ReconciliationRequired bool
	// DeferHistoryDelivery reports a held windowed-load delivery (see
	// HistoryEffectQueueState.DeferHistoryDelivery).
	DeferHistoryDelivery bool
	// PlanIncomplete/PlanStalled 与 P1.2 worker 的这两项均为 deprecated 零值：
	// 续跑组已删除（P1-1 Stage 4）、worker 已删除（Stage 3），字段仅为
	// debug JSON/文档的跨包稳定性保留。
	PlanIncomplete       bool
	PlanStalled          bool
	PlanRequestInFlight  bool
	PlanWindowsDelegated uint64
	NextToken            uint64
	TerminalEpoch        uint64
	Summary              HistoryEffectQueueSummary
}

// Diagnostics projects the queue without detaching anything. The caller must
// hold the actor mutex because it reads the actor-owned ledger; see
// UIController.HistoryEffectDiagnostics.
func (s HistoryEffectQueueState) Diagnostics() HistoryEffectDiagnostics {
	return HistoryEffectDiagnostics{
		Frozen:                 s.Frozen,
		ProjectionUnknown:      s.ProjectionUnknown,
		ReconciliationRequired: s.ReconciliationRequired,
		DeferHistoryDelivery:   s.DeferHistoryDelivery,
		NextToken:              s.NextToken,
		TerminalEpoch:          s.TerminalEpoch,
		Summary:                s.Summary(),
	}
}

func (s *HistoryEffectQueueState) enqueue(commit HistoryCommit) error {
	if s == nil {
		return ErrInvalidHistoryCommit
	}
	if s.ledger == nil {
		s.ledger = NewHistoryCommitLedger()
	}
	autoToken := commit.Token == 0
	if autoToken {
		commit.Token = s.NextToken + 1
	}
	if err := s.ledger.Enqueue(commit); err != nil {
		return err
	}
	if autoToken || commit.Token > s.NextToken {
		s.NextToken = commit.Token
	}
	return nil
}

func (s *HistoryEffectQueueState) markInFlight(token, generation uint64) error {
	if s == nil || s.ledger == nil {
		return ErrCommitNotPending
	}
	if s.Frozen {
		return ErrHistoryCommitFrozen
	}
	if s.ProjectionUnknown {
		return ErrHistoryProjectionUnknown
	}
	if s.hasUnresolvedTerminalDelivery() {
		return ErrHistoryCommitRecoveryPending
	}
	entry, ok := s.ledger.Entry(token)
	if !ok || entry.State != HistoryCommitQueued || entry.Commit.LayoutGeneration != generation {
		return ErrStaleLayoutGeneration
	}
	// Single physical writer: a second, different claim while a token may still
	// be in flight is out of order. Re-claiming the same token is idempotent
	// (the executor retries a claim whose snapshot read found nothing).
	if s.WriteCursor != 0 && s.WriteCursor != token {
		return ErrHistoryCommitOutOfOrder
	}
	// Native scrollback is ordered. Do not let a stale presenter claim a later
	// token while an earlier eligible effect has not reached a terminal result.
	if s.ledger.hasOlderQueuedToken(token) {
		return ErrHistoryCommitOutOfOrder
	}
	s.WriteCursor = token
	return nil
}

func (s *HistoryEffectQueueState) rebasePending(commit HistoryCommit) error {
	if s == nil || s.ledger == nil {
		return ErrCommitNotPending
	}
	for _, entry := range s.ledger.byToken {
		current := entry.Commit
		// The write cursor token may already be physically written: rebasing it
		// would let an old payload be acknowledged against new semantics. The
		// planner keeps it (identity-valid presentation drift is counted as
		// ClaimedPresentationDrift; G4/C1) and the executor's generation gate
		// releases it via Deferred before the next rebase; every other Pending
		// entry rebases in place.
		if entry.State != HistoryCommitQueued || current.Token == s.WriteCursor ||
			current.CellID != commit.CellID ||
			current.Revision != commit.Revision ||
			current.SourceRange != commit.SourceRange ||
			current.FragmentID != commit.FragmentID {
			continue
		}
		return s.ledger.RebasePending(current.Token, commit)
	}
	return ErrCommitNotPending
}

func (s *HistoryEffectQueueState) invalidate(token uint64) error {
	if s == nil || s.ledger == nil {
		return ErrCommitNotPending
	}
	if s.WriteCursor == token {
		// Claimed: a physical write may be in flight, but the cursor only says
		// the claim exists — not that bytes crossed the writer. Record the
		// invalidation intent and let the write result (Committed / zero-write
		// / partial) resolve it with its proof instead of guessing here.
		return s.ledger.MarkInvalidationPending(token)
	}
	return s.ledger.Invalidate(token, false)
}

func (s *HistoryEffectQueueState) ack(token, frame, generation uint64) error {
	if s == nil || s.ledger == nil {
		return ErrCommitNotInFlight
	}
	if s.WriteCursor == token {
		if entry, ok := s.ledger.Entry(token); ok && entry.InvalidationPending {
			// The write committed, but the payload was invalidated while it was
			// claimed: old bytes are on screen without a matching source. Record
			// the physical fact and raise the recovery obligation instead of
			// acknowledging the invalidated payload as delivered.
			if err := s.ledger.ResolveInvalidation(token, true); err != nil {
				return err
			}
			s.WriteCursor = 0
			s.ProjectionUnknown = true
			s.ReconciliationRequired = true
			return nil
		}
	}
	// Only the token the single writer currently holds may be acknowledged.
	// A duplicate ack of an already-Acked token keeps its dedicated error so
	// diagnostics can still distinguish "acked twice" from "never claimed".
	if s.WriteCursor != token {
		if entry, ok := s.ledger.Entry(token); ok && entry.State == HistoryCommitDelivered {
			return s.ledger.Ack(token, frame, generation)
		}
		return ErrCommitNotInFlight
	}
	if err := s.ledger.Ack(token, frame, generation); err != nil {
		return err
	}
	s.WriteCursor = 0
	// P2-1 终态压缩：ack 是终态转换的主入口，库存超过高水位时在这里把最老已
	// 终结条目回收到目标水位。窗口化保证近期 ack（帧与状态）仍可读，同时库存
	// 有界；Acked+Active 提供前缀证明的条目永不参与回收。
	s.ledger.compactResolvedIfLarge()
	return nil
}

// ackBatch resolves one covered batch: the exact ordered commits a single
// terminal transaction wrote. Every covered token is resolved from its own
// ledger state with the same proof classification as the single-token path, so
// a token that can no longer be proven semantically becomes unresolved on its
// own instead of quarantining the whole batch. Returns whether the batch left
// an unresolved delivery; a non-nil error means the covered set itself was
// malformed (executor invariant violation) and failed closed as a whole.
func (s *HistoryEffectQueueState) ackBatch(commits []HistoryCommit, frame, generation uint64) (bool, error) {
	if s == nil || s.ledger == nil {
		return false, ErrCommitNotInFlight
	}
	if len(commits) == 0 {
		return false, nil
	}

	// Shape first: the covered set must be an ordered token prefix whose head
	// carries the claimed transaction generation. A violation cannot describe
	// a physical write, so the whole set fails closed before any token moves.
	previousToken := uint64(0)
	for index, commit := range commits {
		if commit.Token == 0 || (previousToken != 0 && commit.Token <= previousToken) ||
			(index == 0 && commit.LayoutGeneration != generation) {
			return s.quarantineCoveredBatch(commits, ErrStaleLayoutGeneration), ErrStaleLayoutGeneration
		}
		previousToken = commit.Token
	}

	unresolved := false
	for index, commit := range commits {
		entry, ok := s.ledger.Entry(commit.Token)
		if !ok {
			// A compacted token was already resolved; a source with no terminal
			// record at all cannot have been minted by this ledger.
			if !s.ledger.hasTerminalRecordForSource(historyCommitSourceIdentity(commit)) {
				return s.quarantineCoveredBatch(commits, ErrCommitSourceChanged), ErrCommitSourceChanged
			}
			continue
		}
		if entry.State != HistoryCommitQueued {
			// Already resolved (delivered / quarantined / settled): the proof
			// adds no new physical fact.
			continue
		}
		switch {
		case entry.InvalidationPending:
			// Committed bytes whose source was invalidated while claimed: the
			// single-token classification applies per token (unresolved
			// isolation), instead of a batch-wide failure.
			s.ackCoveredToken(commit.Token, frame, generation)
			unresolved = true
		case index == 0 && s.WriteCursor != commit.Token:
			// The claim that consumed this transaction is gone; the delivered
			// bytes cannot be attributed to a retryable token.
			s.quarantineCoveredToken(commit.Token, ErrCommitNotInFlight)
			unresolved = true
		case historyCommitSourceIdentity(entry.Commit) != historyCommitSourceIdentity(commit):
			// The source was replaced while the write was in flight: the bytes
			// on screen no longer match any live source.
			s.quarantineCoveredToken(commit.Token, ErrCommitSourceChanged)
			unresolved = true
		case !historyCommitPresentationEqual(entry.Commit, commit):
			if index > 0 && entry.Commit.LayoutGeneration > commit.LayoutGeneration {
				// A resize raced the write and rebased this still-Pending
				// follower onto the newer layout generation. Identity is
				// unchanged and the newer generation only re-presents the same
				// delivered range, so the proven handoff stays delivered at its
				// own generation; failing it would raise an obligation that the
				// no-replay policy can never repay.
				s.ackCoveredToken(commit.Token, frame, entry.Commit.LayoutGeneration)
			} else {
				// Same-generation content change or a rebased first/in-flight
				// token: the delivered bytes no longer match the planned source.
				s.quarantineCoveredToken(commit.Token, ErrCommitSourceChanged)
				unresolved = true
			}
		default:
			s.ackCoveredToken(commit.Token, frame, entry.Commit.LayoutGeneration)
		}
	}
	return unresolved, nil
}

// ackCoveredToken resolves one proven covered token exactly like the
// sequential single-token path: the covered batch temporarily advances the
// single write cursor through its ordered tokens, and each ack clears it.
func (s *HistoryEffectQueueState) ackCoveredToken(token, frame, generation uint64) {
	s.WriteCursor = token
	if err := s.ack(token, frame, generation); err != nil {
		// A covered token that cannot be acknowledged despite matching its
		// ledger record is a ledger invariant violation; keep the physical
		// fact by quarantining it instead of silently dropping the proof.
		s.quarantineCoveredToken(token, err)
	}
}

// quarantineCoveredToken records the physical fact for one covered token whose
// bytes can no longer be proven against a live source. Returns whether the
// token is unresolved (recovery obligation) after the transition. Only the
// genuinely ambiguous token changes state; its neighbours keep their own
// resolution.
func (s *HistoryEffectQueueState) quarantineCoveredToken(token uint64, cause error) bool {
	if s == nil || s.ledger == nil {
		return false
	}
	entry, ok := s.ledger.byToken[token]
	if !ok || entry.State == HistoryCommitDelivered {
		return false
	}
	wasUnresolved := entry.Unresolved()
	switch entry.State {
	case HistoryCommitQueued:
		entry.State = HistoryCommitQuarantined
		entry.UnresolvedDelivery = true
		entry.MayRemint = false
	case HistoryCommitQuarantined:
		// Failed / settled keep their axes (settled 保持"已解决"语义，强化
		// 只写物理事实不改恢复义务）；invalidated-clean 的物理事实强化为
		// "可能已部分落盘"：撤回重铸许可并转为未决。
		if entry.MayRemint {
			entry.UnresolvedDelivery = true
			entry.MayRemint = false
		}
	}
	entry.Failure = cause
	entry.MayHavePartiallyWritten = true
	entry.InvalidationPending = false
	// Keep the unresolved counter monotonic: only a transition into an
	// unresolved state increments it; already-unresolved entries do not.
	if !wasUnresolved && entry.Unresolved() {
		s.ledger.unresolvedCount++
	}
	s.ledger.byToken[token] = entry
	// Queued -> Quarantined is a terminal transition; keep the cached minimum
	// non-terminal token from pinning on this token.
	s.ledger.advanceQueueHeadAfterTerminal(token)
	// A claimed batch member can never be written again: release the cursor or
	// it would pin ordered handoff behind a terminal token forever.
	if s.WriteCursor == token {
		s.WriteCursor = 0
	}
	return entry.Unresolved()
}

// quarantineCoveredBatch fails a malformed covered set closed: every token it
// names keeps its recorded physical fact. Returns whether any token is
// unresolved after the pass.
func (s *HistoryEffectQueueState) quarantineCoveredBatch(commits []HistoryCommit, cause error) bool {
	unresolved := false
	for _, commit := range commits {
		if s.quarantineCoveredToken(commit.Token, cause) {
			unresolved = true
		}
	}
	return unresolved
}

func (s *HistoryEffectQueueState) deferInFlight(token, generation uint64) error {
	if s == nil || s.ledger == nil {
		return ErrCommitNotInFlight
	}
	entry, ok := s.ledger.Entry(token)
	if !ok || entry.Commit.LayoutGeneration != generation {
		return ErrStaleLayoutGeneration
	}
	if entry.InvalidationPending && s.WriteCursor == token {
		// Deferred carries the zero-write proof: nothing reached the host, so
		// the invalidation is clean and the projection stays known.
		if err := s.ledger.ResolveInvalidation(token, false); err != nil {
			return err
		}
		s.WriteCursor = 0
		return nil
	}
	// Deferred means the terminal transaction did not start, so the entry stays
	// Pending and only the write cursor is released. A Deferred for a token that
	// does not hold the cursor is a safe no-op refusal: the executor posts it
	// unconditionally on the claim-miss path, where the claim may have been
	// refused before ever setting the cursor.
	if entry.State != HistoryCommitQueued || s.WriteCursor != token {
		return ErrCommitNotInFlight
	}
	s.WriteCursor = 0
	return nil
}

// claimedPresentationDrifted reports whether a still-claimed token's candidate
// presentation diverged (layout generation or display range). Payload lines are
// intentionally not compared: Active-origin commits snapshot a fixed source
// prefix, so later source growth is normal streaming, not drift.
func (s *HistoryEffectQueueState) claimedPresentationDrifted(candidate HistoryCommit) bool {
	if s == nil || s.ledger == nil || s.WriteCursor == 0 {
		return false
	}
	entry, ok := s.ledger.Entry(s.WriteCursor)
	if !ok || entry.State != HistoryCommitQueued {
		return false
	}
	if historyCommitSourceIdentity(entry.Commit) != historyCommitSourceIdentity(candidate) {
		return false
	}
	return entry.Commit.LayoutGeneration != candidate.LayoutGeneration ||
		entry.Commit.DisplayRange != candidate.DisplayRange
}

// noteClaimedPresentationDrift increments the diagnostic counter when the
// claimed token's candidate presentation drifted. Returns true when counted.
func (s *HistoryEffectQueueState) noteClaimedPresentationDrift(candidate HistoryCommit) bool {
	if !s.claimedPresentationDrifted(candidate) {
		return false
	}
	s.ClaimedPresentationDrift++
	return true
}

func (s *HistoryEffectQueueState) fail(token, generation uint64, err error, mayHavePartiallyWritten bool) error {
	if s == nil || s.ledger == nil {
		return ErrCommitNotInFlight
	}
	entry, ok := s.ledger.Entry(token)
	if !ok || entry.Commit.LayoutGeneration != generation {
		return ErrStaleLayoutGeneration
	}
	if entry.InvalidationPending {
		// The invalidation dominates the failure classification: the source is
		// gone, and the proof fact only decides whether recovery is owed.
		if err := s.ledger.ResolveInvalidation(token, mayHavePartiallyWritten); err != nil {
			return err
		}
		if s.WriteCursor == token {
			s.WriteCursor = 0
		}
		if mayHavePartiallyWritten {
			s.ProjectionUnknown = true
			s.ReconciliationRequired = true
		}
		return nil
	}
	if err := s.ledger.Fail(token, err, mayHavePartiallyWritten); err != nil {
		return err
	}
	if s.WriteCursor == token {
		s.WriteCursor = 0
	}
	// Any failed terminal transaction means the cached physical projection can
	// no longer prove which bytes reached the terminal. Recovery must repaint
	// from semantic source instead of blind retrying the same handoff batch.
	s.ProjectionUnknown = true
	s.ReconciliationRequired = true
	return nil
}

func (s *HistoryEffectQueueState) markProjectionKnown() {
	if s != nil {
		s.ProjectionUnknown = false
	}
}

// invalidateTranscriptPlanMemo drops the fingerprint memo that lets
// syncHistoryEffectsForTranscript skip a full plan while every plan input is
// unchanged. The memo covers transcript/layout/theme/epoch inputs, so it cannot
// observe that the *ledger* a plan was reconciled into has since been replaced
// wholesale or never received that plan at all (a replacement snapshot an
// earlier reduction installed while the geometry was still zero, which records
// the memo against an empty candidate set). A session load re-proves the plan
// from source instead of trusting the fingerprint, so the loaded generation is
// always planned against the ledger that actually exists.
func (s *HistoryEffectQueueState) invalidateTranscriptPlanMemo() {
	if s != nil {
		s.lastPlannedTranscriptValid = false
	}
}

// settleUnresolvedWithoutReplay is the normal-interaction recovery policy: a
// writer failure or invalidated in-flight token must not trigger a scrollback
// replay, so the unproven range is quarantined in the ledger and the queue
// resumes ordered delivery after a source-backed viewport repaint.
func (s *HistoryEffectQueueState) settleUnresolvedWithoutReplay() bool {
	if s == nil {
		return false
	}
	settled := s.ledger.SettleUnresolvedWithoutReplay()
	s.ProjectionUnknown = false
	s.ReconciliationRequired = false
	return settled
}

// hasTerminalRecordForSource reports whether this semantic range already has
// a lifecycle that must not be minted again. Invalidated pending work may be
// replanned if the same source becomes eligible later; an invalidated
// in-flight effect is different because it may already have written bytes and
// remains blocked behind projection recovery.
func (s HistoryEffectQueueState) hasTerminalRecordForSource(commit HistoryCommit) bool {
	return s.ledger.hasTerminalRecordForSource(historyCommitSourceIdentity(commit))
}

// hasUnresolvedTerminalDelivery prevents a recovered viewport from silently
// handing off a later cell after an earlier terminal transaction failed or was
// invalidated in flight. A full primary repaint can restore the visible frame,
// but it cannot prove what reached native scrollback; that range needs an
// explicit reconciliation policy before ordered history delivery resumes.
func (s HistoryEffectQueueState) hasUnresolvedTerminalDelivery() bool {
	return s.ledger.hasUnresolvedTerminalDelivery()
}
