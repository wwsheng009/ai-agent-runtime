package ui

import "time"

// PostOutcome classifies how a tracked post was handled. It exists so a
// producer can acknowledge a rejected or merged post explicitly instead of
// polling the actor to discover what happened.
type PostOutcome int

const (
	// PostDropped means the action was not accepted (controller closed, or the
	// non-blocking mailbox refused it). No revision will ever carry it, so a
	// producer must treat it as its own wake-up instead of waiting.
	PostDropped PostOutcome = iota
	// PostAdmitted means the action owns a mailbox slot. Its ticket is released
	// when that slot is applied, or earlier when a later coalescable post merges
	// into the same slot (the merged entry then carries the higher ticket).
	PostAdmitted
	// PostMerged means a coalescable action merged into an already queued slot;
	// the returned ticket is that slot's current ticket, so a waiter still
	// observes exactly the apply that carries the merged payload.
	PostMerged
)

// actionWaiter is a one-shot registration against the actor's applied/visible
// watermarks. The Run loop closes the channels exactly once while c.mu is held;
// a timed-out waiter removes itself under the same lock, so a channel is never
// closed after its waiter has abandoned it.
type actionWaiter struct {
	applied     chan struct{}
	visible     chan struct{}
	aborted     chan struct{}
	appliedSent bool
	visibleSent bool
	abortedSent bool
}

// PostTracked is Post plus the ack identity of the accepted action. Blocking
// semantics are identical to Post (coalescable merge, then bounded-capacity
// backpressure). The returned ticket is 0 exactly when the outcome is
// PostDropped.
func (c *UIController) PostTracked(action UIAction) (PostOutcome, uint64) {
	if c == nil || action == nil {
		return PostDropped, 0
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return PostDropped, 0
	}
	ticket := c.nextPostTicketLocked()
	if c.mergeCoalescableLocked(action, ticket) {
		c.observeAcceptedTicketLocked(ticket)
		c.mu.Unlock()
		return PostMerged, ticket
	}
	var waitStart time.Time
	for len(c.queue) >= c.cap {
		if c.closed {
			c.recordPostWaitLocked(waitStart)
			c.mu.Unlock()
			return PostDropped, 0
		}
		if waitStart.IsZero() {
			waitStart = time.Now()
		}
		c.cond.Wait()
	}
	c.recordPostWaitLocked(waitStart)
	if c.closed {
		c.mu.Unlock()
		return PostDropped, 0
	}
	c.appendQueuedLocked(action, ticket)
	c.observeAcceptedTicketLocked(ticket)
	c.mu.Unlock()
	c.cond.Broadcast()
	return PostAdmitted, ticket
}

// TryPostTracked is TryPost plus the ack identity of the accepted action.
func (c *UIController) TryPostTracked(action UIAction) (PostOutcome, uint64) {
	if c == nil || action == nil {
		return PostDropped, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return PostDropped, 0
	}
	ticket := c.nextPostTicketLocked()
	if c.mergeCoalescableLocked(action, ticket) {
		c.observeAcceptedTicketLocked(ticket)
		return PostMerged, ticket
	}
	if len(c.queue) >= c.cap {
		return PostDropped, 0
	}
	c.appendQueuedLocked(action, ticket)
	c.observeAcceptedTicketLocked(ticket)
	c.cond.Broadcast()
	return PostAdmitted, ticket
}

// PostDeferredTracked is PostDeferred plus the ack identity of the accepted
// action. The queue may exceed cap (see PostDeferred); admission never blocks.
func (c *UIController) PostDeferredTracked(action UIAction) (PostOutcome, uint64) {
	if c == nil || action == nil {
		return PostDropped, 0
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return PostDropped, 0
	}
	ticket := c.nextPostTicketLocked()
	if c.mergeCoalescableLocked(action, ticket) {
		c.observeAcceptedTicketLocked(ticket)
		c.deferredPosted++
		c.deferredMerged++
		c.mu.Unlock()
		return PostMerged, ticket
	}
	c.appendQueuedLocked(action, ticket)
	c.observeAcceptedTicketLocked(ticket)
	c.deferredPosted++
	if len(c.queue) > c.cap {
		c.capacityOverflow++
	}
	if len(c.queue) > c.peakPending {
		c.peakPending = len(c.queue)
	}
	c.mu.Unlock()
	c.cond.Broadcast()
	return PostAdmitted, ticket
}

