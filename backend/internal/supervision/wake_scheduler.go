package supervision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Wake-related sentinel errors (doc 6.5).
var (
	// ErrWakeRateLimited is returned when the auto-wake budget for the wake's
	// budget class is exhausted. The wake row stays durable and the
	// notification is never dropped; the caller should escalate to the
	// operator and let the next natural parent turn deliver the digest via
	// preflight. Approval-class wakes have their own budget (unlimited by
	// default), so a burst of child failures cannot rate-limit a blocking
	// approval.
	ErrWakeRateLimited = errors.New("supervision: auto wake rate limit reached; notification kept durable")
	// ErrWakeParentBusy is returned when the parent session is not runnable;
	// the durable wake row is kept for the next runnable point.
	ErrWakeParentBusy = errors.New("supervision: parent not runnable; wake kept pending")
)

const (
	// defaultMaxAutoWakePerWindow is the bounded-class default (doc 6.5 rule 4).
	defaultMaxAutoWakePerWindow = 5
	// defaultMaxProgressWakePerWindow is the independent progress-class default
	// (P0-2/ADR-2): bounded like the other non-approval classes, but accounted
	// separately so progress reports cannot starve critical lifecycle wakes.
	defaultMaxProgressWakePerWindow = 6
	// unlimitedWakeBudget marks a budget class without a hard cap.
	unlimitedWakeBudget = 0
)

// WakeBudgetMode selects where auto-wake budget claims are counted (P1-6 方案 2).
type WakeBudgetMode string

// Notification families behind the idempotency key (plan C2-4 #15 / B4,
// doc 6.6). The kind is part of notify_key, so the same obligation can be
// reported once per family without one family suppressing another.
const (
	// WakeEventTerminal marks a terminal obligation transition. Terminal
	// notifications dedup by terminal_epoch (WakeRequest.EventSeq) and are
	// delivered exactly once per epoch.
	WakeEventTerminal = "terminal"
	// WakeEventProgress marks a progress rollup. Progress notifications dedup
	// by progress_seq (WakeRequest.EventSeq): the same seq never wakes twice.
	WakeEventProgress = "progress"
	// WakeEventApproval marks an approval request that gates a child; it is
	// not bounded by the progress budget (doc 6.14 / EC-A4).
	WakeEventApproval = "approval"
	// WakeEventLifecycle is the legacy lifecycle family (coalesced by dedup
	// key). Wakes that carry no structured identity keep this classification
	// implicitly by leaving EventKind empty.
	WakeEventLifecycle = "lifecycle"
)

// DeriveNotifyKey computes the stable notification idempotency key
// notify_key = hash(turn_id, obligation_id, event_kind, terminal_epoch|
// progress_seq) (doc 6.6 / plan C2-4). It is a pure function of the event
// identity, so a retried delivery (requeueSupervisionWake) reuses the same key
// and the consumer can dedup by it. An event with neither a turn nor an
// obligation id yields an empty key: those wakes keep the legacy
// coalescing-only behavior.
func DeriveNotifyKey(turnID, obligationID, eventKind string, eventSeq int64) string {
	turnID = strings.TrimSpace(turnID)
	obligationID = strings.TrimSpace(obligationID)
	eventKind = strings.TrimSpace(eventKind)
	if turnID == "" && obligationID == "" {
		return ""
	}
	if eventKind == "" {
		eventKind = WakeEventLifecycle
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		turnID, obligationID, eventKind, strconv.FormatInt(eventSeq, 10),
	}, "|")))
	return "nk-" + hex.EncodeToString(sum[:12])
}

const (
	// WakeBudgetModeMemory keeps the rolling window in process memory: the
	// historical behavior where every process gets its own budget and a
	// restart clears the counters.
	WakeBudgetModeMemory WakeBudgetMode = "memory"
	// WakeBudgetModeDurable records every delivered auto-wake as a claim row
	// in the supervision store, so processes sharing a database share the
	// budget and a restart does not reset it.
	WakeBudgetModeDurable WakeBudgetMode = "durable"
)

// WakeRequest describes one critical lifecycle event that should wake the
// parent session / team lead (doc 6.5).
type WakeRequest struct {
	RootScopeID           string
	TargetParentSessionID string
	TargetParentTeamID    string
	WakeReason            string
	NotificationSeq       int64
	// TurnID optionally names the parked turn this wake resumes (plan C2-1).
	// Empty falls back to the scheduler's TurnHint resolver; when both are
	// empty the wake keeps the legacy "new turn" delivery semantics.
	TurnID string
	// ObligationID / EventKind / EventSeq describe the ledger event behind the
	// wake and feed the stable notify key (plan C2-4 #15 / B4). EventSeq is
	// the terminal_epoch for terminal notifications and the progress_seq for
	// progress notifications (doc 6.6); a retry/reassignment of the same
	// obligation must increment it so a *new* terminal state is delivered
	// while a replay of the old one stays deduplicated.
	ObligationID string
	EventKind    string
	EventSeq     int64
	// NotifyKey optionally overrides the derived idempotency key. Hosts that
	// already published a key (for example across a retry) pass it verbatim so
	// the duplicate is recognised.
	NotifyKey string
}

