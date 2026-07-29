package agent

import runtimecheckpoint "github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"

// CheckpointManager manages automatic checkpoints and restores.
type CheckpointManager = runtimecheckpoint.Manager

// GetCheckpointManager returns the capture manager. Once capture is explicitly
// disabled it stays nil until ApplyCheckpointConfig re-enables it.
func (a *Agent) GetCheckpointManager() *CheckpointManager {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.checkpointDisabled {
		return nil
	}
	return a.ensureCheckpointManagerLocked()
}

// GetCheckpointRestoreManager returns a manager capable of reading/restoring
// existing checkpoints even when new checkpoint capture is disabled.
func (a *Agent) GetCheckpointRestoreManager() *CheckpointManager {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ensureCheckpointManagerLocked()
}

func (a *Agent) ensureCheckpointManagerLocked() *CheckpointManager {
	if a.checkpointMgr == nil && a.artifacts != nil {
		a.checkpointMgr = runtimecheckpoint.NewManager(a.artifacts, a.eventBus)
	}
	return a.checkpointMgr
}

// SetCheckpointManager overrides the manager. Supplying a manager also enables
// capture; supplying nil preserves the current enable/disable state.
func (a *Agent) SetCheckpointManager(manager *CheckpointManager) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.checkpointMgr = manager
	if manager != nil {
		a.checkpointDisabled = false
	}
}

// CheckpointStorageOptions controls optional storage-lean capture behavior.
type CheckpointStorageOptions struct {
	StoreMode                string
	ConversationSnapshot     bool
	MaxDiffBytes             int64
	MaxCheckpointsPerSession int
}

// ApplyCheckpointConfig applies runtime checkpoint capture and storage limits.
// Existing two-argument callers remain compatible; storage options are optional.
// Disabling capture never removes access to the artifact store used for restore.
func (a *Agent) ApplyCheckpointConfig(enabled bool, maxFileBytes int64, options ...CheckpointStorageOptions) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.checkpointDisabled = !enabled
	if !enabled {
		return
	}
	manager := a.ensureCheckpointManagerLocked()
	if manager == nil {
		return
	}
	if maxFileBytes >= 0 {
		manager.MaxFileBytes = maxFileBytes
	}
	if len(options) == 0 {
		return
	}
	storage := options[0]
	manager.StoreMode = storage.StoreMode
	manager.ConversationSnapshot = storage.ConversationSnapshot
	if storage.MaxDiffBytes >= 0 {
		manager.MaxDiffBytes = storage.MaxDiffBytes
	}
	if storage.MaxCheckpointsPerSession >= 0 {
		manager.MaxCheckpointsPerSession = storage.MaxCheckpointsPerSession
	}
}

// CheckpointCaptureEnabled reports whether automatic mutation capture is active.
func (a *Agent) CheckpointCaptureEnabled() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return !a.checkpointDisabled && a.checkpointMgr != nil
}
