package supervision

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Wake-related sentinel errors (doc 6.5).
var (
	// ErrWakeRateLimited is returned when the auto-wake budget for the root
	// scope is exhausted. The wake row stays durable and the notification is
	// never dropped; the caller should escalate to the operator and let the
	// next natural parent turn deliver the digest via preflight.
	ErrWakeRateLimited = errors.New("supervision: auto wake rate limit reached; notification kept durable")
	// ErrWakeParentBusy is returned when the parent session is not runnable;
	// the durable wake row is kept for the next runnable point.
	ErrWakeParentBusy = errors.New("supervision: parent not runnable; wake kept pending")
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
// 1 and 4).
type WakeSchedulerConfig struct {
	// DebounceWindow is reserved for future time-based coalescing; the
	// current implementation coalesces via the durable dedup key until the
	// wake is claimed and resolved.
	DebounceWindow time.Duration
	// RateWindow is the rolling window for auto-turn budgeting.
	RateWindow time.Duration
	// MaxAutoWakePerWindow caps auto-scheduled parent turns per root scope.
	// 0 uses the default (5 per hour).
	MaxAutoWakePerWindow int
}

// WakeScheduler subscribes the lifecycle inbox to the parent turn start
// path (doc 6.5 "wake 调度入口": no resident LLM polling goroutine; wake only
// delivers a pending digest into the existing turn start path).
type WakeScheduler struct {
	store Store

	rateMu   sync.Mutex
	claims   map[string][]time.Time // rootScopeID -> claim timestamps

	rateWindow         time.Duration
	maxAutoWake        int
	now                func() time.Time
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
	if maxAutoWake <= 0 {
		maxAutoWake = 5
	}
	return &WakeScheduler{
		store:         store,
		claims:        map[string][]time.Time{},
		rateWindow:    rateWindow,
		maxAutoWake:   maxAutoWake,
		now:           timeNow,
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
		WakeID:               "wake_" + newWakeID(),
		RootScopeID:          strings.TrimSpace(req.RootScopeID),
		TargetParentSessionID: strings.TrimSpace(req.TargetParentSessionID),
		TargetParentTeamID:   strings.TrimSpace(req.TargetParentTeamID),
		WakeReason:           strings.TrimSpace(req.WakeReason),
		NotificationSeq:      req.NotificationSeq,
		DedupKey:             dedupKey,
		CreatedAt:            s.now().UTC(),
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

	// Rate limit auto turns per root scope (doc 6.5 rule 4).
	if !s.allowAutoWake(rootScopeID, s.now().UTC()) {
		return nil, nil, ErrWakeRateLimited
	}

	var claimed []WakePending
	afterSeq := int64(0)
	for _, w := range pending {
		ok, err := s.store.ClaimWakePending(ctx, w.WakeID, "wake_scheduler", s.now().UTC())
		if err != nil {
			return claimed, nil, err
		}
		if !ok {
			continue // claimed by a concurrent drainer; skip
		}
		if w.NotificationSeq > afterSeq {
			afterSeq = w.NotificationSeq
		}
		claimed = append(claimed, w)
	}
	if len(claimed) == 0 {
		return nil, nil, nil
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
		_ = s.store.MarkNotificationDelivered(ctx, item.NotificationID, s.now().UTC())
	}
	s.recordClaim(rootScopeID, s.now().UTC())
	return claimed, digest, nil
}

// ResolveWake releases a claimed wake after the turn consumed it.
func (s *WakeScheduler) ResolveWake(ctx context.Context, wakeID string) error {
	return s.store.ResolveWakePending(ctx, strings.TrimSpace(wakeID))
}

// allowAutoWake checks the rolling budget for a root scope.
func (s *WakeScheduler) allowAutoWake(rootScopeID string, now time.Time) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	windowStart := now.Add(-s.rateWindow)
	recent := s.claims[rootScopeID][:0]
	for _, t := range s.claims[rootScopeID] {
		if t.After(windowStart) {
			recent = append(recent, t)
		}
	}
	s.claims[rootScopeID] = recent
	return len(recent) < s.maxAutoWake
}

func (s *WakeScheduler) recordClaim(rootScopeID string, now time.Time) {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	s.claims[rootScopeID] = append(s.claims[rootScopeID], now)
}

func newWakeID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
