package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

const (
	planPreviewMaxBytes = 512 * 1024
	planPreviewMaxRunes = 200_000
)

type sessionPlanModeResponse struct {
	SessionID            string   `json:"session_id"`
	Active               bool     `json:"active"`
	Status               string   `json:"status"`
	PlanPath             string   `json:"plan_path,omitempty"`
	WriteAllowPaths      []string `json:"write_allow_paths,omitempty"`
	PreviousMode         string   `json:"previous_mode,omitempty"`
	PermissionMode       string   `json:"permission_mode"`
	PendingExitRequest   bool     `json:"pending_exit_request,omitempty"`
	ExitDecision         string   `json:"exit_decision,omitempty"`
	Notes                string   `json:"notes,omitempty"`
	EnteredAt            string   `json:"entered_at,omitempty"`
	ExitedAt             string   `json:"exited_at,omitempty"`
	WorkspacePath        string   `json:"workspace_path,omitempty"`
	PlanContent          string   `json:"plan_content"`
	PlanContentAvailable bool     `json:"plan_content_available"`
	PlanContentTruncated bool     `json:"plan_content_truncated,omitempty"`
	PlanContentError     string   `json:"plan_content_error,omitempty"`
	Action               string   `json:"action,omitempty"`
}

type sessionPlanModeRequest struct {
	Action   string `json:"action"`
	Decision string `json:"decision"`
	PlanPath string `json:"plan_path"`
	Notes    string `json:"notes"`
}

// GetSessionPlanMode returns durable plan-mode state and a path-safe plan file preview.
func (h *Handler) GetSessionPlanMode(w http.ResponseWriter, r *http.Request) {
	session, err := h.loadSessionForPlanMode(r)
	if err != nil {
		h.writePlanModeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, h.buildSessionPlanModeResponse(session, ""))
}

// UpdateSessionPlanMode enters or exits plan mode for a session.
//
// Body:
//
//	{"action":"enter","plan_path":"plan.md"}
//	{"action":"exit","decision":"approve|request_changes|quit","notes":"..."}
//	{"action":"approve|request_changes|quit","notes":"..."}  // decision aliases
func (h *Handler) UpdateSessionPlanMode(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session manager not configured"))
		return
	}

	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	var req sessionPlanModeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}

	action, decision, err := normalizePlanModeAction(req)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}

	// Prefer live actor so mid-turn permission engine/run-meta stay in sync.
	if actor, ok := h.sessionPlanModeActor(sessionID); ok && actor != nil {
		session, applyErr := h.applyPlanModeViaActor(r.Context(), actor, sessionID, action, decision, req)
		if applyErr != nil {
			h.writePlanModeError(w, applyErr)
			return
		}
		h.writeJSON(w, http.StatusOK, h.buildSessionPlanModeResponse(session, action))
		return
	}

	ctx, cancel := sessionStoreQueryContext(r)
	defer cancel()
	session, err := h.sessionManager.GetSession(ctx, sessionID)
	if err != nil {
		writeSessionStoreError(w, err)
		return
	}

	if err := applyPlanModeToSession(session, action, decision, req.PlanPath, req.Notes); err != nil {
		h.writePlanModeError(w, err)
		return
	}
	if err := h.sessionManager.Update(ctx, session); err != nil {
		writeSessionStoreError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, h.buildSessionPlanModeResponse(session, action))
}

func (h *Handler) loadSessionForPlanMode(r *http.Request) (*chat.Session, error) {
	if h == nil || h.sessionManager == nil {
		return nil, errors.New(errors.ErrConfigInvalid, "session manager not configured")
	}
	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		return nil, errors.New(errors.ErrValidationFailed, "session id is required")
	}
	ctx, cancel := sessionStoreQueryContext(r)
	defer cancel()
	session, err := h.sessionManager.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return session, nil
}

