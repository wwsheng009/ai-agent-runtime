package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
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
	UpdateSessions(sessions []wire.SessionSummary)
}

// SetGatewayController injects the gateway control plane (runtime-server startup).
func (h *Handler) SetGatewayController(controller GatewayController) {
	if h == nil {
		return
	}
	h.gatewayController = controller
}

// GatewaySessions returns a lightweight session summary list for the gateway
// register. It is best-effort: an unavailable store yields an empty list.
func (h *Handler) GatewaySessions() []wire.SessionSummary {
	if h == nil || h.sessionManager == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sessions, err := h.sessionManager.SearchSessions(ctx, &runtimechat.SessionSearchOptions{})
	if err != nil {
		return nil
	}
	sessions = filterAddressableSessions(sessions)
	out := make([]wire.SessionSummary, 0, len(sessions))
	store := h.gatewayStateStore()
	for _, session := range sessions {
		if session == nil || strings.TrimSpace(session.ID) == "" {
			continue
		}
		summary := wire.SessionSummary{
			SessionID: session.ID,
			Title:     strings.TrimSpace(session.Metadata.Title),
			// Pre-set the host's own session state; the shared projection
			// overrides it whenever it has tracked a richer state.
			State: string(session.State),
		}
		if store != nil {
			store.Get(session.ID).Apply(&summary)
		}
		out = append(out, summary)
	}
	// 子代理行（display-only，默认开启）：把仍在跑的 subagent 任务作为子会话行
	// 追加在各自父会话之后，控制台据此展开/折叠；AICLI_GATEWAY_REPORT_SUBAGENTS=0
	// 可关闭（docs/gateway/07-console.md）。
	if gatewayagent.ReportSubagentsEnabled(nil) {
		out = h.appendGatewaySubagentSessions(ctx, out)
	}
	return out
}

// appendGatewaySubagentSessions appends the live subagent rows for the parent
// sessions in the list. Bounded by gatewayagent.SubagentSessionMax across the
// whole list and best-effort: a missing store or a read error leaves the parent
// rows untouched. Terminal tasks are never listed (the parent turn reports them).
func (h *Handler) appendGatewaySubagentSessions(ctx context.Context, sessions []wire.SessionSummary) []wire.SessionSummary {
	if h == nil || len(sessions) == 0 {
		return sessions
	}
	store := h.peekSubagentBatchStore()
	if store == nil {
		return sessions
	}
	parents := append([]wire.SessionSummary(nil), sessions...)
	added := 0
	for _, parent := range parents {
		if added >= gatewayagent.SubagentSessionMax {
			break
		}
		parentID := strings.TrimSpace(parent.SessionID)
		if parentID == "" {
			continue
		}
		batches, err := store.ListBatches(ctx, subagentbatch.BatchFilter{
			ParentSessionID: parentID,
			Status: []subagentbatch.BatchStatus{
				subagentbatch.BatchQueued,
				subagentbatch.BatchRunning,
				subagentbatch.BatchPartiallyCompleted,
			},
			Limit: gatewayagent.SubagentSessionMax,
		})
		if err != nil {
			continue
		}
		for _, batch := range batches {
			tasks, err := store.ListTasks(ctx, batch.BatchID)
			if err != nil {
				continue
			}
			for _, task := range tasks {
				if added >= gatewayagent.SubagentSessionMax {
					break
				}
				if task.Status.Terminal() {
					continue
				}
				childID := strings.TrimSpace(task.ChildSessionID)
				if childID == "" {
					continue
				}
				title := strings.TrimSpace(task.TaskSubject)
				if title == "" {
					title = strings.TrimSpace(task.TaskID)
				}
				var started time.Time
				if task.StartedAt != nil {
					started = *task.StartedAt
				}
				sessions = append(sessions, gatewayagent.SubagentSessionSummary(
					parentID, childID, task.TaskID, task.TaskType, title, started))
				added++
			}
		}
	}
	return sessions
}

// pushGatewaySessions pushes the current session list to the gateway. It is a
// no-op when no gateway controller is attached.
func (h *Handler) pushGatewaySessions() {
	if h == nil || h.gatewayController == nil {
		return
	}
	h.gatewayController.UpdateSessions(h.GatewaySessions())
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
