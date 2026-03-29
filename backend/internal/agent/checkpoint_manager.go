package agent

import runtimecheckpoint "github.com/ai-gateway/ai-agent-runtime/internal/checkpoint"

// CheckpointManager manages automatic checkpoints.
type CheckpointManager = runtimecheckpoint.Manager

// GetCheckpointManager returns the checkpoint manager.
func (a *Agent) GetCheckpointManager() *CheckpointManager {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.checkpointMgr == nil {
		if a.artifacts != nil {
			a.checkpointMgr = runtimecheckpoint.NewManager(a.artifacts, a.eventBus)
		}
	}
	return a.checkpointMgr
}

// SetCheckpointManager sets the checkpoint manager.
func (a *Agent) SetCheckpointManager(manager *CheckpointManager) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.checkpointMgr = manager
}
