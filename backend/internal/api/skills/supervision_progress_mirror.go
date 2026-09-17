package skills

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// subagentProgressMirror returns the per-host, long-lived live-only progress
// mirror (P0-1c/M7 parity with the CLI host). The same instance serves both
// the per-child runtime-event subscription (Observe → parent-stream live
// event) and the durable progress projection's Messages hook, so
// supervision_descendants / preflight digests enrich last_message identically
// on both hosts.
//
// The mirror never writes to the event store: `subagent.progress` is
// registered as ChannelLiveOnly in internal/events/contract.go.
func (h *Handler) subagentProgressMirror() *supervision.SubagentProgressMirror {
	if h == nil {
		return nil
	}
	h.supervisionProgressMirrorOnce.Do(func() {
		h.supervisionProgressMirrorValue = supervision.NewSubagentProgressMirror(supervision.DefaultSubagentProgressWindow)
	})
	return h.supervisionProgressMirrorValue
}