// WakeResult reports how ScheduleWake coalesced the event.
type WakeResult struct {
	WakeID   string `json:"wake_id,omitempty"`
	DedupKey string `json:"dedup_key,omitempty"`
	Seq      int64  `json:"seq,omitempty"`
	// NotifyKey is the stable idempotency key of the scheduled event
	// (plan C2-4 #15). Empty for legacy wakes without a structured identity.
	NotifyKey string `json:"notify_key,omitempty"`
	// Suppressed is true when the same notify key was already delivered: the
	// event must not start a second resume (AC-P1-4a). Callers treat it as
	// success; nothing was persisted.
	Suppressed bool `json:"suppressed,omitempty"`
	// Coalesced is true when an unclaimed wake for the same root+parent+
	// reason already existed (multiple child exceptions => one parent turn,
	// doc 6.5 rule 1).
	Coalesced bool `json:"coalesced,omitempty"`
}

// WakeSchedulerConfig tunes wake debounce and rate limiting (doc 6.5 rules
// 1 and 4, plan P1-6).
type WakeSchedulerConfig struct {
	// DebounceWindow is reserved for future time-based coalescing; the
	// current implementation coalesces via the durable dedup key until the
	// wake is claimed and resolved.
	DebounceWindow time.Duration
	// RateWindow is the rolling window for auto-turn budgeting.
	RateWindow time.Duration
	// MaxAutoWakePerWindow caps auto-scheduled parent turns per root scope
	// for the bounded classes (failure / other). 0 uses the default (5 per
	// hour); a negative value removes the cap for those classes.
	MaxAutoWakePerWindow int
	// MaxApprovalWakePerWindow caps the approval class in the same rolling
	// window. 0 (the default) or a negative value means unlimited: delaying
	// an approval stalls the child that is waiting for the decision. Set a
	// positive value for an explicit cap.
	MaxApprovalWakePerWindow int
	// MaxProgressWakePerWindow caps the progress class (the opt-in periodic
	// progress check) in the same rolling window, independently of the
	// failure/other budget. 0 uses the default (6 per window); a negative
	// value removes the cap (not recommended: progress is the floodable
	// family).
	MaxProgressWakePerWindow int
	// BudgetMode selects the budget ledger (see WakeBudgetMode). The empty
	// value means WakeBudgetModeMemory.
	BudgetMode WakeBudgetMode
	// SelfCheckPerWindow is the turn-end self-check allowance (plan P1-6
	// 方案 4): how many digest-only parent turns a root scope may start per
	// rate window when its wake was deferred by an exhausted class budget.
	// 0 (the default) disables the self-check.
	SelfCheckPerWindow int
	// HostCapabilities optionally reports which action channels this process
	// can actually execute, so the wake digest (which becomes a parent turn's
	// prompt) only announces reachable actions (P2-12 方案 1). It is a function
	// because executors may be wired after the scheduler is constructed; nil
	// means undeclared and keeps the host-neutral action set.
	HostCapabilities func() *HostCapabilities
	// Progress optionally adds the P0-B progress rollup to every drained wake
	// digest. The opt-in progress check (P2-D) carries no lifecycle
	// notification, so this rollup is the only content its wake can deliver;
	// nil keeps the wake digest byte-identical to the pre-P0-B output.
	Progress ProgressSource
	// TurnHint optionally resolves the parked turn id of a parent session when
	// a wake is scheduled, so the wake can resume that same turn (plan C2-1,
	// I3). nil keeps the legacy "new turn" wake.
	TurnHint TurnHintFunc
	// Obligations optionally supplies the ledger projection used to assemble
	// the same-turn resume context (plan C2-1 终局综合 / AC-P1-1d). nil keeps
	// the legacy wake prompt. Hosts normally wire it later via
	// SetObligationSource, once their durable batch control plane exists.
	Obligations ObligationSource
	// ResumeBudget bounds that projection. Zero values fall back to
	// DefaultConfig() (DigestMaxItems=20 / DigestMaxChars=4000).
	ResumeBudget ResumeBudget
}

