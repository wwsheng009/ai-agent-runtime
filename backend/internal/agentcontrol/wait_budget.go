package agentcontrol

import (
	"strings"
	"sync"
)

// DefaultMaxConsecutiveWaitWithoutProgress is the built-in budget for
// consecutive no-progress active wait segments on one parent turn (design
// §16.2 成本口径："连续 active wait 超过 2 次且无进展 ⇒ 提示模型改用挂起").
//
// A "segment" is one wait_agent observation window. Progress means the ledger
// reported at least one terminal_delta during the segment. Once the budget is
// spent the host must stop opening new active windows for that turn and steer
// the model to the suspension path instead: the design keeps wait_agent itself
// from parking a turn, so the enforcement is "no more active waiting" plus
// next_action=suspend — a parent that then tries to finish is caught by I1,
// which converts the premature finalize into a turn suspension.
const DefaultMaxConsecutiveWaitWithoutProgress = 2

// WaitBudget tracks consecutive no-progress active wait segments per parent
// turn. It is deliberately in-memory and host-local: the durable truth of a
// turn lives in the §6.12 parked-turn record, while this budget only bounds
// how much wall-clock/token an active wait may burn before the host stops
// granting new windows. Losing the counter on restart is safe (worst case a
// resumed turn gets a fresh budget), which is why it must never gate finalize
// or obligation semantics.
type WaitBudget struct {
	mu      sync.Mutex
	entries map[string]*waitBudgetEntry
}

type waitBudgetEntry struct {
	consecutive int
}

// Observe records one completed active wait segment. progress=true (something
// reached a terminal state during the segment) resets the counter. limit<=0
// disables the budget and always reports exhausted=false.
func (b *WaitBudget) Observe(key string, progress bool, limit int) (consecutive int, exhausted bool) {
	if b == nil || limit <= 0 {
		return 0, false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.entries == nil {
		b.entries = make(map[string]*waitBudgetEntry)
	}
	entry := b.entries[key]
	if entry == nil {
		entry = &waitBudgetEntry{}
		b.entries[key] = entry
	}
	if progress {
		entry.consecutive = 0
		return 0, false
	}
	entry.consecutive++
	return entry.consecutive, entry.consecutive >= limit
}

// Exhausted reports whether the key's budget is already spent, without
// consuming anything. It is the gate checked before opening a new active wait
// window.
func (b *WaitBudget) Exhausted(key string, limit int) (consecutive int, exhausted bool) {
	if b == nil || limit <= 0 {
		return 0, false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entries[key]
	if entry == nil {
		return 0, false
	}
	return entry.consecutive, entry.consecutive >= limit
}

// Reset clears the budget for one key (ledger drained, progress observed, or a
// new turn took over).
func (b *WaitBudget) Reset(key string) {
	if b == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, key)
}
