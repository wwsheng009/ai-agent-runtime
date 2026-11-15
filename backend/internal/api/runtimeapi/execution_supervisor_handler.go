package runtimeapi

import (
	"net/http"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// ExecutionSupervisorSnapshot is the read-only runtime-server counterpart of
// aicli `/debug supervision watchdog`. It never lazily builds the supervisor.
func (h *Handler) ExecutionSupervisorSnapshot(w http.ResponseWriter, r *http.Request) {
	config := supervision.DefaultExecutionSupervisorConfig()
	config.Enabled = true
	store := h.getSupervisionStore()
	runStore, ok := store.(supervision.ExecutionRunStore)
	available := ok && runStore != nil
	supervisor := h.peekExecutionSupervisor()
	built := supervisor != nil
	stats := supervision.ExecutionSupervisorStats{}
	if built {
		config = supervisor.Config
		stats = supervisor.Stats()
	}
	snapshot := supervision.ExecutionSupervisorSnapshot{
		Available: available,
		Built:     built,
		Config:    config,
		Stats:     stats,
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"available": available,
		"built":     built,
		"config":    config,
		"stats":     stats,
		"text":      snapshot.Render(),
	})
}