// WakeScheduler subscribes the lifecycle inbox to the parent turn start
// path (doc 6.5 "wake 调度入口": no resident LLM polling goroutine; wake only
// delivers a pending digest into the existing turn start path).
type WakeScheduler struct {
	store Store

	rateMu sync.Mutex
	claims map[string][]time.Time // rootScopeID|budgetClass -> claim timestamps
	// selfChecks maps rootScopeID -> turn-end self-check timestamps.
	selfChecks map[string][]time.Time

	rateWindow      time.Duration
	maxAutoWake     int // 0 => unlimited (failure / other classes)
	maxApprovalWake int // 0 => unlimited (approval class)
	maxProgressWake int // 0 => unlimited (progress class)
	maxSelfCheck    int // 0 => self-check disabled
	budgetMode      WakeBudgetMode
	now             func() time.Time
	// hostCapabilities resolves the announced-action capabilities lazily, so a
	// control plane that wires its executor after construction still reports
	// the truth (see WakeSchedulerConfig.HostCapabilities).
	hostCapabilities func() *HostCapabilities
	// progressMu guards the lazily wired P0-B progress projection. Hosts
	// attach their batch control plane after the scheduler is constructed
	// (see SetProgressSource), and drains may already run concurrently.
	progressMu sync.Mutex
	progress   ProgressSource
	// obligationsMu guards the lazily wired resume projection (plan C2-1).
	obligationsMu sync.Mutex
	obligations   ObligationSource
	// turnHint resolves the parked turn id at scheduling time (see
	// WakeSchedulerConfig.TurnHint).
	turnHint TurnHintFunc
	// resumeBudget bounds the assembled resume context (AC-P1-1d).
	resumeBudget ResumeBudget
}

// NewWakeScheduler creates a wake scheduler over a durable store.
func NewWakeScheduler(store Store, config WakeSchedulerConfig) *WakeScheduler {
	if store == nil {
		panic("supervision: wake scheduler requires a store")
	}
	rateWindow := config.RateWindow
	if rateWindow <= 0 {
		rateWindow = time.Hour
	}
	maxAutoWake := config.MaxAutoWakePerWindow
	switch {
	case maxAutoWake < 0:
		maxAutoWake = unlimitedWakeBudget
	case maxAutoWake == 0:
		maxAutoWake = defaultMaxAutoWakePerWindow
	}
	maxApprovalWake := config.MaxApprovalWakePerWindow
	if maxApprovalWake < 0 {
		maxApprovalWake = unlimitedWakeBudget
	}
	maxProgressWake := config.MaxProgressWakePerWindow
	switch {
	case maxProgressWake < 0:
		maxProgressWake = unlimitedWakeBudget
	case maxProgressWake == 0:
		maxProgressWake = defaultMaxProgressWakePerWindow
	}
	budgetMode := config.BudgetMode
	if budgetMode != WakeBudgetModeDurable {
		budgetMode = WakeBudgetModeMemory
	}
	return &WakeScheduler{
		store:            store,
		claims:           map[string][]time.Time{},
		selfChecks:       map[string][]time.Time{},
		rateWindow:       rateWindow,
		maxAutoWake:      maxAutoWake,
		maxApprovalWake:  maxApprovalWake,
		maxProgressWake:  maxProgressWake,
		maxSelfCheck:     config.SelfCheckPerWindow,
		budgetMode:       budgetMode,
		now:              timeNow,
		hostCapabilities: config.HostCapabilities,
		progress:         config.Progress,
		obligations:      config.Obligations,
		turnHint:         config.TurnHint,
		resumeBudget:     config.ResumeBudget,
	}
}

// SetProgressSource wires (or clears) the P0-B progress projection used when
// the wake digest is built. Hosts call it once their durable batch control
// plane exists, which is normally after the scheduler was constructed.
func (s *WakeScheduler) SetProgressSource(source ProgressSource) {
	if s == nil {
		return
	}
	s.progressMu.Lock()
	s.progress = source
	s.progressMu.Unlock()
}

// progressSource reads the wired projection; nil means unwired, which keeps
// the drained digest free of progress content.
func (s *WakeScheduler) progressSource() ProgressSource {
	if s == nil {
		return nil
	}
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	return s.progress
}

// SetObligationSource wires (or clears) the ledger projection used to assemble
// the same-turn resume context (plan C2-1). Hosts call it once their durable
// batch control plane exists; a nil source keeps the legacy wake prompt.
func (s *WakeScheduler) SetObligationSource(source ObligationSource) {
	if s == nil {
		return
	}
	s.obligationsMu.Lock()
	s.obligations = source
	s.obligationsMu.Unlock()
}

// obligationSource reads the wired ledger projection; nil means unwired, which
// keeps the wake on the legacy "new turn" path.
func (s *WakeScheduler) obligationSource() ObligationSource {
	if s == nil {
		return nil
	}
	s.obligationsMu.Lock()
	defer s.obligationsMu.Unlock()
	return s.obligations
}

