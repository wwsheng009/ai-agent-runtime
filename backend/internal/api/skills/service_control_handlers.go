package skills

import (
	"net/http"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

type RuntimeServiceStatus struct {
	Running          bool   `json:"running"`
	PID              int    `json:"pid"`
	PIDFile          string `json:"pid_file,omitempty"`
	ListenAddr       string `json:"listen_addr,omitempty"`
	ConfigPath       string `json:"config_path,omitempty"`
	Cwd              string `json:"cwd,omitempty"`
	Executable       string `json:"executable,omitempty"`
	StartedAt        string `json:"started_at,omitempty"`
	RestartSupported bool   `json:"restart_supported"`
	Note             string `json:"note,omitempty"`
}

type RuntimeServiceRestartResult struct {
	Accepted    bool   `json:"accepted"`
	Message     string `json:"message,omitempty"`
	RequestedAt string `json:"requested_at,omitempty"`
}

type RuntimeServiceControlService interface {
	Status() (*RuntimeServiceStatus, error)
	Restart() (*RuntimeServiceRestartResult, error)
}

func (h *Handler) SetServiceControlService(service RuntimeServiceControlService) {
	h.serviceControlService = service
}

func (h *Handler) GetRuntimeServiceStatus(w http.ResponseWriter, r *http.Request) {
	if h.serviceControlService == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "runtime service control not configured"))
		return
	}

	status, err := h.serviceControlService.Status()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "failed to get runtime service status", err))
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"service": status,
	})
}

func (h *Handler) RestartRuntimeService(w http.ResponseWriter, r *http.Request) {
	if h.serviceControlService == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "runtime service control not configured"))
		return
	}

	result, err := h.serviceControlService.Restart()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "failed to restart runtime service", err))
		return
	}

	h.writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"restart": result,
	})
}
