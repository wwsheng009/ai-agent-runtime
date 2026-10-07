package ui

import (
	"errors"
	"fmt"
	"sort"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

var (
	ErrInvalidHistoryCommit = errors.New("invalid history commit")
	ErrDuplicateCommitToken = errors.New("duplicate history commit token")
	ErrDuplicateCommitRange = errors.New("duplicate history commit range")
	// ErrCommitNotPending / ErrCommitNotInFlight 是历史命名：三态归一后两者都
	// 表示"条目不是 queued（不可 claim/ack/fail）"。保留名字以稳定既有错误
	// 分类与诊断，不表示仍有独立的状态。
	ErrCommitNotPending        = errors.New("history commit is not pending")
	ErrCommitNotInFlight       = errors.New("history commit is not in flight")
	ErrHistoryCommitOutOfOrder = errors.New("history commit is not the oldest eligible effect")
	ErrDuplicateCommitAck      = errors.New("duplicate history commit acknowledgement")
	ErrStaleLayoutGeneration   = errors.New("stale history commit layout generation")
	ErrCommitSourceChanged     = errors.New("history commit source identity changed")
)

// SourceRange identifies a half-open range in one semantic cell source.
// It is not a physical terminal-row range.
type SourceRange struct {
	Start int
	End   int
}

func (r SourceRange) Valid() bool {
	return r.Start >= 0 && r.End >= r.Start
}

// DisplayRange identifies the half-open derived display-row range emitted by
// one layout generation.
type DisplayRange struct {
	Start int
	End   int
}

func (r DisplayRange) Valid() bool {
	return r.Start >= 0 && r.End >= r.Start
}

// HistoryCommit is the typed terminal effect for moving finalized display rows
// into native scrollback. Token, cell identity, revision, both ranges, and
// layout generation are all required; text is payload, never identity.
type HistoryCommit struct {
	Token       uint64
	CellID      scene.CellID
	Revision    uint64
	SourceRange SourceRange
	// FragmentID is zero for source-preserving commits. Rich renderers use a
	// stable non-zero ordinal to identify a physical presentation fragment when
	// a single semantic source range produces several display rows.
	FragmentID       uint64
	DisplayRange     DisplayRange
	LayoutGeneration uint64
	Lines            []render.Line
}

func (c HistoryCommit) Valid() bool {
	return c.Token != 0 && c.CellID != 0 && c.LayoutGeneration != 0 &&
		c.SourceRange.Valid() && c.SourceRange.End > c.SourceRange.Start &&
		c.DisplayRange.Valid() && c.DisplayRange.End > c.DisplayRange.Start
}

func (c HistoryCommit) Clone() HistoryCommit {
	c.Lines = cloneRenderLines(c.Lines)
	return c
}

// HistoryCommitState records terminal-effect progress separately from source
// and physical projection state. P1-1 六态归一：交付生命周期只有三态，物理写
// 窗口不再是一个状态，而由 HistoryEffectQueueState.WriteCursor 单独表达。
type HistoryCommitState uint8

const (
	// HistoryCommitQueued：已入队、未交付。可能已被 executor claim（游标指向）
	// 也可能还没有；两种情况下都还没有终态交付证明，且都会阻断更晚 token 的写。
	HistoryCommitQueued HistoryCommitState = iota
	// HistoryCommitDelivered：物理写成功并已进滚动区（原 Acked）。
	HistoryCommitDelivered
	// HistoryCommitQuarantined：交付不可证明或被拒绝（原 Failed/Invalidated/
	// Abandoned）。不再阻断有序交付，但按 Quarantine 子类保留未决计数、压缩
	// 资格与"同源防重铸"三个行为差异。
	HistoryCommitQuarantined
)

// HistoryCommitQuarantine 是 quarantined 的子类，只保留无法合并的行为差异：
//   - Failed：写失败，恒未决（settle 前不可压缩、阻断同源再铸）；
//   - Invalidated：语义替换取消；MayHavePartiallyWritten 时未决且阻断再铸，
//     否则已解决、可压缩、不阻断同源再铸；
//   - Settled：settle 后的隔离终态：不再未决、可压缩，但永久阻断同源再铸
//     （无重放策略下不可由内存证明的交付既不重发也不重铸）。
type HistoryCommitQuarantine uint8

const (
	HistoryCommitQuarantineNone HistoryCommitQuarantine = iota
	HistoryCommitQuarantineFailed
	HistoryCommitQuarantineInvalidated
	HistoryCommitQuarantineSettled
)

func (s HistoryCommitState) String() string {
	switch s {
	case HistoryCommitQueued:
		return "queued"
	case HistoryCommitDelivered:
		return "delivered"
	case HistoryCommitQuarantined:
		return "quarantined"
	default:
		return "unknown"
	}
}

func (q HistoryCommitQuarantine) String() string {
	switch q {
	case HistoryCommitQuarantineFailed:
		return "failed"
	case HistoryCommitQuarantineInvalidated:
		return "invalidated"
	case HistoryCommitQuarantineSettled:
		return "settled"
	default:
		return "none"
	}
}

// HistoryCommitEntry is an immutable snapshot of one ledger item.
type HistoryCommitEntry struct {
	Commit                  HistoryCommit
	State                   HistoryCommitState
	Quarantine              HistoryCommitQuarantine
	AckFrame                uint64
	MayHavePartiallyWritten bool
	Failure                 error
	// InvalidationPending marks a Queued entry whose source/presentation was
	// invalidated while a write claim was outstanding. The claim is retained
	// (the cursor still names it) and the write result action resolves the
	// invalidation with the proof fact: Deferred/zero-write -> clean
	// invalidation, Committed -> invalidated with bytes on screen, partial
	// failure -> unresolved. The cursor alone never decides this.
	InvalidationPending bool
}

func (e HistoryCommitEntry) Clone() HistoryCommitEntry {
	e.Commit = e.Commit.Clone()
	return e
}

// Unresolved reports whether the delivery still needs terminal recovery: a
// failed write is always unresolved, an invalidated write only when bytes may
// have partially landed. Settled quarantines are resolved by definition.
func (e HistoryCommitEntry) Unresolved() bool {
	return e.Quarantine == HistoryCommitQuarantineFailed ||
		(e.Quarantine == HistoryCommitQuarantineInvalidated && e.MayHavePartiallyWritten)
}

func (e HistoryCommitEntry) IsFailed() bool {
	return e.State == HistoryCommitQuarantined && e.Quarantine == HistoryCommitQuarantineFailed
}

func (e HistoryCommitEntry) IsInvalidated() bool {
	return e.State == HistoryCommitQuarantined && e.Quarantine == HistoryCommitQuarantineInvalidated
}

func (e HistoryCommitEntry) IsSettled() bool {
	return e.State == HistoryCommitQuarantined && e.Quarantine == HistoryCommitQuarantineSettled
}

// BlocksRemint reports whether this entry's source identity must block a second
// mint of the same range within the terminal epoch. Delivered and every
// quarantine kind block except a pure (non-partial) invalidation, whose source
// was never physically delivered and is therefore free to be planned again.
func (e HistoryCommitEntry) BlocksRemint() bool {
	switch e.State {
	case HistoryCommitQueued, HistoryCommitDelivered:
		return true
	case HistoryCommitQuarantined:
		return e.Quarantine != HistoryCommitQuarantineInvalidated || e.MayHavePartiallyWritten
	default:
		return false
	}
}

type historyCommitRangeKey struct {
	cellID           scene.CellID
	revision         uint64
	sourceStart      int
	sourceEnd        int
	fragmentID       uint64
	layoutGeneration uint64
}

type historyCommitSourceKey struct {
	cellID      scene.CellID
	revision    uint64
	sourceStart int
	sourceEnd   int
	fragmentID  uint64
}

func historyCommitSourceIdentity(commit HistoryCommit) historyCommitSourceKey {
	return historyCommitSourceKey{
		cellID:      commit.CellID,
		revision:    commit.Revision,
		sourceStart: commit.SourceRange.Start,
		sourceEnd:   commit.SourceRange.End,
		fragmentID:  commit.FragmentID,
	}
}

func historyCommitKey(c HistoryCommit) historyCommitRangeKey {
	return historyCommitRangeKey{
		cellID:           c.CellID,
		revision:         c.Revision,
		sourceStart:      c.SourceRange.Start,
		sourceEnd:        c.SourceRange.End,
		fragmentID:       c.FragmentID,
		layoutGeneration: c.LayoutGeneration,
	}
}

// HistoryCommitLedger is reducer-owned effect progress. It deliberately has
// no terminal I/O and does not infer identity from line text or hashes.
type HistoryCommitLedger struct {
	byToken  map[uint64]HistoryCommitEntry
	byRange  map[historyCommitRangeKey]uint64
	bySource map[historyCommitSourceKey]map[uint64]struct{}
	// queueHeadToken 是队列头指针：最小未交付（Queued）token，0 = 队列为空。
	// 它与 HistoryEffectQueueState.WriteCursor（交付游标，可能正在物理写的
	// token）配对：claim 只允许头指针位置，交付/取消后头指针向前跳过终态。
	// P1-1 第 3 步：pendingCount 已删除 —— 空队列判据直接由头指针表达
	// （HasPending），不再维护一个与状态转移并行的计数镜像。production pprof
	// 显示旧的整表扫描在 per-claim 排序护栏上是热点，因此头指针只向前推进，
	// 终态吸收（Delivered/Quarantined 不回退）保证跳过扫描摊还 O(1)。
	queueHeadToken uint64
	// unresolvedCount caches the number of entries that require terminal
	// recovery (failed, or invalidated with MayHavePartiallyWritten). It keeps
	// hasUnresolvedTerminalDelivery O(1); production pprof showed the previous
	// full-map scan inside the per-action wake predicate as a hot spot.
	unresolvedCount int
	// compactedTerminalSources 是被终态压缩剪除、但来源身份仍必须阻断再次铸造的
	// source key 最小集（Delivered/Settled 等阻断态）。P2-1：Delivered transcript 条目
	// 与 settle 后的隔离终态条目不再被任何读取方消费（行载荷已置 nil 或无用），
	// 但 "该来源已交付" 必须永远阻断重复铸造（append-only 去重）；只有外部整体
	// 替换 ledger 时随之清零。
	compactedTerminalSources map[historyCommitSourceKey]struct{}
	// compactedEntries 是累计剪除的条目数（单调，soak 观测用：证明压缩确实发生）。
	compactedEntries uint64
	// tokens is the deduplicated, ascending set of live token identities. It
	// mirrors byToken keys so orderedTokens avoids a per-call sort allocation;
	// 终态压缩成对删除 byToken 与这里的元素，因此镜像始终精确。
	tokens []uint64
}

func NewHistoryCommitLedger() *HistoryCommitLedger {
	return &HistoryCommitLedger{
		byToken:                  make(map[uint64]HistoryCommitEntry),
		byRange:                  make(map[historyCommitRangeKey]uint64),
		bySource:                 make(map[historyCommitSourceKey]map[uint64]struct{}),
		compactedTerminalSources: make(map[historyCommitSourceKey]struct{}),
		tokens:                   make([]uint64, 0, 64),
	}
}

func (l *HistoryCommitLedger) Enqueue(commit HistoryCommit) error {
	if l == nil {
		return fmt.Errorf("%w: nil ledger", ErrInvalidHistoryCommit)
	}
	if l.byToken == nil {
		l.byToken = make(map[uint64]HistoryCommitEntry)
	}
	if l.byRange == nil {
		l.byRange = make(map[historyCommitRangeKey]uint64)
	}
	if l.bySource == nil {
		l.bySource = make(map[historyCommitSourceKey]map[uint64]struct{})
	}
	if !commit.Valid() {
		return ErrInvalidHistoryCommit
	}
	if _, exists := l.byToken[commit.Token]; exists {
		return ErrDuplicateCommitToken
	}
	key := historyCommitKey(commit)
	if token, exists := l.byRange[key]; exists {
		previous := l.byToken[token]
		if previous.BlocksRemint() {
			return ErrDuplicateCommitRange
		}
	}
	sourceKey := historyCommitSourceIdentity(commit)
	// 已压缩的阻断身份仍然生效：调用方绕过 enqueueHistoryCandidates 的直接入队
	// （测试、手写状态）同样不得二次铸造同一来源。
	if _, blocked := l.compactedTerminalSources[sourceKey]; blocked {
		return ErrDuplicateCommitRange
	}
	l.byToken[commit.Token] = HistoryCommitEntry{Commit: commit.Clone(), State: HistoryCommitQueued}
	l.byRange[key] = commit.Token
	l.tokens = insertSortedToken(l.tokens, commit.Token)
	if l.bySource[sourceKey] == nil {
		l.bySource[sourceKey] = make(map[uint64]struct{})
	}
	l.bySource[sourceKey][commit.Token] = struct{}{}
	if l.queueHeadToken == 0 || commit.Token < l.queueHeadToken {
		l.queueHeadToken = commit.Token
	}
	return nil
}

// RebasePending updates only the display payload of an unstarted effect after
// a layout generation change. Token and semantic source identity are retained;
// resize therefore never creates a second history handoff token.
func (l *HistoryCommitLedger) RebasePending(token uint64, replacement HistoryCommit) error {
	entry, ok := l.entry(token)
	if !ok || entry.State != HistoryCommitQueued {
		return ErrCommitNotPending
	}
	current := entry.Commit
	if current.CellID != replacement.CellID || current.Revision != replacement.Revision ||
		current.SourceRange != replacement.SourceRange || current.FragmentID != replacement.FragmentID {
		return ErrCommitSourceChanged
	}
	replacement.Token = token
	if !replacement.Valid() {
		return ErrInvalidHistoryCommit
	}
	oldKey := historyCommitKey(current)
	newKey := historyCommitKey(replacement)
	if existing, exists := l.byRange[newKey]; exists && existing != token {
		return ErrDuplicateCommitRange
	}
	delete(l.byRange, oldKey)
	entry.Commit = replacement.Clone()
	l.byToken[token] = entry
	l.byRange[newKey] = token
	return nil
}

// Invalidate prevents a queued effect from being consumed after transcript
// replacement. mayHavePartiallyWritten is the physical fact supplied by the
// caller: true only when a write provably started (or its coverage is
// unknown). A partially written cancellation counts as an unresolved terminal
// delivery; the counter is monotonic (unresolved entries never revert).
//
// Callers that hold a claim must not guess this fact from the write cursor:
// record the intent with MarkInvalidationPending and resolve it from the write
// result action (which carries the proof).
func (l *HistoryCommitLedger) Invalidate(token uint64, mayHavePartiallyWritten bool) error {
	return l.invalidateQueued(token, mayHavePartiallyWritten)
}

// MarkInvalidationPending records that a queued entry must be invalidated, but
// keeps it (and its write claim) alive until the in-flight write result
// arrives. The result resolves the pending invalidation with its proof fact.
func (l *HistoryCommitLedger) MarkInvalidationPending(token uint64) error {
	entry, ok := l.entry(token)
	if !ok || entry.State != HistoryCommitQueued {
		return ErrCommitNotPending
	}
	if !entry.InvalidationPending {
		entry.InvalidationPending = true
		l.byToken[token] = entry
	}
	return nil
}

// ResolveInvalidation completes a pending invalidation with the physical fact
// carried by the write result action.
func (l *HistoryCommitLedger) ResolveInvalidation(token uint64, mayHavePartiallyWritten bool) error {
	entry, ok := l.entry(token)
	if !ok || entry.State != HistoryCommitQueued || !entry.InvalidationPending {
		return ErrCommitNotPending
	}
	entry.InvalidationPending = false
	l.byToken[token] = entry
	return l.invalidateQueued(token, mayHavePartiallyWritten)
}

func (l *HistoryCommitLedger) invalidateQueued(token uint64, mayHavePartiallyWritten bool) error {
	entry, ok := l.entry(token)
	if !ok || entry.State != HistoryCommitQueued {
		return ErrCommitNotPending
	}
	entry.State = HistoryCommitQuarantined
	entry.Quarantine = HistoryCommitQuarantineInvalidated
	entry.InvalidationPending = false
	if mayHavePartiallyWritten {
		l.unresolvedCount++
		entry.MayHavePartiallyWritten = true
	}
	l.byToken[token] = entry
	l.advanceQueueHeadAfterTerminal(token)
	return nil
}

// Ack accepts a terminal effect only for the layout generation that produced
// it. A stale acknowledgement remains in-flight for the caller's recovery
// policy; it never advances semantic handoff progress.
func (l *HistoryCommitLedger) Ack(token, frame, currentGeneration uint64) error {
	entry, ok := l.entry(token)
	if !ok {
		return ErrCommitNotInFlight
	}
	if entry.State == HistoryCommitDelivered {
		return ErrDuplicateCommitAck
	}
	if entry.State != HistoryCommitQueued {
		return ErrCommitNotInFlight
	}
	if entry.Commit.LayoutGeneration != currentGeneration {
		return ErrStaleLayoutGeneration
	}
	entry.State = HistoryCommitDelivered
	entry.AckFrame = frame
	entry.Failure = nil
	entry.MayHavePartiallyWritten = false
	// Transcript source can always rebuild its payload; nothing retains rendered
	// lines past ack (A2 第二刀后不再有 Active-origin 交付).
	entry.Commit.Lines = nil
	l.byToken[token] = entry
	l.advanceQueueHeadAfterTerminal(token)
	return nil
}

// nextQueuedToken returns the smallest live token greater than from that is
// still Queued, or 0 when none remains. It walks the cached
// ascending token slice; the walk is bounded by the number of consecutive
// terminal tokens after from, so the total cost is amortized O(1) per
// terminal transition across the ledger's lifetime.
func (l *HistoryCommitLedger) nextQueuedToken(from uint64) uint64 {
	tokens := l.orderedTokens()
	// Binary-search the first token above `from`, then scan forward only over
	// the terminal run. The previous implementation skipped the token prefix
	// with a linear walk, which is O(total ledger) per Ack — on resumed
	// sessions with tens of thousands of accumulated tokens that alone pinned
	// a core (production pprof: nextNonTerminalToken 9% flat). Acks proceed
	// oldest-first, so the forward scan stays amortized O(1) per transition.
	index := sort.Search(len(tokens), func(i int) bool { return tokens[i] > from })
	for ; index < len(tokens); index++ {
		if entry, ok := l.byToken[tokens[index]]; ok &&
			entry.State == HistoryCommitQueued {
			return tokens[index]
		}
	}
	return 0
}

// advanceQueueHeadAfterTerminal moves the queue-head cursor past token after a
// Queued -> terminal transition (Delivered or Quarantined). Terminal states are
// absorbing, so the head only ever moves forward and the bounded recompute scan
// stays amortized O(1) per terminal transition. Skipping this refresh pins the
// head on a terminal token and makes hasOlderQueuedToken report a phantom older
// effect forever, which deadlocks every later claim behind out-of-order
// rejection.
func (l *HistoryCommitLedger) advanceQueueHeadAfterTerminal(token uint64) {
	if l.queueHeadToken == token {
		l.queueHeadToken = l.nextQueuedToken(token)
	}
}

func (l *HistoryCommitLedger) Fail(token uint64, err error, mayHavePartiallyWritten bool) error {
	entry, ok := l.entry(token)
	if !ok || entry.State != HistoryCommitQueued {
		return ErrCommitNotInFlight
	}
	entry.State = HistoryCommitQuarantined
	entry.Quarantine = HistoryCommitQuarantineFailed
	entry.Failure = err
	entry.MayHavePartiallyWritten = mayHavePartiallyWritten
	// A failed quarantine is always unresolved regardless of
	// MayHavePartiallyWritten.
	l.unresolvedCount++
	l.byToken[token] = entry
	l.advanceQueueHeadAfterTerminal(token)
	return nil
}

// SettleUnresolvedWithoutReplay retires unresolved terminal deliveries in
// place instead of replacing native scrollback. Normal interaction must never
// replay history, so a range whose bytes cannot be proven (failed write, or an
// invalidated claimed token that may have partially landed) is quarantined:
// it stops counting as an unresolved delivery so ordered handoff can resume,
// while its source identity stays terminal so the same range is never minted
// twice. The trade-off is deliberate and visible: an unproven range is not
// re-emitted, and a partially written range may leave visible rows incomplete.
// Append-only delivery never rebuilds scrollback; the settle quarantines the
// unproven range in place instead.
func (l *HistoryCommitLedger) SettleUnresolvedWithoutReplay() bool {
	if l == nil || l.unresolvedCount == 0 {
		return false
	}
	settled := false
	for token, entry := range l.byToken {
		if !entry.Unresolved() {
			continue
		}
		entry.State = HistoryCommitQuarantined
		entry.Quarantine = HistoryCommitQuarantineSettled
		entry.MayHavePartiallyWritten = false
		l.byToken[token] = entry
		settled = true
	}
	if settled {
		// The counter only ever counted the exact states retired above, so it
		// is safe to clear instead of rescanning the whole ledger.
		l.unresolvedCount = 0
	}
	return settled
}

func (l *HistoryCommitLedger) Entry(token uint64) (HistoryCommitEntry, bool) {
	entry, ok := l.entry(token)
	return entry.Clone(), ok
}

// Entries returns a token-ordered detached ledger view. Ordering is part of
// the terminal-effect contract: a presenter must consume the oldest eligible
// pending commit before newer history ranges.
func (l *HistoryCommitLedger) Entries() []HistoryCommitEntry {
	if l == nil || len(l.byToken) == 0 {
		return nil
	}
	tokens := l.orderedTokens()
	entries := make([]HistoryCommitEntry, 0, len(tokens))
	for _, token := range tokens {
		entry := l.byToken[token]
		entries = append(entries, entry.Clone())
	}
	return entries
}

// HasPending is allocation-free and is intended for scheduler decisions. The
// detached Entries view is deliberately reserved for diagnostics and callers
// that actually need payload ownership.
//
// The queue-head cursor answers this in O(1): head != 0 iff at least one Queued
// entry exists. Only externally constructed ledgers (tests that write byToken
// directly, bypassing Enqueue) leave the head unset; the ordered-token cache
// mismatch is the same signal orderedTokens() uses to detect them, and only
// then does this pay for a scan.
func (l *HistoryCommitLedger) HasPending() bool {
	if l == nil {
		return false
	}
	if l.queueHeadToken != 0 {
		return true
	}
	if len(l.tokens) != len(l.byToken) {
		return l.nextQueuedToken(0) != 0
	}
	return false
}

// QueuedCount counts Queued entries by scanning the ledger. Diagnostic-only
// (env-gated trace): production emptiness/ordering decisions use the O(1)
// queue-head cursor instead.
func (l *HistoryCommitLedger) QueuedCount() int {
	if l == nil {
		return 0
	}
	count := 0
	for _, entry := range l.byToken {
		if entry.State == HistoryCommitQueued {
			count++
		}
	}
	return count
}

// holdsPlan reports whether the ledger still records any delivery lifecycle.
// Planning memoizes its *inputs*, never the ledger itself, so this is what lets
// a memo hit prove that the plan it claims to have reconciled still exists: a
// plan that produced candidates and was reconciled into a ledger that no longer
// holds any entry is a state the memo alone can never repair.
func (l *HistoryCommitLedger) holdsPlan() bool {
	return l != nil && (len(l.byToken) > 0 || len(l.compactedTerminalSources) > 0)
}

// prunableResolvedEntry 判定一个已终结的条目是否仍被任何读取方消费载荷。
// 保留集合只有一类：failed / invalidated-with-partial 隔离——未决交付，
// settle 之前必须保持可寻址。
//
// 其余终态（Delivered、settled 隔离、invalidated 且未部分写入）的载荷
// 无人再读，只剩"该来源已交付"这一身份事实，可压缩为 tombstone。
func prunableResolvedEntry(entry HistoryCommitEntry) bool {
	switch entry.State {
	case HistoryCommitDelivered:
		return true
	case HistoryCommitQuarantined:
		return entry.Quarantine == HistoryCommitQuarantineSettled ||
			(entry.Quarantine == HistoryCommitQuarantineInvalidated && !entry.MayHavePartiallyWritten)
	default:
		return false
	}
}

// historyLedgerCompactHighWater/Target 是 P2-1 终态压缩的窗口门限。ledger 的
// live 库存超过高水位时，从最老 token 起把"已终结且载荷无人消费"的条目回收，
// 直到回落到目标水位。窗口化而非即时剪除：近期已确认条目对排障与 executor 断言
// 仍有价值（ack 帧、状态转换），而库存增长被硬性有界。测试可临时调小这两个值。
var (
	historyLedgerCompactHighWater = 4096
	historyLedgerCompactTarget    = 2048
)

// compactResolvedIfLarge 把 live 库存从高水位压回目标水位，返回回收条目数。
// 只有超过高水位才扫描，且每次最多扫一遍；被保留的非可剪除条目（未决交付、
// Acked+Active 前缀证明）不参与回收，因此库存上界 = 高水位 + 保留集合。
func (l *HistoryCommitLedger) compactResolvedIfLarge() int {
	if l == nil || len(l.byToken) <= historyLedgerCompactHighWater {
		return 0
	}
	// 快照一份有序 token：pruneEntry 会原地收缩 l.tokens，直接在内部切片上
	// range 会跳过/重复元素。
	tokens := append([]uint64(nil), l.orderedTokens()...)
	pruned := 0
	for _, token := range tokens {
		if len(l.byToken) <= historyLedgerCompactTarget {
			break
		}
		entry, ok := l.byToken[token]
		if !ok || !prunableResolvedEntry(entry) {
			continue
		}
		l.pruneEntry(token, entry)
		pruned++
	}
	return pruned
}

// pruneResolvedToken 在终态转换后回收单条目的全部索引与载荷（P2-1 ledger 终态
// 压缩）。返回是否发生剪除。调用方必须在条目不再被本回合后续读取之前调用（ack
// 之后 reducer 仍会用 Entry 取 Active-origin 的提交，而 Active 条目永不被剪除）。
func (l *HistoryCommitLedger) pruneResolvedToken(token uint64) bool {
	if l == nil {
		return false
	}
	entry, ok := l.byToken[token]
	if !ok || !prunableResolvedEntry(entry) {
		return false
	}
	l.pruneEntry(token, entry)
	return true
}

// pruneEntry 从全部索引里移除一个条目，并把其来源身份（阻断态时）转存为压缩
// tombstone。delete 与 range 组合是安全的：调用点按 token 逐个剪除，不依赖遍历
// 期间的一致快照。
func (l *HistoryCommitLedger) pruneEntry(token uint64, entry HistoryCommitEntry) {
	delete(l.byToken, token)
	key := historyCommitKey(entry.Commit)
	// 同一 range key 可能已指向更新 token（Invalidated 非部分写入允许同区间重入队），
	// 只有仍指向本 token 时才删除，避免误删在册条目的登记。
	if l.byRange[key] == token {
		delete(l.byRange, key)
	}
	l.tokens = removeSortedToken(l.tokens, token)
	sourceKey := historyCommitSourceIdentity(entry.Commit)
	if tokens := l.bySource[sourceKey]; tokens != nil {
		delete(tokens, token)
		if len(tokens) == 0 {
			delete(l.bySource, sourceKey)
		}
	}
	// Delivered/Settled 等阻断态：身份必须留下。Invalidated 非部分写入不阻断
	//（既有语义），因此不留 tombstone。
	if entry.BlocksRemint() {
		if l.compactedTerminalSources == nil {
			l.compactedTerminalSources = make(map[historyCommitSourceKey]struct{})
		}
		l.compactedTerminalSources[sourceKey] = struct{}{}
	}
	l.compactedEntries++
}

func (l *HistoryCommitLedger) hasTerminalRecordForSource(key historyCommitSourceKey) bool {
	if l == nil {
		return false
	}
	if _, blocked := l.compactedTerminalSources[key]; blocked {
		return true
	}
	for token := range l.bySource[key] {
		entry, ok := l.byToken[token]
		if !ok {
			continue
		}
		if entry.BlocksRemint() {
			return true
		}
	}
	return false
}

func (l *HistoryCommitLedger) hasUnresolvedTerminalDelivery() bool {
	// Counter-backed O(1) predicate. The counter is monotonic: entries reach
	// failed or invalidated-with-partial-write and never revert to a resolved
	// state except through the explicit SettleUnresolvedWithoutReplay pass.
	return l != nil && l.unresolvedCount > 0
}

func (l *HistoryCommitLedger) hasOlderQueuedToken(token uint64) bool {
	// O(1) equivalent of the previous full-map scan: an earlier Queued token
	// exists exactly when the queue head is still older than token. See the
	// queueHeadToken field comment.
	return l != nil && l.queueHeadToken != 0 && l.queueHeadToken < token
}

func (l *HistoryCommitLedger) orderedTokens() []uint64 {
	if l == nil || len(l.byToken) == 0 {
		return nil
	}
	if len(l.tokens) == len(l.byToken) {
		// Fast path: every key went through Enqueue, so the cached ascending
		// slice is authoritative. Return the internal slice as a read-only
		// view: callers must only iterate it within the current actor turn
		// and must never mutate or retain it across ledger mutations
		// (Enqueue may shift elements in place). Production pprof showed the
		// previous per-call copy as a top allocation source on resumed
		// sessions where every Pending/Entries call copied the full history.
		return l.tokens
	}
	// Defensive fallback: an externally constructed ledger (tests, hand-rolled
	// state) bypassed Enqueue. Rebuild the ordering from the map so behavior
	// stays identical even though the cache never observed those keys.
	tokens := make([]uint64, 0, len(l.byToken))
	for token := range l.byToken {
		tokens = append(tokens, token)
	}
	sort.Slice(tokens, func(i, j int) bool { return tokens[i] < tokens[j] })
	return tokens
}

// Clone returns an independent ledger. AppState snapshots must never retain
// actor-owned maps or render-line slices.
func (l *HistoryCommitLedger) Clone() *HistoryCommitLedger {
	clone := NewHistoryCommitLedger()
	if l == nil {
		return clone
	}
	for token, entry := range l.byToken {
		clone.byToken[token] = entry.Clone()
	}
	for key, token := range l.byRange {
		clone.byRange[key] = token
	}
	for key, tokens := range l.bySource {
		cloneTokens := make(map[uint64]struct{}, len(tokens))
		for token := range tokens {
			cloneTokens[token] = struct{}{}
		}
		clone.bySource[key] = cloneTokens
	}
	if len(l.compactedTerminalSources) > 0 {
		clone.compactedTerminalSources = make(map[historyCommitSourceKey]struct{}, len(l.compactedTerminalSources))
		for key := range l.compactedTerminalSources {
			clone.compactedTerminalSources[key] = struct{}{}
		}
	}
	clone.compactedEntries = l.compactedEntries
	clone.queueHeadToken = l.queueHeadToken
	clone.unresolvedCount = l.unresolvedCount
	clone.tokens = append([]uint64(nil), l.tokens...)
	return clone
}

func (l *HistoryCommitLedger) entry(token uint64) (HistoryCommitEntry, bool) {
	if l == nil || l.byToken == nil {
		return HistoryCommitEntry{}, false
	}
	entry, ok := l.byToken[token]
	return entry, ok
}

// insertSortedToken inserts token into the ascending slice s and returns it.
// s must already be sorted ascending; token is assumed absent (Enqueue is the
// only insertion point and terminal compaction removes the mirror entry
// together with its byToken key, so duplicates cannot occur).
func insertSortedToken(s []uint64, token uint64) []uint64 {
	at := sort.Search(len(s), func(i int) bool { return s[i] >= token })
	s = append(s, 0)
	copy(s[at+1:], s[at:])
	s[at] = token
	return s
}

// removeSortedToken removes token from the ascending slice s and returns it.
// The slice mirrors byToken keys; terminal compaction is its only removal path.
func removeSortedToken(s []uint64, token uint64) []uint64 {
	at := sort.Search(len(s), func(i int) bool { return s[i] >= token })
	if at >= len(s) || s[at] != token {
		return s
	}
	copy(s[at:], s[at+1:])
	return s[:len(s)-1]
}