// SetTurnHint wires (or clears) the parked-turn resolver used when a wake is
// scheduled (plan C2-1: 通知携带 turn_id).
func (s *WakeScheduler) SetTurnHint(hint TurnHintFunc) {
	if s == nil {
		return
	}
	s.obligationsMu.Lock()
	s.turnHint = hint
	s.obligationsMu.Unlock()
}

// turnHintFor evaluates the parked-turn resolver defensively: a resolver panic
// or empty answer must never break wake scheduling (the wake itself is the
// durable guarantee that the event is not lost).
func (s *WakeScheduler) turnHintFor(ctx context.Context, parentSessionID string) (turnID string) {
	if s == nil {
		return ""
	}
	s.obligationsMu.Lock()
	hint := s.turnHint
	s.obligationsMu.Unlock()
	if hint == nil || strings.TrimSpace(parentSessionID) == "" {
		return ""
	}
	defer func() {
		if recover() != nil {
			turnID = ""
		}
	}()
	return strings.TrimSpace(hint(ctx, parentSessionID))
}

// ResumeBudgetValue reports the effective resume-context budget
// (AC-P1-1d numbers when unset).
func (s *WakeScheduler) ResumeBudgetValue() ResumeBudget {
	if s == nil {
		return ResumeBudget{}.withDefaults()
	}
	return s.resumeBudget.withDefaults()
}

// BuildResumeContext assembles the same-turn resume payload for a claimed
// wake, using the scheduler's wired progress + obligation sources and the
// configured budget (plan C2-1: AutoWakePrompt 改为携带 rollup digest 的 resume
// 上下文). It never returns nil and never fails: a missing projection degrades
// to the legacy prompt instead of dropping the wake.
func (s *WakeScheduler) BuildResumeContext(ctx context.Context, req ResumeContextRequest) *ResumeContext {
	if s == nil {
		return nil
	}
	req.Budget = s.resumeBudget
	if strings.TrimSpace(req.TurnID) == "" {
		req.TurnID = s.turnHintFor(ctx, req.ParentSessionID)
	}
	return BuildResumeContext(ctx, s.obligationSource(), s.progressSource(), req)
}

// hostCapabilitySnapshot evaluates the declared capabilities at digest build
// time. A nil or unset resolver means undeclared, which keeps the host-neutral
// announcement instead of guessing that a channel is missing.
func (s *WakeScheduler) hostCapabilitySnapshot() *HostCapabilities {
	if s == nil || s.hostCapabilities == nil {
		return nil
	}
	return s.hostCapabilities()
}

