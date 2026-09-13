// Package uniqid mints identifiers that stay unique on hosts whose clock is
// coarse.
//
// Go reads time.Now() from the operating system clock, and on some hosts
// (notably Windows guests) many consecutive calls return the exact same
// nanosecond value. Identifiers built from UnixNano alone then collide. When
// such an id is a primary key or an idempotency key, the duplicate is often
// dropped silently (INSERT ... OR IGNORE, map overwrite), so the caller loses
// data without ever seeing an error. That is exactly how the supervision wake
// scheduler lost a blocking approval notification before this helper was
// extracted.
//
// New/Token append a process-local sequence and a per-process random suffix to
// the timestamp. Ids therefore stay unique both inside a single clock tick and
// across processes that share a durable database, while still sorting by
// creation time under the timestamp prefix.
package uniqid

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

var (
	sequence = atomic.Uint64{}
	process  = processSuffix()
)

// New returns prefix followed by a collision-resistant identifier, for example
// "wake_" + "1737000000000000000-7-9f3ac1d2".
func New(prefix string) string {
	return prefix + Token()
}

// Token returns an identifier without a prefix, for embedding in DSNs, file
// names, or composite keys.
func Token() string {
	return fmt.Sprintf("%d-%d-%s", time.Now().UnixNano(), sequence.Add(1), process)
}

// processSuffix returns a short random string that distinguishes identifiers
// minted by different processes inside the same clock tick. It falls back to
// the pid when the system entropy source fails, which still separates the
// common single-process case.
func processSuffix() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("p%d", os.Getpid())
	}
	return hex.EncodeToString(buf[:])
}
