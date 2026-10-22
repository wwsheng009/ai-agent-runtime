package tools

import (
	"os"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
)

// writeFileAtomicLocal replaces path with data through the shared mutatecore
// atomic writer. The Local suffix is kept for the package-local call sites;
// agentconfig / runtimeserver / planstore / mesh each have their own config
// write channel, which is intentionally separate.
func writeFileAtomicLocal(path string, data []byte, fallbackMode os.FileMode) error {
	return mutatecore.WriteAtomic(path, data, fallbackMode)
}

// writeFileModeDefault is used when a brand-new file is created without an
// existing mode to preserve.
const writeFileModeDefault os.FileMode = mutatecore.DefaultFileMode