// ScheduleWake persists a durable, deduplicated wake request. It is safe to
// call for every critical lifecycle event: repeated events for the same root
// scope + parent + reason category collapse into one row (doc 6.5 rules 1/3,
// 6.3 rule 7). When the parent session is known to be busy the caller should
// pass that via ParentRunnable=false to keep the row pending.
func (s *WakeScheduler) ScheduleWake(ctx context.Context, req WakeRequest) (WakeResult, error) {
	if strings.TrimSpace(req.RootScopeID) == "" {
		return WakeResult{}, fmt.Errorf("%w: root_scope_id is required", ErrActionInvalid)
	}
	if strings.TrimSpace(req.WakeReason) == "" {
		req.WakeReason = "critical_lifecycle"
	}
	dedupKey := strings.Join([]string{
		strings.TrimSpace(req.RootScopeID),
		strings.TrimSpace(req.TargetParentSessionID),
		strings.TrimSpace(req.TargetParentTeamID),
		strings.TrimSpace(req.WakeReason),
	}, "|")
	// The parked turn is carried as a hint only (plan C2-1). It is resolved
	// here, at scheduling time, and deliberately kept out of the dedup key so
	// coalescing still collapses repeated events for the same parent + reason
	// into one wake (doc 6.5 rule 1); the delivery path re-derives the
	// authoritative turn id from the obligation ledger (I3 保底).
	turnID := strings.TrimSpace(req.TurnID)
	if turnID == "" {
		turnID = s.turnHintFor(ctx, req.TargetParentSessionID)
	}
	// The notification idempotency key is derived from the event identity
	// (plan C2-4 #15 / B4). A replay of an event that was already delivered
	// must not produce a second resume, so the check happens before the row is
	// persisted; the durable insert repeats the predicate so concurrent
	// schedulers cannot both win (sqlite_store.InsertWakePending).
	notifyKey := strings.TrimSpace(req.NotifyKey)
	if notifyKey == "" {
		notifyKey = DeriveNotifyKey(turnID, req.ObligationID, req.EventKind, req.EventSeq)
	}
	if notifyKey != "" {
		// Fail open on a read error: a store hiccup must not silence the
		// parent. The delivery-time check (WakeConsumer) re-applies the same
		// idempotency rule before a turn is actually started.
		if delivered, err := s.store.IsWakeDelivered(ctx, notifyKey); err == nil && delivered {
			return WakeResult{
				DedupKey:   dedupKey,
				Seq:        req.NotificationSeq,
				NotifyKey:  notifyKey,
				Suppressed: true,
			}, nil
		}
	}
	w := WakePending{
		WakeID:                newWakeID(),
		RootScopeID:           strings.TrimSpace(req.RootScopeID),
		TargetParentSessionID: strings.TrimSpace(req.TargetParentSessionID),
		TargetParentTeamID:    strings.TrimSpace(req.TargetParentTeamID),
		WakeReason:            strings.TrimSpace(req.WakeReason),
		NotificationSeq:       req.NotificationSeq,
		TurnID:                turnID,
		NotifyKey:             notifyKey,
		EventKind:             strings.TrimSpace(req.EventKind),
		ObligationID:          strings.TrimSpace(req.ObligationID),
		EventSeq:              req.EventSeq,
		DedupKey:              dedupKey,
		CreatedAt:             s.now().UTC(),
	}
	if err := s.store.InsertWakePending(ctx, w); err != nil {
		return WakeResult{}, err
	}

	// Detect coalescing by listing the parent's pending wakes.
	result := WakeResult{
		WakeID:    w.WakeID,
		DedupKey:  dedupKey,
		Seq:       req.NotificationSeq,
		NotifyKey: notifyKey,
	}
	pending, err := s.store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           w.RootScopeID,
		TargetParentSessionID: w.TargetParentSessionID,
		TargetParentTeamID:    w.TargetParentTeamID,
		UnclaimedOnly:         true,
	})
	if err != nil {
		return result, err
	}
	for _, p := range pending {
		if p.DedupKey == dedupKey {
			result.WakeID = p.WakeID
			if p.NotificationSeq > result.Seq {
				result.Seq = p.NotificationSeq
			}
			result.Coalesced = true
			break
		}
	}
	if !result.Coalesced && notifyKey != "" {
		// The row is missing either because the insert predicate refused it
		// (the key was delivered concurrently) or because another process
		// already resolved it. Both mean one thing: this event must not start
		// a resume, and the caller must not keep a wake id to release.
		if delivered, err := s.store.IsWakeDelivered(ctx, notifyKey); err == nil && delivered {
			result.Suppressed = true
			result.WakeID = ""
		}
	}
	return result, nil
}

// ParentRunnable is a function the turn runner provides to decide whether the
// parent may start another turn right now. Returning false keeps the wake
// durable instead of dropping it (doc 6.5 rule 2: no concurrent second turn
// while running / waiting approval / compacting).
type ParentRunnable func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool

