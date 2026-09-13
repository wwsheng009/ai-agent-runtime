package supervision

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// Identifiers in this package are primary keys, so they must stay unique even
// when the host clock is coarse. Go reads time.Now() from the operating system
// clock, and on some hosts (notably Windows guests) many consecutive calls
// return the exact same nanosecond value. Identifiers built from UnixNano
// alone then collide, and an INSERT ... OR IGNORE drops the row silently: a
// scheduled wake (including a blocking approval) disappears without any error,
// so the parent is never woken.
//
// A process-local sequence disambiguates ids generated inside one clock tick,
// and a per-process random suffix keeps ids unique across processes that share
// a durable database. The format stays opaque and ordered by creation time.
var (
	uniqueIDSeq     atomic.Uint64
	uniqueIDProcess = randomIDProcessSuffix()
)

// uniqueSupervisionID builds a collision-resistant identifier with the given
// prefix, for example "wake_" or "act-".
func uniqueSupervisionID(prefix string) string {
	return fmt.Sprintf("%s%d-%d-%s", prefix, time.Now().UnixNano(), uniqueIDSeq.Add(1), uniqueIDProcess)
}

// randomIDProcessSuffix returns a short random string that distinguishes
// identifiers created by different processes in the same clock tick.
func randomIDProcessSuffix() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", os.Getpid())
	}
	return hex.EncodeToString(buf[:])
}
