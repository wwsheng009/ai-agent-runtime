package supervision

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// The fact ledger refuses to treat host control-plane prompts as user
// assertions, and that guard keys off types.SupervisionPromptTag. Both prompt
// producers here still concatenate a literal body onto the tag, so a rename on
// one side would silently let the durable guard harvest runtime bookkeeping
// again. The digest header needs no such assertion: digest.go now renders the
// shared constant itself.
func TestHostPromptsStillCarryTheSharedControlPlaneTag(t *testing.T) {
	require.True(t, strings.HasPrefix(AutoWakePrompt, types.SupervisionPromptTag),
		"AutoWakePrompt must keep the tag durable consumers reject on")
	require.True(t, types.IsControlPlanePromptText(AutoWakePrompt))

	resume := ResumePrompt(&ResumeContext{Text: "verdict"})
	require.True(t, strings.HasPrefix(resume, types.SupervisionPromptTag),
		"ResumePrompt must keep the tag durable consumers reject on")
	require.True(t, types.IsControlPlanePromptText(resume))

	// AutoWakePromptFor keeps the legacy prompt byte-identical without a resume
	// context, so adopting the tag constant must not have changed a single byte.
	require.Equal(t, AutoWakePrompt, AutoWakePromptFor(nil))
	require.Equal(t, "[supervision] ", AutoWakePrompt[:len(types.SupervisionPromptTag)+1])
}