// DrainRunnable claims pending wakes at a runnable state-transition point and
// returns the aggregated preflight digest plus the claimed wake ids. It is
// the only place auto turns originate; it never launches a turn itself
// (doc 6.5 rule 5: the wake prompt references only the lifecycle digest).
func (s *WakeScheduler) DrainRunnable(ctx context.Context, parentSessionID, parentTeamID, rootScopeID string, runnable ParentRunnable) ([]WakePending, *Digest, error) {
	pending, err := s.store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: parentSessionID,
		TargetParentTeamID:    parentTeamID,
		UnclaimedOnly:         true,
	})
	if err != nil {
		return nil, nil, err
	}
	if len(pending) == 0 {
		return nil, nil, nil
	}
	// The runnable check only matters when there is a wake to consume: a
	// busy parent with nothing pending is a cheap no-op, not an error.
	if runnable != nil && !runnable(ctx, rootScopeID, parentSessionID, parentTeamID) {
		return nil, nil, ErrWakeParentBusy
	}

	now := s.now().UTC()
	// Budget each class independently (doc 6.5 rule 4, P1-6 方案 1): an
	// exhausted failure budget must not delay a blocking approval wake. A
	// partially drained batch leaves the disallowed wakes durable for the
	// next window; no notification is ever dropped.
	allowed := make(map[WakeBudgetClass]bool, 3)
	for _, w := range pending {
		class := WakeBudgetClassOf(w.WakeReason)
		if _, ok := allowed[class]; ok {
			continue
		}
		allowed[class] = s.AllowAutoWake(ctx, rootScopeID, class, now)
	}
	drainable := false
	for _, ok := range allowed {
		if ok {
			drainable = true
			break
		}
	}
	if !drainable {
		return nil, nil, ErrWakeRateLimited
	}

	var claimed []WakePending
	claimedByClass := map[WakeBudgetClass]int{}
	classReasons := map[WakeBudgetClass]string{}
	// A wake stores the sequence of the notification that caused it. Digest
	// AfterSeq is exclusive, so use one less than the earliest claimed
	// sequence; using the latest sequence directly drops every triggering
	// notification from the wake digest.
	afterSeq := int64(0)
	firstNotificationSeq := int64(0)
	for _, w := range pending {
		class := WakeBudgetClassOf(w.WakeReason)
		if !allowed[class] {
			continue // budget consumed by a higher-priority class this drain
		}
		ok, err := s.store.ClaimWakePending(ctx, w.WakeID, WakeClaimOwner, now)
		if err != nil {
			return claimed, nil, err
		}
		if !ok {
			continue // claimed by a concurrent drainer; skip
		}
		if w.NotificationSeq > 0 && (firstNotificationSeq == 0 || w.NotificationSeq < firstNotificationSeq) {
			firstNotificationSeq = w.NotificationSeq
		}
		claimedByClass[class]++
		if _, ok := classReasons[class]; !ok {
			classReasons[class] = w.WakeReason
		}
		claimed = append(claimed, w)
	}
	if len(claimed) == 0 {
		return nil, nil, nil
	}
	if firstNotificationSeq > 0 {
		afterSeq = firstNotificationSeq - 1
	}

	digest, err := BuildDigest(ctx, s.store, DigestRequest{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: parentSessionID,
		TargetParentTeamID:    parentTeamID,
		AfterSeq:              afterSeq,
		IncludeResolvedSince:  true,
		HostCapabilities:      s.hostCapabilitySnapshot(),
		// The P2-D progress wake has no lifecycle notification; without this
		// projection its digest would be empty and could never start the
		// report turn it exists for.
		Progress: s.progressSource(),
	})
	if err != nil {
		return claimed, nil, err
	}
	// Mark delivered: a wake counts as a delivery attempt toward the parent.
	for _, item := range digest.Items {
		if item.NotificationID == "" {
			continue
		}
		// Rows that already travelled that far are skipped so a repeated drain
		// cannot churn version/updated_at on an unchanged row (plan §4.3).
		if item.DeliveryState == DeliveryDelivered || item.DeliveryState == DeliverySeen {
			continue
		}
		_ = s.store.MarkNotificationDelivered(ctx, item.NotificationID, now)
	}
	// Only a digest that actually turns into a parent turn spends budget:
	// claiming a stale wake whose notification was resolved while the parent
	// was busy must not consume the next window, while a progress-only digest
	// (which is delivered, see WakeConsumer) must, or the opt-in progress
	// check would flood the parent with report turns once per tick.
	if digestDeliverable(claimed, digest) {
		for class, count := range claimedByClass {
			if count == 0 {
				continue
			}
			s.recordClaim(ctx, rootScopeID, parentSessionID, class, classReasons[class], now)
		}
	}
	return claimed, digest, nil
}

// digestDeliverable reports whether a drained digest carries content worth one
// parent turn for the given claimed wakes. Lifecycle items always qualify. A
// progress-only digest (P2-D progress check) qualifies too, because the rollup
// is exactly what that wake exists to surface — but only for wakes that request
// progress: a stale lifecycle wake whose notification was acknowledged or
// resolved while the parent was busy must keep its "no content-free turn"
// guarantee even when the scope happens to have an active batch rollup.
//
// Both the delivery guard (WakeConsumer) and the budget accounting
// (DrainRunnable) use this predicate so a turn that starts is always a turn
// that spends budget, and a wake that is drained without a turn never does.
func digestDeliverable(claimed []WakePending, digest *Digest) bool {
	if digest == nil {
		return false
	}
	if len(digest.Items) > 0 {
		return true
	}
	if len(digest.Progress) == 0 {
		return false
	}
	for _, wake := range claimed {
		if WakeReasonIsProgressCheck(wake.WakeReason) {
			return true
		}
	}
	return false
}

// ResolveWake releases a claimed wake after the turn consumed it.
func (s *WakeScheduler) ResolveWake(ctx context.Context, wakeID string) error {
	return s.store.ResolveWakePending(ctx, strings.TrimSpace(wakeID))
}

// WakeClaimOwner is the claim owner recorded by the scheduler's own drain path.
// A release must present the same owner so a concurrent drainer's claim is
// never un-claimed (plan C2-5 / A6).
const WakeClaimOwner = "wake_scheduler"

