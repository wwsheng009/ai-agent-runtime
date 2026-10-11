package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
)

// GatewayController is the runtime-server's view of the gateway connection
// control plane (docs/gateway/12-connection-control.md). It is implemented by
// *gatewayagent.Controller; the interface keeps the handler testable and avoids
// a hard dependency on the concrete transport wiring.
type GatewayController interface {
	Status() gatewayagent.Status
	ConnectManual(ctx context.Context) error
	Disconnect(ctx context.Context) error
	Reconnect(ctx context.Context) error
	Config() gatewayagent.Config
	Configure(cfg gatewayagent.Config) error
}

// SetGatewayController injects the gateway control plane (runtime-server startup).
func (h *Handler) SetGatewayController(controller GatewayController) {
	if h == nil {
		return
	}
	h.gatewayController = controller
}

// gatewayControlTimeout bounds a manual connect/disconnect/reconnect so a hung
// dial cannot hold the HTTP request open indefinitely.
const gatewayControlTimeout = 20 * time.Second

func (h *Handler) gatewayNotConfigured(w http.ResponseWriter) {
	h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
		"gateway is not configured"))
}

// GetGatewayStatus returns the current connection status snapshot.
func (h *Handler) GetGatewayStatus(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		return
	}
	if h.gatewayController == nil {
		h.gatewayNotConfigured(w)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"status": h.gatewayController.Status()})
}

// ConnectGateway performs a manual connect (operator-initiated).
func (h *Handler) ConnectGateway(w http.ResponseWriter, r *http.Request) {
	h.gatewayAction(w, r, func(ctx context.Context) error { return h.gatewayController.ConnectManual(ctx) })
}

// DisconnectGateway performs a manual disconnect (runtime-only; config untouched).
func (h *Handler) DisconnectGateway(w http.ResponseWriter, r *http.Request) {
	h.gatewayAction(w, r, func(ctx context.Context) error { return h.gatewayController.Disconnect(ctx) })
}

// ReconnectGateway disconnects then connects again.
func (h *Handler) ReconnectGateway(w http.ResponseWriter, r *http.Request) {
	h.gatewayAction(w, r, func(ctx context.Context) error { return h.gatewayController.Reconnect(ctx) })
}

func (h *Handler) gatewayAction(w http.ResponseWriter, r *http.Request, run func(context.Context) error) {
	if h == nil {
		return
	}
	if h.gatewayController == nil {
		h.gatewayNotConfigured(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gatewayControlTimeout)
	defer cancel()
	if err := run(ctx); err != nil {
		h.writeError(w, http.StatusBadGateway, errors.Wrap(errors.ErrUpstreamUnavailable,
			"gateway action failed", err))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"status": h.gatewayController.Status()})
}

// GetGatewayConfig returns the configuration without the token secret.
func (h *Handler) GetGatewayConfig(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		return
	}
	if h.gatewayController == nil {
		h.gatewayNotConfigured(w)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"config": gatewayConfigPayload(h.gatewayController.Config()),
		"status": h.gatewayController.Status(),
	})
}

// UpdateGatewayConfig merges the request into the current configuration and
// applies it. Omitting token keeps the stored value; the token is never echoed.
func (h *Handler) UpdateGatewayConfig(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		return
	}
	if h.gatewayController == nil {
		h.gatewayNotConfigured(w)
		return
	}
	var req struct {
		URL         *string `json:"url"`
		Name        *string `json:"name"`
		Token       *string `json:"token"`
		AutoConnect *bool   `json:"auto_connect"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}
	cfg := h.gatewayController.Config()
	if req.URL != nil {
		cfg.URL = strings.TrimSpace(*req.URL)
	}
	if req.Name != nil {
		cfg.Name = strings.TrimSpace(*req.Name)
	}
	if req.Token != nil {
		cfg.Token = strings.TrimSpace(*req.Token)
	}
	if req.AutoConnect != nil {
		cfg.AutoConnect = *req.AutoConnect
	}
	cfg.Enabled = strings.TrimSpace(cfg.URL) != "" && strings.TrimSpace(cfg.Token) != ""
	if err := h.gatewayController.Configure(cfg); err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid,
			"failed to apply gateway config", err))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"config": gatewayConfigPayload(h.gatewayController.Config()),
		"status": h.gatewayController.Status(),
	})
}

// gatewayConfigPayload builds the safe (secret-free) config projection.
func gatewayConfigPayload(cfg gatewayagent.Config) map[string]interface{} {
	return map[string]interface{}{
		"enabled":      cfg.Enabled,
		"url":          cfg.URL,
		"name":         cfg.Name,
		"auto_connect": cfg.AutoConnect,
		"routes":       cfg.Routes,
		"token_hint":   cfg.TokenHint(),
	}
}