func (h *Handler) sessionPlanModeActor(sessionID string) (*chat.SessionActor, bool) {
	if h == nil {
		return nil, false
	}
	h.sessionRuntimeMu.RLock()
	hub := h.sessionHub
	h.sessionRuntimeMu.RUnlock()
	if hub == nil {
		return nil, false
	}
	return hub.Get(sessionID)
}

func (h *Handler) applyPlanModeViaActor(
	ctx context.Context,
	actor *chat.SessionActor,
	sessionID string,
	action string,
	decision planmode.ExitDecision,
	req sessionPlanModeRequest,
) (*chat.Session, error) {
	if actor == nil {
		return nil, fmt.Errorf("session actor is not configured")
	}
	switch action {
	case "enter":
		if _, err := actor.EnterPlanMode(ctx, sessionID, toolbroker.EnterPlanModeArgs{
			PlanPath: strings.TrimSpace(req.PlanPath),
		}); err != nil {
			return nil, err
		}
	case "exit", "approve", "request_changes", "quit":
		if _, err := actor.ExitPlanMode(ctx, sessionID, toolbroker.ExitPlanModeArgs{
			Decision: string(decision),
			Notes:    strings.TrimSpace(req.Notes),
		}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported plan mode action %q", action)
	}
	session, err := h.sessionManager.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return session, nil
}

func applyPlanModeToSession(session *chat.Session, action string, decision planmode.ExitDecision, planPath, notes string) error {
	if session == nil {
		return fmt.Errorf("session is required")
	}
	switch action {
	case "enter":
		current := planmode.Load(session)
		previousMode := sessionPermissionMode(session)
		if planmode.IsActive(current) && strings.TrimSpace(current.PreviousMode) != "" {
			previousMode = current.PreviousMode
		}
		if strings.EqualFold(strings.TrimSpace(previousMode), string(runtimepolicy.ModePlan)) &&
			(!planmode.IsActive(current) || strings.TrimSpace(current.PreviousMode) == "") {
			previousMode = string(runtimepolicy.ModeDefault)
		}
		state := planmode.Enter(previousMode, planPath)
		planmode.Save(session, state)
		applySessionPermissionMode(session, runtimepolicy.ModePlan)
		return nil

	case "exit", "approve", "request_changes", "quit":
		current := planmode.Load(session)
		currentMode := sessionPermissionMode(session)
		if !planmode.IsActive(current) && current.Status != planmode.StatusExited {
			if !strings.EqualFold(strings.TrimSpace(currentMode), string(runtimepolicy.ModePlan)) {
				return errors.New(errors.ErrValidationFailed, "not in plan mode; call enter first")
			}
			current = planmode.Enter(string(runtimepolicy.ModeDefault), planmode.DefaultPlanPath)
		}
		exited, err := planmode.Exit(current, decision, notes)
		if err != nil {
			return errors.New(errors.ErrValidationFailed, err.Error())
		}
		resume := planmode.ResumeModeAfterExit(exited)
		mode := parseSessionPlanPermissionMode(resume)
		if exited.ExitDecision == planmode.ExitRequestChanges {
			exited.Status = planmode.StatusActive
			exited.PendingExitRequest = false
			planmode.Save(session, exited)
			applySessionPermissionMode(session, runtimepolicy.ModePlan)
			return nil
		}
		planmode.Save(session, exited)
		applySessionPermissionMode(session, mode)
		return nil

	default:
		return errors.New(errors.ErrValidationFailed, fmt.Sprintf("unsupported plan mode action %q", action))
	}
}

func (h *Handler) buildSessionPlanModeResponse(session *chat.Session, action string) sessionPlanModeResponse {
	state := planmode.Load(session)
	permissionMode := sessionPermissionMode(session)
	if planmode.IsActive(state) {
		permissionMode = string(runtimepolicy.ModePlan)
	} else if strings.TrimSpace(permissionMode) == "" {
		permissionMode = planmode.EffectivePermissionMode(state)
	}

	workspace := ""
	if session != nil {
		workspace = sessionmeta.String(session.Metadata.Context, sessionmeta.WorkspacePath)
		if workspace == "" {
			if raw, ok := session.GetContext(sessionmeta.WorkspacePath); ok {
				workspace = strings.TrimSpace(fmt.Sprint(raw))
			}
		}
	}

	planPath := state.PlanPath
	if planPath == "" && (planmode.IsActive(state) || strings.EqualFold(permissionMode, string(runtimepolicy.ModePlan))) {
		planPath = planmode.DefaultPlanPath
	}

	content, available, truncated, contentErr := readPlanPreviewContent(workspace, planPath)
	writeAllow := append([]string(nil), state.WriteAllowPaths...)
	if planmode.IsActive(state) && len(writeAllow) == 0 {
		writeAllow = []string{planmode.NormalizePlanPath(planPath)}
	}

	resp := sessionPlanModeResponse{
		SessionID:            "",
		Active:               planmode.IsActive(state) || strings.EqualFold(permissionMode, string(runtimepolicy.ModePlan)),
		Status:               string(state.Status),
		PlanPath:             planPath,
		WriteAllowPaths:      writeAllow,
		PreviousMode:         state.PreviousMode,
		PermissionMode:       permissionMode,
		PendingExitRequest:   state.PendingExitRequest,
		ExitDecision:         string(state.ExitDecision),
		Notes:                state.Notes,
		EnteredAt:            state.EnteredAt,
		ExitedAt:             state.ExitedAt,
		WorkspacePath:        workspace,
		PlanContent:          content,
		PlanContentAvailable: available,
		PlanContentTruncated: truncated,
		PlanContentError:     contentErr,
		Action:               strings.TrimSpace(action),
	}
	if session != nil {
		resp.SessionID = session.ID
	}
	if resp.Status == "" {
		resp.Status = string(planmode.StatusInactive)
	}
	return resp
}

func readPlanPreviewContent(workspacePath, planPath string) (content string, available bool, truncated bool, errText string) {
	planPath = strings.TrimSpace(planPath)
	if planPath == "" {
		return "", false, false, ""
	}
	absPath, err := resolvePlanPreviewPath(workspacePath, planPath)
	if err != nil {
		return "", false, false, err.Error()
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, false, ""
		}
		return "", false, false, err.Error()
	}
	if len(data) > planPreviewMaxBytes {
		data = data[:planPreviewMaxBytes]
		truncated = true
	}
	text := string(data)
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "\uFFFD")
	}
	if utf8.RuneCountInString(text) > planPreviewMaxRunes {
		runes := []rune(text)
		text = string(runes[:planPreviewMaxRunes])
		truncated = true
	}
	return text, true, truncated, ""
}