// nextPostTicketLocked allocates the ack identity for one accepted post.
// Tickets increase monotonically, and a coalescable merge lifts the target
// slot's ticket to the maximum, so "ticket <= watermark" stays a valid
// release test even when posts merge into the same slot.
//
// 调用方必须持有 c.mu。
func (c *UIController) nextPostTicketLocked() uint64 {
	c.nextTicket++
	return c.nextTicket
}

// observeAcceptedTicketLocked records the newest ticket that owns (or will
// own, after a merge) a mailbox slot. Dropped posts deliberately do not
// advance it: their ticket numbers are never applied, so a capacity fence
// waiting on the newest *accepted* ticket always terminates.
//
// 调用方必须持有 c.mu。
func (c *UIController) observeAcceptedTicketLocked(ticket uint64) {
	if ticket > c.lastAcceptedTicket {
		c.lastAcceptedTicket = ticket
	}
}

// LastAcceptedTicket returns the highest ticket of any action accepted into
// the mailbox (admitted or merged). Waiting for its apply is the event-driven
// capacity fence for TryPost: once the applied watermark covers it, the
// mailbox holds no accepted action, so a retry must be admitted (or merged).
func (c *UIController) LastAcceptedTicket() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastAcceptedTicket
}

// mergeCoalescableLocked merges action into an already queued coalescable slot
// when one exists. It accounts posted/dropped like the historical Post merge
// path and returns true when the action was absorbed.
//
// 调用方必须持有 c.mu。
func (c *UIController) mergeCoalescableLocked(action UIAction, ticket uint64) bool {
	if action.Class() != ClassCoalescable {
		return false
	}
	key := action.CoalesceKey()
	if key == "" {
		return false
	}
	idx, ok := c.coalesce[key]
	if !ok || idx >= len(c.queue) || idx >= len(c.queueTickets) {
		return false
	}
	c.queue[idx] = mergeActions(c.queue[idx], action)
	if ticket > c.queueTickets[idx] {
		c.queueTickets[idx] = ticket
	}
	c.posted++
	c.dropped++
	return true
}

// appendQueuedLocked appends an admitted action and its ticket to the mailbox
// lanes. The two lanes are always resliced together, so their lengths stay
// equal.
//
// 调用方必须持有 c.mu。
func (c *UIController) appendQueuedLocked(action UIAction, ticket uint64) {
	c.queue = append(c.queue, action)
	c.queueTickets = append(c.queueTickets, ticket)
	if action.Class() == ClassCoalescable {
		if key := action.CoalesceKey(); key != "" {
			c.coalesce[key] = len(c.queue) - 1
		}
	}
	c.posted++
}

// WaitActionApplied blocks until the action posted with ticket has been applied
// and its AppState revision published. It returns true when the watermark
// already covers the ticket, false on timeout or when the actor aborted the
// wait at shutdown. ticket==0 returns true immediately (nothing to await);
// timeout<=0 performs a non-blocking check.
//
// Applied-only waits are state fences: the caller may read the controller
// snapshot/revision produced by its action, but not the physical frame. Use
// WaitActionVisible when the caller needs the batch's effects delivered.
func (c *UIController) WaitActionApplied(ticket uint64, timeout time.Duration) bool {
	return c.waitActionTicket(ticket, timeout, false)
}

// WaitActionVisible is WaitActionApplied plus the requirement that the batch
// containing the ticket finished effect delivery (flush included). It is the
// physical-frame fence: once true, the work caused by the action has crossed
// the effect consumer.
func (c *UIController) WaitActionVisible(ticket uint64, timeout time.Duration) bool {
	return c.waitActionTicket(ticket, timeout, true)
}

