package runtimeapi

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// backgroundJobObligationResolver returns the handler's background manager as
// the durable job reader. The manager is the same instance the job_start tool
// uses; when it has not been built yet, getBackgroundManager installs it once so
// an obligation source wired earlier still sees later required-job rows.
func (h *Handler) backgroundJobObligationResolver() subagentbatch.BackgroundJobObligationResolver {
	if h == nil {
		return nil
	}
	h.backgroundMu.Lock()
	manager := h.backgroundManager
	h.backgroundMu.Unlock()
	if manager == nil {
		manager = h.getBackgroundManager(h.runtimeConfig)
	}
	if manager == nil {
		return nil
	}
	return manager
}

// obligationResolvers is the API host's complete non-batch resolver set. Every
// settle/admission/ledger path uses this projection so a required job cannot be
// omitted from one predicate while another understands it.
func (h *Handler) obligationResolvers() subagentbatch.ObligationResolvers {
	if h == nil {
		return subagentbatch.ObligationResolvers{}
	}
	return subagentbatch.ObligationResolvers{
		AgentSessions:  h.agentSessionObligationResolver(),
		Teams:          h.teamObligationResolver(),
		BackgroundJobs: h.backgroundJobObligationResolver(),
	}
}
