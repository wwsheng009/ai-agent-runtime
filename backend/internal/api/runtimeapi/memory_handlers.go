package runtimeapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"

	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type memoryStatusResponse struct {
	WorkspacePath    string         `json:"workspace_path"`
	Mode             string         `json:"mode"`
	Scope            string         `json:"scope"`
	DBPath           string         `json:"db_path,omitempty"`
	SchemaVersion    int            `json:"schema_version"`
	Total            int            `json:"total"`
	ByStatus         map[string]int `json:"by_status,omitempty"`
	ByFreshness      map[string]int `json:"by_freshness,omitempty"`
	SummaryRevision  int64          `json:"summary_revision,omitempty"`
	SummaryStatus    string         `json:"summary_status,omitempty"`
	SummaryFreshness string         `json:"summary_freshness,omitempty"`
	Jobs             map[string]int `json:"jobs,omitempty"`
	Fallback         bool           `json:"fallback"`
	FallbackReason   string         `json:"fallback_reason,omitempty"`
}

type memoryRecordDTO struct {
	ID              string   `json:"id"`
	Scope           string   `json:"scope"`
	Kind            string   `json:"kind"`
	Status          string   `json:"status"`
	Text            string   `json:"text"`
	Tags            []string `json:"tags,omitempty"`
	Confidence      float64  `json:"confidence,omitempty"`
	Pinned          bool     `json:"pinned"`
	RetentionClass  string   `json:"retention_class,omitempty"`
	SourceKind      string   `json:"source_kind"`
	SourceSessionID string   `json:"source_session_id,omitempty"`
	SourceTurnID    string   `json:"source_turn_id,omitempty"`
	SourceRefs      []string `json:"source_refs,omitempty"`
	Revision        int64    `json:"revision"`
	CreatedAt       string   `json:"created_at,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
	LastUsedAt      string   `json:"last_used_at,omitempty"`
	UseCount        int      `json:"use_count,omitempty"`
	ValidUntil      string   `json:"valid_until,omitempty"`
	Freshness       string   `json:"freshness_status"`
	LastVerifiedAt  string   `json:"last_verified_at,omitempty"`
	RevalidateAfter string   `json:"revalidate_after,omitempty"`
	StaleReason     string   `json:"stale_reason,omitempty"`
}

type memoryRecordListResponse struct {
	WorkspacePath string            `json:"workspace_path"`
	Items         []memoryRecordDTO `json:"items"`
	Count         int               `json:"count"`
	NextCursor    string            `json:"next_cursor,omitempty"`
}

type memoryRecordCreateRequest struct {
	WorkspacePath string   `json:"workspace_path"`
	Text          string   `json:"text"`
	Kind          string   `json:"kind"`
	Tags          []string `json:"tags"`
	Pinned        *bool    `json:"pinned"`
	Source        string   `json:"source"`
	SessionID     string   `json:"session_id"`
}

type memoryRecordPatchRequest struct {
	WorkspacePath string    `json:"workspace_path"`
	Text          *string   `json:"text"`
	Tags          *[]string `json:"tags"`
	Pinned        *bool     `json:"pinned"`
	Status        *string   `json:"status"`
}

type memorySessionModeRequest struct {
	WorkspacePath string `json:"workspace_path"`
	Mode          string `json:"mode"`
}

type memoryRecallTraceDTO struct {
	ID              string                      `json:"id"`
	SessionID       string                      `json:"session_id"`
	TurnID          string                      `json:"turn_id,omitempty"`
	ProjectKey      string                      `json:"project_key"`
	QueryHash       string                      `json:"query_hash"`
	QueryPreview    string                      `json:"query_preview,omitempty"`
	SummaryRevision int64                       `json:"summary_revision,omitempty"`
	CandidateCount  int                         `json:"candidate_count"`
	FilteredCount   int                         `json:"filtered_count"`
	SelectedIDs     []string                    `json:"selected_ids,omitempty"`
	Dropped         []memorystore.DroppedRecord `json:"dropped,omitempty"`
	TokenEstimate   int                         `json:"token_estimate"`
	TokenBudget     int                         `json:"token_budget"`
	BudgetSource    string                      `json:"budget_source,omitempty"`
	Injected        bool                        `json:"injected"`
	CreatedAt       string                      `json:"created_at,omitempty"`
}

func (h *Handler) GetMemoryStatus(w http.ResponseWriter, r *http.Request) {
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	report, err := store.Status(r.Context())
	if err != nil && !report.Fallback {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, memoryStatusResponse{
		WorkspacePath: workspace, Mode: report.Mode, Scope: report.Scope,
		DBPath: report.DBPath, SchemaVersion: report.SchemaVersion, Total: report.Total,
		ByStatus: report.ByStatus, ByFreshness: report.ByFreshness,
		SummaryRevision: report.SummaryRevision, SummaryStatus: report.SummaryStatus,
		SummaryFreshness: report.SummaryFreshness, Jobs: report.Jobs,
		Fallback: report.Fallback, FallbackReason: report.FallbackReason,
	})
}

func (h *Handler) ListMemoryRecords(w http.ResponseWriter, r *http.Request) {
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	limit := parsePositiveIntQuery(r, "limit", 50)
	filter := memorystore.RecordFilter{
		Scope:           memorystore.Scope(strings.TrimSpace(r.URL.Query().Get("scope"))),
		Status:          memorystore.Status(strings.TrimSpace(r.URL.Query().Get("status"))),
		Kind:            strings.TrimSpace(r.URL.Query().Get("kind")),
		Tag:             strings.TrimSpace(r.URL.Query().Get("tag")),
		Query:           firstNonEmpty(strings.TrimSpace(r.URL.Query().Get("q")), strings.TrimSpace(r.URL.Query().Get("query"))),
		SourceSessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
		Limit:           limit,
		Cursor:          strings.TrimSpace(r.URL.Query().Get("cursor")),
	}
	page, err := store.ListRecords(r.Context(), filter)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	items := make([]memoryRecordDTO, 0, len(page.Items))
	for _, record := range page.Items {
		items = append(items, memoryRecordToDTO(record))
	}
	h.writeJSON(w, http.StatusOK, memoryRecordListResponse{
		WorkspacePath: workspace, Items: items, Count: page.Count, NextCursor: page.NextCursor,
	})
}

func (h *Handler) CreateMemoryRecord(w http.ResponseWriter, r *http.Request) {
	var req memoryRecordCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, req.WorkspacePath)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	note, err := store.Append(memorystore.AppendNoteOptions{
		Text: req.Text, Tags: req.Tags, Kind: req.Kind, Pinned: req.Pinned,
		Source: req.Source, SessionID: req.SessionID,
	})
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	if record, err := store.GetRecord(r.Context(), note.ID); err == nil {
		h.writeJSON(w, http.StatusOK, memoryRecordToDTO(record))
		return
	}
	h.writeJSON(w, http.StatusOK, memoryRecordDTO{
		ID: note.ID, Scope: "project", Kind: firstNonEmpty(req.Kind, "reference"),
		Status: "active", Text: note.Text, Tags: note.Tags, Pinned: req.Pinned == nil || *req.Pinned,
		SourceKind: firstNonEmpty(req.Source, "manual"), SourceSessionID: req.SessionID,
		Revision: 1, CreatedAt: note.CreatedAt.UTC().Format(time.RFC3339),
		Freshness: "fresh",
	})
}

func (h *Handler) GetMemoryRecord(w http.ResponseWriter, r *http.Request) {
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	record, err := store.GetRecord(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "memory record not found"))
		return
	}
	h.writeJSON(w, http.StatusOK, memoryRecordToDTO(record))
}

func (h *Handler) PatchMemoryRecord(w http.ResponseWriter, r *http.Request) {
	var req memoryRecordPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, req.WorkspacePath)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	patch := memorystore.RecordPatch{Text: req.Text, Tags: req.Tags, Pinned: req.Pinned}
	if req.Status != nil {
		status := memorystore.Status(strings.TrimSpace(*req.Status))
		patch.Status = &status
	}
	record, err := store.UpdateRecord(r.Context(), mux.Vars(r)["id"], patch)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, memoryRecordToDTO(record))
}

func (h *Handler) DeleteMemoryRecord(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	if err := store.DeleteRecord(r.Context(), mux.Vars(r)["id"]); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "action": "delete"})
}

func (h *Handler) SetMemorySessionMode(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	var req memorySessionModeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, req.WorkspacePath)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	mode := memorystore.SessionMode(strings.ToLower(strings.TrimSpace(req.Mode)))
	if err := store.SetSessionMode(r.Context(), mux.Vars(r)["id"], mode, ""); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "session_id": mux.Vars(r)["id"], "mode": mode})
}

func (h *Handler) GetMemorySessionContext(w http.ResponseWriter, r *http.Request) {
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	page, err := store.ListRecords(r.Context(), memorystore.RecordFilter{
		SourceSessionID: mux.Vars(r)["id"], Limit: 50,
	})
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	items := make([]memoryRecordDTO, 0, len(page.Items))
	for _, record := range page.Items {
		items = append(items, memoryRecordToDTO(record))
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id": mux.Vars(r)["id"], "items": items, "count": len(items),
	})
}

func (h *Handler) ListMemoryRecallTraces(w http.ResponseWriter, r *http.Request) {
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	traces, err := store.ListRecallTraces(r.Context(), strings.TrimSpace(r.URL.Query().Get("session_id")), parsePositiveIntQuery(r, "limit", 50))
	if err != nil {
		if store.Mode() == "off" {
			traces = nil
		} else {
			h.writeError(w, http.StatusServiceUnavailable, err)
			return
		}
	}
	items := make([]memoryRecallTraceDTO, 0, len(traces))
	for _, trace := range traces {
		items = append(items, memoryRecallTraceDTO{
			ID: trace.ID, SessionID: trace.SessionID, TurnID: trace.TurnID,
			ProjectKey: trace.ProjectKey, QueryHash: trace.QueryHash, QueryPreview: trace.QueryPreview,
			SummaryRevision: trace.SummaryRevision, CandidateCount: trace.CandidateCount,
			FilteredCount: trace.FilteredCount, SelectedIDs: trace.SelectedIDs, Dropped: trace.Dropped,
			TokenEstimate: trace.TokenEstimate, TokenBudget: trace.TokenBudget,
			BudgetSource: trace.BudgetSource, Injected: trace.Injected,
			CreatedAt: formatMemoryTime(trace.CreatedAt),
		})
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"items": items, "count": len(items)})
}

func (h *Handler) resolveMemoryWorkspace(r *http.Request, bodyWorkspace string) (string, error) {
	requested := strings.TrimSpace(bodyWorkspace)
	if r != nil {
		requested = firstNonEmpty(requested, strings.TrimSpace(r.URL.Query().Get("workspace_path")))
	}
	configured := ""
	if h != nil && h.runtimeConfig != nil {
		configured = strings.TrimSpace(h.runtimeConfig.Workspace.Root)
	}
	if configured == "" {
		if cwd, err := os.Getwd(); err == nil {
			configured = cwd
		}
	}
	configuredAbs, err := filepath.Abs(configured)
	if err != nil {
		return "", err
	}
	configuredAbs = filepath.Clean(configuredAbs)
	if requested == "" {
		return configuredAbs, nil
	}
	requestedAbs, err := filepath.Abs(requested)
	if err != nil {
		return "", err
	}
	requestedAbs = filepath.Clean(requestedAbs)
	if !sameWorkspacePath(configuredAbs, requestedAbs) {
		return "", fmt.Errorf("workspace_path does not match the active runtime workspace")
	}
	return configuredAbs, nil
}

func sameWorkspacePath(left, right string) bool {
	if strings.EqualFold(filepath.Clean(left), filepath.Clean(right)) {
		return true
	}
	leftReal, leftErr := filepath.EvalSymlinks(left)
	rightReal, rightErr := filepath.EvalSymlinks(right)
	return leftErr == nil && rightErr == nil && strings.EqualFold(filepath.Clean(leftReal), filepath.Clean(rightReal))
}

func (h *Handler) openMemoryStore(workspace string) (*memorystore.Store, error) {
	cfg := memorystore.Config{ProjectRoot: workspace}
	if h != nil && h.runtimeConfig != nil {
		cfg.Mode = h.runtimeConfig.Memory.Mode
		cfg.StorePath = h.runtimeConfig.Memory.StorePath
		cfg.InjectionMode = h.runtimeConfig.Memory.Injection.Mode
		cfg.PinnedBudgetRatio = h.runtimeConfig.Memory.Injection.PinnedBudgetRatio
		cfg.DisableInjection = !h.runtimeConfig.Memory.Injection.Enabled
		cfg.DisableManual = !h.runtimeConfig.Memory.Manual.Enabled
	}
	return memorystore.New(cfg)
}

func (h *Handler) authorizeMemoryAdmin(r *http.Request) error {
	if h.hasValidSearchAdminToken(r) || h.hasTrustedAdminRole(r) || isLoopbackRequest(r) {
		return nil
	}
	return errors.New(errors.ErrAgentPermission, "memory admin endpoints require loopback access, valid admin token, or admin role")
}

func memoryRecordToDTO(record memorystore.Record) memoryRecordDTO {
	return memoryRecordDTO{
		ID: record.ID, Scope: string(record.Scope), Kind: record.Kind, Status: string(record.Status),
		Text: record.Text, Tags: append([]string(nil), record.Tags...), Confidence: record.Confidence,
		Pinned: record.Pinned, RetentionClass: record.RetentionClass, SourceKind: record.SourceKind,
		SourceSessionID: record.SourceSessionID, SourceTurnID: record.SourceTurnID,
		SourceRefs: append([]string(nil), record.SourceRefs...), Revision: record.Revision,
		CreatedAt: formatMemoryTime(record.CreatedAt), UpdatedAt: formatMemoryTime(record.UpdatedAt),
		LastUsedAt: formatMemoryTime(record.LastUsedAt), UseCount: record.UseCount,
		ValidUntil: formatMemoryTime(record.ValidUntil), Freshness: string(record.Freshness),
		LastVerifiedAt:  formatMemoryTime(record.LastVerifiedAt),
		RevalidateAfter: formatMemoryTime(record.RevalidateAfter), StaleReason: record.StaleReason,
	}
}

func formatMemoryTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
