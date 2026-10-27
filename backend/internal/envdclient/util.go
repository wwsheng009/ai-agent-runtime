package envdclient

import (
	"fmt"
	"io"
)

// readLimited reads at most limit bytes from r.
//
// It exists so every response body goes through the same bound: a remote agent
// endpoint that returns an enormous body must not be able to exhaust the runtime's
// memory. On overflow it reports an error rather than silently truncating, because
// a truncated file or listing that looks complete is worse than a clear failure.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > limit {
		return nil, fmt.Errorf("response exceeded the %d byte limit", limit)
	}
	return buf, nil
}
