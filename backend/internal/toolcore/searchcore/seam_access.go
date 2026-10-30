package searchcore

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/localio"
)

// seamAccess resolves "no accessor given" to this process's filesystem, in one place.
//
// Every options struct in this package carries an Access field, so without a single
// definition each entry point would hand-roll its own nil check and the meaning of
// "unset" would drift - while the whole point of leaving it unset is precisely that an
// unconverted caller keeps the behaviour it always had.
func seamAccess(access toolcore.Access) toolcore.Access {
	if access == nil {
		return localio.Access
	}
	return access
}
