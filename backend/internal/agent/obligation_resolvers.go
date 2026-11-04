package agent

import "github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"

// SetObligationResolvers installs the host's durable non-batch obligation
// readers on this agent. NewReActLoop fills any unset LoopReActConfig field
// from this value, and ChildAgentFactory copies it to in-process child agents,
// so a nested turn resolves the same child/team/job rows as its parent.
func (a *Agent) SetObligationResolvers(resolvers subagentbatch.ObligationResolvers) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.obligationResolvers = resolvers
	coordinator := a.batchCoordinator
	a.mu.Unlock()
	if coordinator != nil {
		coordinator.SetObligationResolvers(resolvers)
	}
}

// GetObligationResolvers returns a snapshot of the installed resolvers.
func (a *Agent) GetObligationResolvers() subagentbatch.ObligationResolvers {
	if a == nil {
		return subagentbatch.ObligationResolvers{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.obligationResolvers
}
