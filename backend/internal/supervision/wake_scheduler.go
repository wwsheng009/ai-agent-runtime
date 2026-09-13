package supervision

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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
	// unlimitedWakeBudget marks a budget class without a hard cap.
	unlimitedWakeBudget = 0
)

// WakeBudgetMode selects where auto-wake budget claims are counted (P1-6 方案 2).
type WakeBudgetMode string

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
}

// WakeResult reports how ScheduleWake coalesced the event.
type WakeResult struct {
	WakeID   string `json:"wake_id,omitempty"`
	DedupKey string `json:"dedup_key,omitempty"`
	Seq      int64  `json:"seq,omitempty"`
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
	// BudgetMode selects the budget ledger (see WakeBudgetMode). The empty
	// value means WakeBudgetModeMemory.
	BudgetMode WakeBudgetMode
}

// WakeScheduler subscribes the lifecycle inbox to the parent turn start
// path (doc 6.5 "wake 调度入口": no resident LLM polling goroutine; wake only
// delivers a pending digest into the existing turn start path).
type WakeScheduler struct {
	store Store

	rateMu sync.Mutex
	claims map[string][]time.Time // rootScopeID|budgetClass -> claim timestamps
	// claimSeq keeps durable claim ids unique when several claims are booked
	// inside the same nanosecond (stubbed clocks or concurrent drains). A
	// duplicate id would be swallowed by the unique index and silently
	// under-count the window.
	claimSeq atomic.Uint64

	rateWindow      time.Duration
	maxAutoWake     int // 0 => unlimited (failure / other classes)
	maxApprovalWake int // 0 => unlimited (approval class)
	budgetMode      WakeBudgetMode
	now             func() time.Time
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
	budgetMode := config.BudgetMode
	if budgetMode != WakeBudgetModeDurable {
		budgetMode = WakeBudgetModeMemory
	}
	return &WakeScheduler{
		store:           store,
		claims:          map[string][]time.Time{},
		rateWindow:      rateWindow,
		maxAutoWake:     maxAutoWake,
		maxApprovalWake: maxApprovalWake,
		budgetMode:      budgetMode,
		now:             timeNow,
	}
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
	w := WakePending{
		WakeID:                "wake_" + newWakeID(),
		RootScopeID:           strings.TrimSpace(req.RootScopeID),
		TargetParentSessionID: strings.TrimSpace(req.TargetParentSessionID),
		TargetParentTeamID:    strings.TrimSpace(req.TargetParentTeamID),
		WakeReason:            strings.TrimSpace(req.WakeReason),
		NotificationSeq:       req.NotificationSeq,
		DedupKey:              dedupKey,
		CreatedAt:             s.now().UTC(),
	}
	if err := s.store.InsertWakePending(ctx, w); err != nil {
		return WakeResult{}, err
	}

	// Detect coalescing by listing the parent's pending wakes.
	result := WakeResult{WakeID: w.WakeID, DedupKey: dedupKey, Seq: req.NotificationSeq}
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
		ok, err := s.store.ClaimWakePending(ctx, w.WakeID, "wake_scheduler", now)
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
	})
	if err != nil {
		return claimed, nil, err
	}
	// Mark delivered: a wake counts as a delivery attempt toward the parent.
	for _, item := range digest.Items {
		if item.NotificationID == "" {
			continue
		}
		_ = s.store.MarkNotificationDelivered(ctx, item.NotificationID, now)
	}
	// Only a digest that actually carries content spends budget: claiming a
	// stale wake whose notification was resolved while the parent was busy
	// must not consume the next window.
	if len(digest.Items) > 0 {
		for class, count := range claimedByClass {
			if count == 0 {
				continue
			}
			s.recordClaim(ctx, rootScopeID, parentSessionID, class, classReasons[class], now)
		}
	}
	return claimed, digest, nil
}

// ResolveWake releases a claimed wake after the turn consumed it.
func (s *WakeScheduler) ResolveWake(ctx context.Context, wakeID string) error {
	return s.store.ResolveWakePending(ctx, strings.TrimSpace(wakeID))
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
	if class == WakeBudgetClassApproval {
		return s.maxApprovalWake
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
		ClaimID:               fmt.Sprintf("wakeclaim-%s-%d-%d", string(class), now.UnixNano(), s.claimSeq.Add(1)),
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

func newWakeID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
