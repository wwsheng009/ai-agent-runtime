package commands

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// backgroundJobObligationResolver returns the host's durable background-job
// reader for required job obligations. The manager implements the interface
// directly, so the loop resolves exactly the rows the job tool wrote.
func (h *localChatRuntimeHost) backgroundJobObligationResolver() subagentbatch.BackgroundJobObligationResolver {
	if h == nil || h.Background == nil {
		return nil
	}
	return h.Background
}

// obligationResolvers is the CLI host's complete non-batch resolver set. Every
// settle/admission/ledger path uses this projection so a required job cannot be
// omitted from one predicate while another understands it.
func (h *localChatRuntimeHost) obligationResolvers() subagentbatch.ObligationResolvers {
	if h == nil {
		return subagentbatch.ObligationResolvers{}
	}
	return subagentbatch.ObligationResolvers{
		AgentSessions:  h.agentSessionObligationResolver(),
		Teams:          h.teamObligationResolver(),
		BackgroundJobs: h.backgroundJobObligationResolver(),
	}
}