// ReleaseWake returns a claimed wake to the pending queue without consuming it
// (plan C2-5 / A6): when the resume capacity gate defers a resume, the wake
// must stay durable and keep its FIFO slot for the next runnable window
// instead of being dropped or marked delivered.
func (s *WakeScheduler) ReleaseWake(ctx context.Context, wakeID, claimedBy string) error {
	if s == nil || s.store == nil {
		return nil
	}
	_, err := s.store.ReleaseWakePending(ctx, strings.TrimSpace(wakeID), strings.TrimSpace(claimedBy))
	return err
}

// WakeQueuePosition is the bounded-FIFO readout for a deferred resume
// (plan C2-5 / A6): the 1-based FIFO position of a wake among its parent's
// unclaimed pending wakes plus the queue length. Position 0 means the wake is
// no longer queued (claimed by a concurrent drainer or already resolved), which
// lets a caller distinguish "still waiting" from "gone".
type WakeQueuePosition struct {
	Position int
	Total    int
}

// QueuePosition reports where a wake sits in its parent's pending queue. The
// store orders pending rows by created_at ASC, so the index of the deferred row
// is exactly the FIFO position the digest must show the parent.
func (s *WakeScheduler) QueuePosition(ctx context.Context, rootScopeID, parentSessionID, parentTeamID, wakeID string) (WakeQueuePosition, error) {
	if s == nil || s.store == nil {
		return WakeQueuePosition{}, nil
	}
	wakeID = strings.TrimSpace(wakeID)
	if wakeID == "" {
		return WakeQueuePosition{}, nil
	}
	pending, err := s.store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: parentSessionID,
		TargetParentTeamID:    parentTeamID,
		UnclaimedOnly:         true,
	})
	if err != nil {
		return WakeQueuePosition{}, err
	}
	for i, w := range pending {
		if w.WakeID == wakeID {
			return WakeQueuePosition{Position: i + 1, Total: len(pending)}, nil
		}
	}
	return WakeQueuePosition{Total: len(pending)}, nil
}

// PendingDepth counts the unclaimed wakes already waiting for a parent. It is
// the queue the resume would join when the capacity gate denies it (A6).
func (s *WakeScheduler) PendingDepth(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) (int, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}
	pending, err := s.store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: parentSessionID,
		TargetParentTeamID:    parentTeamID,
		UnclaimedOnly:         true,
	})
	if err != nil {
		return 0, err
	}
	return len(pending), nil
}

// FilterDeliveredWakes drops claimed wakes whose notify key was already
// delivered (plan C2-4 #15 / B4). It is the delivery-time half of the
// idempotency rule: ScheduleWake stops a replayed event from becoming a
// pending row, and this call stops a row that was already persisted (for
// example before a crash) from starting a second resume (AC-P1-4a). Wakes
// without a key, and reads that fail, are kept — the wake must not be lost
// because the ledger is temporarily unreadable.
func (s *WakeScheduler) FilterDeliveredWakes(ctx context.Context, wakes []WakePending) []WakePending {
	if s == nil || s.store == nil || len(wakes) == 0 {
		return wakes
	}
	kept := make([]WakePending, 0, len(wakes))
	for _, w := range wakes {
		key := strings.TrimSpace(w.NotifyKey)
		if key == "" {
			kept = append(kept, w)
			continue
		}
		delivered, err := s.store.IsWakeDelivered(ctx, key)
		if err != nil || !delivered {
			kept = append(kept, w)
		}
	}
	return kept
}

