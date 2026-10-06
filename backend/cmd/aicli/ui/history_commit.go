package ui

import (
	"errors"
	"fmt"
	"sort"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

var (
	ErrInvalidHistoryCommit    = errors.New("invalid history commit")
	ErrDuplicateCommitToken    = errors.New("duplicate history commit token")
	ErrDuplicateCommitRange    = errors.New("duplicate history commit range")
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

// HistoryCommitOrigin distinguishes immutable transcript history from a
// stable prefix handed off while its semantic cell is still mutable. Active
// effects deliberately survive append-only source revisions; transcript
// effects retain the exact finalized revision as part of their identity.
type HistoryCommitOrigin uint8

const (
	HistoryCommitTranscript HistoryCommitOrigin = iota
	HistoryCommitActive
)

func (r DisplayRange) Valid() bool {
	return r.Start >= 0 && r.End >= r.Start
}

// HistoryCommit is the typed terminal effect for moving finalized display rows
// into native scrollback. Token, cell identity, revision, both ranges, and
// layout generation are all required; text is payload, never identity.
type HistoryCommit struct {
	Token       uint64
	Origin      HistoryCommitOrigin
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
	return c.Token != 0 && c.Origin <= HistoryCommitActive &&
		c.CellID != 0 && c.LayoutGeneration != 0 &&
		c.SourceRange.Valid() && c.SourceRange.End > c.SourceRange.Start &&
		c.DisplayRange.Valid() && c.DisplayRange.End > c.DisplayRange.Start
}

func (c HistoryCommit) Clone() HistoryCommit {
	c.Lines = cloneRenderLines(c.Lines)
	return c
}

// HistoryCommitState records terminal-effect progress separately from source
// and physical projection state.
type HistoryCommitState uint8

const (
	HistoryCommitPending HistoryCommitState = iota
	// HistoryCommitInFlight is retained only so externally built states and
	// diagnostics can still name the legacy value. Since the P1-1 write-cursor
	// convergence no production transition enters this state: a claimed entry
	// stays Pending and HistoryEffectQueueState.WriteCursor records that it may
	// be physically written. Step 2 (六态归一) removes the constant.
	HistoryCommitInFlight
	HistoryCommitAcked
	HistoryCommitStateFailed
	HistoryCommitInvalidated
	// HistoryCommitAbandoned is the terminal state for a delivery whose
	// physical outcome is unknown (failed or partially written) and which the
	// no-replay policy refuses to repair by replacing native scrollback. The
	// entry keeps its source identity so the range is never minted a second
	// time, but it no longer blocks ordered delivery.
	HistoryCommitAbandoned
)

func (s HistoryCommitState) String() string {
	switch s {
	case HistoryCommitPending:
		return "pending"
	case HistoryCommitInFlight:
		return "in_flight"
	case HistoryCommitAcked:
		return "acked"
	case HistoryCommitStateFailed:
		return "failed"
	case HistoryCommitInvalidated:
		return "invalidated"
	case HistoryCommitAbandoned:
		return "abandoned"
	default:
		return "unknown"
	}
}

// HistoryCommitEntry is an immutable snapshot of one ledger item.
type HistoryCommitEntry struct {
	Commit                  HistoryCommit
	State                   HistoryCommitState
	AckFrame                uint64
	MayHavePartiallyWritten bool
	Failure                 error
}

func (e HistoryCommitEntry) Clone() HistoryCommitEntry {
	e.Commit = e.Commit.Clone()
	return e
}

type historyCommitRangeKey struct {
	origin           HistoryCommitOrigin
	cellID           scene.CellID
	revision         uint64
	sourceStart      int
	sourceEnd        int
	fragmentID       uint64
	displayStart     int
	displayEnd       int
	layoutGeneration uint64
}

type historyCommitSourceKey struct {
	origin      HistoryCommitOrigin
	cellID      scene.CellID
	revision    uint64
	sourceStart int
	sourceEnd   int
	fragmentID  uint64
}

func historyCommitSourceIdentity(commit HistoryCommit) historyCommitSourceKey {
	revision := commit.Revision
	if commit.Origin == HistoryCommitActive {
		// Mutable source revisions are delivery fences, not new semantic cells.
		// An append-only update must not mint the same stable range again.
		revision = 0
	}
	return historyCommitSourceKey{
		origin:      commit.Origin,
		cellID:      commit.CellID,
		revision:    revision,
		sourceStart: commit.SourceRange.Start,
		sourceEnd:   commit.SourceRange.End,
		fragmentID:  commit.FragmentID,
	}
}

func historyCommitKey(c HistoryCommit) historyCommitRangeKey {
	return historyCommitRangeKey{
		origin:           c.Origin,
		cellID:           c.CellID,
		revision:         c.Revision,
		sourceStart:      c.SourceRange.Start,
		sourceEnd:        c.SourceRange.End,
		fragmentID:       c.FragmentID,
		displayStart:     c.DisplayRange.Start,
		displayEnd:       c.DisplayRange.End,
		layoutGeneration: c.LayoutGeneration,
	}
}

// HistoryCommitLedger is reducer-owned effect progress. It deliberately has
// no terminal I/O and does not infer identity from line text or hashes.
type HistoryCommitLedger struct {
	byToken            map[uint64]HistoryCommitEntry
	byRange            map[historyCommitRangeKey]uint64
	bySource           map[historyCommitSourceKey]map[uint64]struct{}
	activeTokensByCell map[scene.CellID][]uint64
	pendingCount       int
	// minNonTerminalToken caches the smallest token whose state is still
	// Pending or InFlight. It backs hasOlderPendingOrInFlight as an O(1)
	// predicate; production pprof showed the previous full-map scan inside
	// the per-claim ordering guard as a hot spot on resumed sessions with a
	// very large ledger. Terminal states are absorbing (no path re-arms
	// Acked/Failed/Invalidated), so the minimum only ever moves forward and
	// the recompute-after-terminal scan is amortized O(1) per transition.
	minNonTerminalToken uint64
	// activeAckPlanVersion bumps whenever the acked Active-origin prefix that
	// planEligibleHistoryCommits reads via activeAckedRenderedPrefixRows can
	// change. Ack is the only transition that grows that set (terminal states
	// are absorbing and Invalidate/Fail/Defer never touch Acked entries), and
	// reconcileScrollback replaces the whole ledger behind a new TerminalEpoch.
	// The transcript-plan memo uses this counter instead of scanning
	// activeTokensByCell on every stream chunk.
	activeAckPlanVersion uint64
	// unresolvedCount caches the number of entries that require terminal
	// recovery (Failed, or Invalidated with MayHavePartiallyWritten). It keeps
	// hasUnresolvedTerminalDelivery O(1); production pprof showed the previous
	// full-map scan inside the per-action wake predicate as a hot spot.
	unresolvedCount int
	// compactedTerminalSources 是被终态压缩剪除、但来源身份仍必须阻断再次铸造的
	// source key 最小集（Acked/Abandoned 等阻断态）。P2-1：Acked transcript 条目
	// 与 settle 后的 Abandoned 条目不再被任何读取方消费（行载荷已置 nil 或无用），
	// 但 "该来源已交付" 必须永远阻断重复铸造；reconcileScrollback 整体替换 ledger
	// 时随之清零 —— 新 terminal epoch 允许从源重新铸造。
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
		activeTokensByCell:       make(map[scene.CellID][]uint64),
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
	if l.activeTokensByCell == nil {
		l.activeTokensByCell = make(map[scene.CellID][]uint64)
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
		if previous.State != HistoryCommitInvalidated || previous.MayHavePartiallyWritten {
			return ErrDuplicateCommitRange
		}
	}
	sourceKey := historyCommitSourceIdentity(commit)
	// 已压缩的阻断身份仍然生效：调用方绕过 enqueueHistoryCandidates 的直接入队
	// （测试、手写状态）同样不得二次铸造同一来源。
	if _, blocked := l.compactedTerminalSources[sourceKey]; blocked {
		return ErrDuplicateCommitRange
	}
	l.byToken[commit.Token] = HistoryCommitEntry{Commit: commit.Clone(), State: HistoryCommitPending}
	l.byRange[key] = commit.Token
	l.tokens = insertSortedToken(l.tokens, commit.Token)
	if l.bySource[sourceKey] == nil {
		l.bySource[sourceKey] = make(map[uint64]struct{})
	}
	l.bySource[sourceKey][commit.Token] = struct{}{}
	if commit.Origin == HistoryCommitActive {
		tokens := l.activeTokensByCell[commit.CellID]
		at := sort.Search(len(tokens), func(index int) bool { return tokens[index] >= commit.Token })
		tokens = append(tokens, 0)
		copy(tokens[at+1:], tokens[at:])
		tokens[at] = commit.Token
		l.activeTokensByCell[commit.CellID] = tokens
	}
	l.pendingCount++
	if l.minNonTerminalToken == 0 || commit.Token < l.minNonTerminalToken {
		l.minNonTerminalToken = commit.Token
	}
	return nil
}

// RebasePending updates only the display payload of an unstarted effect after
// a layout generation change. Token and semantic source identity are retained;
// resize therefore never creates a second history handoff token.
func (l *HistoryCommitLedger) RebasePending(token uint64, replacement HistoryCommit) error {
	entry, ok := l.entry(token)
	if !ok || entry.State != HistoryCommitPending {
		return ErrCommitNotPending
	}
	current := entry.Commit
	if current.Origin != replacement.Origin || current.CellID != replacement.CellID ||
		(current.Origin != HistoryCommitActive && current.Revision != replacement.Revision) ||
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

// Invalidate prevents a pending or in-flight effect from being consumed after
// transcript replacement. An in-flight invalidation means terminal bytes may
// already have reached the old projection and therefore requires recovery.
//
// mayHavePartiallyWritten is supplied by the caller: the queue knows whether
// the token currently holds the write cursor, i.e. whether a physical write
// may already have started (the previous InFlight state). A partially written
// cancellation counts as an unresolved terminal delivery; the counter is
// monotonic (unresolved entries never revert to resolved).
func (l *HistoryCommitLedger) Invalidate(token uint64, mayHavePartiallyWritten bool) error {
	entry, ok := l.entry(token)
	if !ok || entry.State != HistoryCommitPending {
		return ErrCommitNotPending
	}
	entry.State = HistoryCommitInvalidated
	if mayHavePartiallyWritten {
		l.unresolvedCount++
		entry.MayHavePartiallyWritten = true
	}
	l.pendingCount--
	l.byToken[token] = entry
	l.advanceMinAfterTerminal(token)
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
	if entry.State == HistoryCommitAcked {
		return ErrDuplicateCommitAck
	}
	if entry.State != HistoryCommitPending {
		return ErrCommitNotInFlight
	}
	if entry.Commit.LayoutGeneration != currentGeneration {
		return ErrStaleLayoutGeneration
	}
	entry.State = HistoryCommitAcked
	entry.AckFrame = frame
	entry.Failure = nil
	entry.MayHavePartiallyWritten = false
	// Finalized transcript source can always rebuild its payload. A mutable
	// handoff retains its small delivered fragment until finalization so the
	// finalized planner can prove and skip that already-physical rich prefix.
	if entry.Commit.Origin == HistoryCommitTranscript {
		entry.Commit.Lines = nil
	}
	l.byToken[token] = entry
	l.pendingCount--
	if entry.Commit.Origin == HistoryCommitActive {
		l.activeAckPlanVersion++
	}
	l.advanceMinAfterTerminal(token)
	return nil
}

// nextNonTerminalToken returns the smallest live token greater than from that
// is still Pending or InFlight, or 0 when none remains. It walks the cached
// ascending token slice; the walk is bounded by the number of consecutive
// terminal tokens after from, so the total cost is amortized O(1) per
// terminal transition across the ledger's lifetime.
func (l *HistoryCommitLedger) nextNonTerminalToken(from uint64) uint64 {
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
			(entry.State == HistoryCommitPending || entry.State == HistoryCommitInFlight) {
			return tokens[index]
		}
	}
	return 0
}

func (l *HistoryCommitLedger) nextNonTerminalTokenByScan(from uint64) uint64 {
	for _, token := range l.orderedTokens() {
		if token <= from {
			continue
		}
		if entry, ok := l.byToken[token]; ok &&
			(entry.State == HistoryCommitPending || entry.State == HistoryCommitInFlight) {
			return token
		}
	}
	return 0
}

// advanceMinAfterTerminal refreshes the cached minimum non-terminal token
// after a transition into a terminal state (Acked, Failed, or Invalidated).
// Terminal states are absorbing, so the minimum only ever moves forward and
// the bounded recompute scan stays amortized O(1) per terminal transition.
// Skipping this refresh pins the cache on a terminal token and makes
// hasOlderPendingOrInFlight report a phantom older effect forever, which
// deadlocks every later claim behind out-of-order rejection.
func (l *HistoryCommitLedger) advanceMinAfterTerminal(token uint64) {
	if l.minNonTerminalToken == token {
		l.minNonTerminalToken = l.nextNonTerminalToken(token)
	}
}

func (l *HistoryCommitLedger) Fail(token uint64, err error, mayHavePartiallyWritten bool) error {
	entry, ok := l.entry(token)
	if !ok || entry.State != HistoryCommitPending {
		return ErrCommitNotInFlight
	}
	entry.State = HistoryCommitStateFailed
	entry.Failure = err
	entry.MayHavePartiallyWritten = mayHavePartiallyWritten
	// HistoryCommitStateFailed is always unresolved regardless of
	// MayHavePartiallyWritten.
	l.unresolvedCount++
	l.pendingCount--
	l.byToken[token] = entry
	l.advanceMinAfterTerminal(token)
	return nil
}

// SettleUnresolvedWithoutReplay retires unresolved terminal deliveries in
// place instead of replacing native scrollback. Normal interaction must never
// replay history, so a range whose bytes cannot be proven (failed write, or an
// invalidated in-flight token that may have partially landed) is quarantined:
// it stops counting as an unresolved delivery so ordered handoff can resume,
// while its source identity stays terminal so the same range is never minted
// twice. The trade-off is deliberate and visible: an unproven range is not
// re-emitted, and a partially written range may leave visible rows incomplete.
// Only an explicit resume/load replay authorization may rebuild scrollback
// instead.
func (l *HistoryCommitLedger) SettleUnresolvedWithoutReplay() bool {
	if l == nil || l.unresolvedCount == 0 {
		return false
	}
	settled := false
	for token, entry := range l.byToken {
		unresolved := entry.State == HistoryCommitStateFailed ||
			(entry.State == HistoryCommitInvalidated && entry.MayHavePartiallyWritten)
		if !unresolved {
			continue
		}
		entry.State = HistoryCommitAbandoned
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
func (l *HistoryCommitLedger) HasPending() bool {
	return l != nil && l.pendingCount > 0
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
// 保留集合只有两类：
//   - Acked + Active-origin：activeAckedRenderedPrefixRows 需要已交付的渲染行
//     来证明"前导行已在原生滚动区"并据此计算 finalized 计划的 skipRows；
//   - Failed / Invalidated-with-partial：未决交付，settle 之前必须保持可寻址。
//
// 其余终态（Acked transcript、Abandoned、Invalidated 且未部分写入）的载荷无人
// 再读，只剩"该来源已交付"这一身份事实，可压缩为 tombstone。
func prunableResolvedEntry(entry HistoryCommitEntry) bool {
	switch entry.State {
	case HistoryCommitAcked:
		return entry.Commit.Origin == HistoryCommitTranscript
	case HistoryCommitAbandoned:
		return true
	case HistoryCommitInvalidated:
		return !entry.MayHavePartiallyWritten
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
	// Acked/Abandoned 是"阻断再铸造"的终态：身份必须留下。Invalidated 非部分写入
	// 不阻断（既有语义），因此不留 tombstone。
	if entry.State != HistoryCommitInvalidated {
		if l.compactedTerminalSources == nil {
			l.compactedTerminalSources = make(map[historyCommitSourceKey]struct{})
		}
		l.compactedTerminalSources[sourceKey] = struct{}{}
	}
	if entry.Commit.Origin == HistoryCommitActive {
		tokens := l.activeTokensByCell[entry.Commit.CellID]
		if at := sort.Search(len(tokens), func(i int) bool { return tokens[i] >= token }); at < len(tokens) && tokens[at] == token {
			tokens = append(tokens[:at], tokens[at+1:]...)
			if len(tokens) == 0 {
				delete(l.activeTokensByCell, entry.Commit.CellID)
			} else {
				l.activeTokensByCell[entry.Commit.CellID] = tokens
			}
		}
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
		switch entry.State {
		case HistoryCommitPending, HistoryCommitInFlight, HistoryCommitAcked,
			HistoryCommitStateFailed, HistoryCommitAbandoned:
			return true
		case HistoryCommitInvalidated:
			if entry.MayHavePartiallyWritten {
				return true
			}
		}
	}
	return false
}

func (l *HistoryCommitLedger) hasUnresolvedTerminalDelivery() bool {
	// Counter-backed O(1) predicate. The counter is monotonic: entries reach
	// Failed or Invalidated-with-partial-write and never revert to a resolved
	// state (no path re-arms DeferInFlight/Ack/Invalidate from those states).
	return l != nil && l.unresolvedCount > 0
}

func (l *HistoryCommitLedger) hasOlderPendingOrInFlight(token uint64) bool {
	// O(1) equivalent of the previous full-map scan: an earlier Pending or
	// InFlight token exists exactly when the smallest non-terminal token is
	// still older than token. See the minNonTerminalToken field comment.
	return l != nil && l.minNonTerminalToken != 0 && l.minNonTerminalToken < token
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
	for cellID, tokens := range l.activeTokensByCell {
		clone.activeTokensByCell[cellID] = append([]uint64(nil), tokens...)
	}
	if len(l.compactedTerminalSources) > 0 {
		clone.compactedTerminalSources = make(map[historyCommitSourceKey]struct{}, len(l.compactedTerminalSources))
		for key := range l.compactedTerminalSources {
			clone.compactedTerminalSources[key] = struct{}{}
		}
	}
	clone.compactedEntries = l.compactedEntries
	clone.pendingCount = l.pendingCount
	clone.minNonTerminalToken = l.minNonTerminalToken
	clone.activeAckPlanVersion = l.activeAckPlanVersion
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
