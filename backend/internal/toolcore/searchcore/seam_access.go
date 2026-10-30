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

// seamAccessIsLocal answers the one question a shared core may ask about *where* an
// accessor points: does it serve this process's own machine?
//
// It exists for facts that are properties of the host rather than of the files being
// searched, and which therefore have no honest answer on a remote accessor. The global
// gitignore is the first such fact: it lives under a HOME, and HOME belongs to whoever
// runs the tool, not to the machine holding the matches. The predicate is asked of
// localio because only that package knows its own implementation type; see IsLocal.
func seamAccessIsLocal(access toolcore.Access) bool {
	return localio.IsLocal(seamAccess(access))
}