func resolvePlanPreviewPath(workspacePath, planPath string) (string, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	planPath = planmode.NormalizePlanPath(planPath)
	if workspacePath == "" {
		return "", fmt.Errorf("workspace path is not set on session")
	}
	workspaceAbs, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", fmt.Errorf("invalid workspace path: %w", err)
	}
	info, err := os.Stat(workspaceAbs)
	if err != nil {
		return "", fmt.Errorf("workspace path unavailable: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path is not a directory")
	}

	var candidate string
	if filepath.IsAbs(planPath) {
		candidate = filepath.Clean(planPath)
	} else {
		candidate = filepath.Join(workspaceAbs, filepath.Clean(planPath))
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("invalid plan path: %w", err)
	}
	if !planPathWithinWorkspace(candidateAbs, workspaceAbs) {
		return "", fmt.Errorf("plan path escapes workspace")
	}
	return candidateAbs, nil
}

func planPathWithinWorkspace(targetPath, workspacePath string) bool {
	baseAbs, err := filepath.Abs(strings.TrimSpace(workspacePath))
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(strings.TrimSpace(targetPath))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func normalizePlanModeAction(req sessionPlanModeRequest) (action string, decision planmode.ExitDecision, err error) {
	rawAction := strings.ToLower(strings.TrimSpace(req.Action))
	rawDecision := strings.TrimSpace(req.Decision)
	if rawAction == "" && rawDecision != "" {
		rawAction = "exit"
	}
	if rawAction == "" {
		return "", planmode.ExitNone, fmt.Errorf("action is required (enter|exit|approve|request_changes|quit)")
	}

	switch rawAction {
	case "enter", "on", "start":
		return "enter", planmode.ExitNone, nil
	case "status", "get":
		return "", planmode.ExitNone, fmt.Errorf("use GET for plan status")
	case "exit":
		if rawDecision == "" {
			return "", planmode.ExitNone, fmt.Errorf("exit requires decision (approve|request_changes|quit)")
		}
		normalized, normErr := planmode.NormalizeExitDecision(rawDecision)
		if normErr != nil {
			return "", planmode.ExitNone, normErr
		}
		return "exit", normalized, nil
	case "approve", "approved", "yes", "y":
		return "approve", planmode.ExitApprove, nil
	case "request_changes", "request-changes", "changes", "revise":
		return "request_changes", planmode.ExitRequestChanges, nil
	case "quit", "cancel", "abort", "off", "no", "n":
		return "quit", planmode.ExitQuit, nil
	default:
		return "", planmode.ExitNone, fmt.Errorf("unsupported action %q (enter|exit|approve|request_changes|quit)", rawAction)
	}
}

func sessionPermissionMode(session *chat.Session) string {
	if session == nil {
		return string(runtimepolicy.ModeDefault)
	}
	if text := sessionmeta.String(session.Metadata.Context, sessionmeta.PermissionMode); text != "" {
		return text
	}
	if text := sessionmeta.String(session.Metadata.Context, sessionmeta.EffectivePermissionMode); text != "" {
		return text
	}
	if raw, ok := session.GetContext(sessionmeta.PermissionMode); ok {
		if text := strings.TrimSpace(fmt.Sprint(raw)); text != "" && text != "<nil>" {
			return text
		}
	}
	return string(runtimepolicy.ModeDefault)
}

func applySessionPermissionMode(session *chat.Session, mode runtimepolicy.Mode) {
	if session == nil {
		return
	}
	text := string(mode)
	session.SetContext(sessionmeta.PermissionMode, text)
	session.SetContext(sessionmeta.RequestedPermissionMode, text)
	session.SetContext(sessionmeta.EffectivePermissionMode, text)
}

func parseSessionPlanPermissionMode(raw string) runtimepolicy.Mode {
	switch runtimepolicy.Mode(strings.ToLower(strings.TrimSpace(raw))) {
	case runtimepolicy.ModeAcceptEdits:
		return runtimepolicy.ModeAcceptEdits
	case runtimepolicy.ModePlan:
		return runtimepolicy.ModePlan
	case runtimepolicy.ModeBypassPermissions:
		return runtimepolicy.ModeBypassPermissions
	default:
		return runtimepolicy.ModeDefault
	}
}

func (h *Handler) writePlanModeError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if runtimeErr, ok := err.(*errors.RuntimeError); ok {
		switch runtimeErr.Code {
		case errors.ErrConfigInvalid:
			h.writeError(w, http.StatusServiceUnavailable, runtimeErr)
			return
		case errors.ErrValidationFailed:
			h.writeError(w, http.StatusBadRequest, runtimeErr)
			return
		}
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not found"):
		h.writeError(w, http.StatusNotFound, err)
	case strings.Contains(msg, "not in plan mode"),
		strings.Contains(msg, "exit decision"),
		strings.Contains(msg, "invalid exit"),
		strings.Contains(msg, "unsupported"),
		strings.Contains(msg, "required"),
		strings.Contains(msg, "session id"):
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
	default:
		h.writeError(w, http.StatusInternalServerError, err)
	}
}