func (c *UIController) waitActionTicket(ticket uint64, timeout time.Duration, visible bool) bool {
	if c == nil || ticket == 0 {
		return true
	}
	c.mu.Lock()
	if c.ticketSatisfiedLocked(ticket, visible) {
		c.mu.Unlock()
		return true
	}
	w := c.registerWaiterLocked(ticket)
	c.mu.Unlock()
	if timeout <= 0 {
		return c.cancelWaiter(ticket, w, visible)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	if visible {
		select {
		case <-w.visible:
			return true
		case <-w.aborted:
			return false
		case <-timer.C:
			return c.cancelWaiter(ticket, w, visible)
		}
	}
	select {
	case <-w.applied:
		return true
	case <-w.aborted:
		return false
	case <-timer.C:
		return c.cancelWaiter(ticket, w, visible)
	}
}

// ticketSatisfiedLocked reports whether the current watermarks already cover
// the ticket. 调用方必须持有 c.mu。
func (c *UIController) ticketSatisfiedLocked(ticket uint64, visible bool) bool {
	if visible {
		return c.visibleTicket >= ticket
	}
	return c.appliedTicket >= ticket
}

func (c *UIController) registerWaiterLocked(ticket uint64) *actionWaiter {
	w := &actionWaiter{
		applied: make(chan struct{}),
		visible: make(chan struct{}),
		aborted: make(chan struct{}),
	}
	if c.waiters == nil {
		c.waiters = make(map[uint64][]*actionWaiter)
	}
	c.waiters[ticket] = append(c.waiters[ticket], w)
	return w
}

// cancelWaiter abandons a timed-out wait. It re-checks the watermark under the
// lock so a release that raced the timeout still reports success.
func (c *UIController) cancelWaiter(ticket uint64, w *actionWaiter, visible bool) bool {
	c.mu.Lock()
	c.removeWaiterLocked(ticket, w)
	satisfied := c.ticketSatisfiedLocked(ticket, visible)
	c.mu.Unlock()
	return satisfied
}

// removeWaiterLocked 调用方必须持有 c.mu。
func (c *UIController) removeWaiterLocked(ticket uint64, target *actionWaiter) {
	waiters := c.waiters[ticket]
	remaining := waiters[:0]
	for _, w := range waiters {
		if w == target {
			continue
		}
		remaining = append(remaining, w)
	}
	if len(remaining) == 0 {
		delete(c.waiters, ticket)
		return
	}
	c.waiters[ticket] = remaining
}

// releaseActionWaitersLocked closes the channels of every waiter covered by
// the current watermarks and drops fully released registrations. The applied
// watermark is advanced after the reducer published the state, and the visible
// watermark after the batch delivered its effects, so a released waiter
// observes exactly the fence it asked for.
//
// 调用方必须持有 c.mu。
func (c *UIController) releaseActionWaitersLocked() {
	if len(c.waiters) == 0 {
		return
	}
	for ticket, waiters := range c.waiters {
		remaining := waiters[:0]
		for _, w := range waiters {
			if !w.appliedSent && ticket <= c.appliedTicket {
				close(w.applied)
				w.appliedSent = true
			}
			if !w.visibleSent && ticket <= c.visibleTicket {
				close(w.visible)
				w.visibleSent = true
			}
			if w.appliedSent && w.visibleSent {
				continue
			}
			remaining = append(remaining, w)
		}
		if len(remaining) == 0 {
			delete(c.waiters, ticket)
			continue
		}
		c.waiters[ticket] = remaining
	}
}

// releaseAbortedWaitersLocked fails every still-registered waiter. Run calls it
// on exit after the close-drain, when no further ticket can be applied.
//
// 调用方必须持有 c.mu。
func (c *UIController) releaseAbortedWaitersLocked() {
	for ticket, waiters := range c.waiters {
		for _, w := range waiters {
			if !w.abortedSent {
				close(w.aborted)
				w.abortedSent = true
			}
		}
		delete(c.waiters, ticket)
	}
}