// RecordWakeDelivery marks the given wakes as delivered, which is what makes
// the idempotency key outlive the pending row (plan C2-4 #15 / B4). It must be
// called only after a delivery actually happened: a failed delivery is never
// recorded, so the retry reuses the same key and still reaches the parent
// (AC-P1-4b). Recording is best-effort per wake; the first error is returned
// so callers can log it without losing the remaining keys.
func (s *WakeScheduler) RecordWakeDelivery(ctx context.Context, wakes []WakePending) error {
	if s == nil || s.store == nil {
		return nil
	}
	now := s.now().UTC()
	var firstErr error
	for _, w := range wakes {
		key := strings.TrimSpace(w.NotifyKey)
		if key == "" {
			continue
		}
		err := s.store.MarkWakeDelivered(ctx, WakeDelivered{
			NotifyKey:             key,
			RootScopeID:           strings.TrimSpace(w.RootScopeID),
			TargetParentSessionID: strings.TrimSpace(w.TargetParentSessionID),
			TargetParentTeamID:    strings.TrimSpace(w.TargetParentTeamID),
			WakeID:                w.WakeID,
			TurnID:                strings.TrimSpace(w.TurnID),
			EventKind:             strings.TrimSpace(w.EventKind),
			ObligationID:          strings.TrimSpace(w.ObligationID),
			EventSeq:              w.EventSeq,
			DeliveredAt:           now,
			DeliveredBy:           "wake_consumer",
		})
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// AllowAutoWake reports whether the root scope may deliver another auto turn
// of the given budget class right now (doc 6.5 rule 4, P1-6 方案 1/2).
func (s *WakeScheduler) AllowAutoWake(ctx context.Context, rootScopeID string, class WakeBudgetClass, now time.Time) bool {
	limit := s.budgetLimit(class)
	if limit == unlimitedWakeBudget {
		return true
	}
	windowStart := now.Add(-s.rateWindow)
	if s.budgetMode == WakeBudgetModeDurable && s.store != nil {
		if count, err := s.store.CountWakeClaims(ctx, rootScopeID, class, windowStart); err == nil {
			return count < limit
		}
		// A durable read failure must not silence the parent: fall back to
		// the in-process window until the store recovers.
	}
	return s.memoryUsage(rootScopeID, class, windowStart) < limit
}

// BudgetState reports the rolling usage for one root scope and class. Hosts
// use it for diagnostics (for example the P0-4 `/debug` supervisor view); it
// never mutates the ledger.
func (s *WakeScheduler) BudgetState(ctx context.Context, rootScopeID string, class WakeBudgetClass) WakeBudgetState {
	now := s.now().UTC()
	state := WakeBudgetState{
		RootScopeID: rootScopeID,
		BudgetClass: class,
		Window:      s.rateWindow,
		WindowStart: now.Add(-s.rateWindow),
	}
	if limit := s.budgetLimit(class); limit == unlimitedWakeBudget {
		state.Unlimited = true
	} else {
		state.Limit = limit
	}
	if s.budgetMode == WakeBudgetModeDurable && s.store != nil {
		if count, err := s.store.CountWakeClaims(ctx, rootScopeID, class, state.WindowStart); err == nil {
			state.Used = count
			return state
		}
	}
	state.Used = s.memoryUsage(rootScopeID, class, state.WindowStart)
	return state
}

// budgetLimit returns the per-window cap of a class; unlimitedWakeBudget means
// the class has no hard cap.
func (s *WakeScheduler) budgetLimit(class WakeBudgetClass) int {
	switch class {
	case WakeBudgetClassApproval:
		return s.maxApprovalWake
	case WakeBudgetClassProgress:
		return s.maxProgressWake
	}
	return s.maxAutoWake
}

// memoryUsage prunes and counts the in-process window for one class.
func (s *WakeScheduler) memoryUsage(rootScopeID string, class WakeBudgetClass, windowStart time.Time) int {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	key := wakeBudgetKey(rootScopeID, class)
	recent := s.claims[key][:0]
	for _, t := range s.claims[key] {
		if t.After(windowStart) {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(s.claims, key)
		return 0
	}
	s.claims[key] = recent
	return len(recent)
}

// recordClaim books one delivered auto turn against the class budget. The
// in-process window is always updated (it is the fallback used when the
// durable ledger is unavailable); the durable row is written only in
// WakeBudgetModeDurable.
func (s *WakeScheduler) recordClaim(ctx context.Context, rootScopeID, parentSessionID string, class WakeBudgetClass, reason string, now time.Time) {
	s.rateMu.Lock()
	key := wakeBudgetKey(rootScopeID, class)
	s.claims[key] = append(s.claims[key], now)
	s.rateMu.Unlock()

	if s.budgetMode != WakeBudgetModeDurable || s.store == nil {
		return
	}
	claim := WakeClaim{
		ClaimID:               uniqueSupervisionID("wakeclaim-" + string(class) + "-"),
		RootScopeID:           rootScopeID,
		BudgetClass:           class,
		WakeReason:            reason,
		TargetParentSessionID: parentSessionID,
		ClaimedAt:             now,
		ClaimedBy:             "wake_scheduler",
	}
	if err := s.store.RecordWakeClaim(ctx, claim); err != nil {
		// Best effort: the delivered turn must not fail because budget
		// bookkeeping did, and the in-process window still bounds this
		// process. The durable ledger self-heals on the next drain.
		return
	}
	// Opportunistic retention: keep roughly one extra window of history so a
	// long window (or slight clock skew) still counts the right rows.
	_, _ = s.store.PruneWakeClaims(ctx, now.Add(-2*s.rateWindow))
}

func wakeBudgetKey(rootScopeID string, class WakeBudgetClass) string {
	return strings.TrimSpace(rootScopeID) + "|" + string(class)
}

// newWakeID builds a durable wake identifier. It must not depend on the clock
// alone: two critical lifecycle events projected inside the same clock tick
// would otherwise share a primary key and one of the wakes would be dropped
// silently by the INSERT (see ids.go).
func newWakeID() string {
	return uniqueSupervisionID("wake_")
}
