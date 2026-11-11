package runtimeapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type memoryStatusResponse struct {
	WorkspacePath        string         `json:"workspace_path"`
	Mode                 string         `json:"mode"`
	Scope                string         `json:"scope"`
	DBPath               string         `json:"db_path,omitempty"`
	SchemaVersion        int            `json:"schema_version"`
	Total                int            `json:"total"`
	ByStatus             map[string]int `json:"by_status,omitempty"`
	ByFreshness          map[string]int `json:"by_freshness,omitempty"`
	SummaryRevision      int64          `json:"summary_revision,omitempty"`
	SummaryStatus        string         `json:"summary_status,omitempty"`
	SummaryFreshness     string         `json:"summary_freshness,omitempty"`
	PinnedCount          int            `json:"pinned_count,omitempty"`
	PinnedBudgetRatio    float64        `json:"pinned_budget_ratio,omitempty"`
	InjectionMode        string         `json:"injection_mode,omitempty"`
	GenerationEnabled    bool           `json:"generation_enabled"`
	GenerationTrigger    string         `json:"generation_trigger,omitempty"`
	ConsolidationEnabled bool           `json:"consolidation_enabled"`
	ManualEnabled        bool           `json:"manual_enabled"`
	Jobs                 map[string]int `json:"jobs,omitempty"`
	Fallback             bool           `json:"fallback"`
	FallbackReason       string         `json:"fallback_reason,omitempty"`
}

type memoryRecordDTO struct {
	ID              string                    `json:"id"`
	Scope           string                    `json:"scope"`
	Kind            string                    `json:"kind"`
	Status          string                    `json:"status"`
	Text            string                    `json:"text"`
	Tags            []string                  `json:"tags,omitempty"`
	Confidence      float64                   `json:"confidence,omitempty"`
	Sensitivity     string                    `json:"sensitivity,omitempty"`
	Pinned          bool                      `json:"pinned"`
	RetentionClass  string                    `json:"retention_class,omitempty"`
	SourceKind      string                    `json:"source_kind"`
	SourceSessionID string                    `json:"source_session_id,omitempty"`
	SourceTurnID    string                    `json:"source_turn_id,omitempty"`
	SourceRefs      []string                  `json:"source_refs,omitempty"`
	Revision        int64                     `json:"revision"`
	CreatedAt       string                    `json:"created_at,omitempty"`
	UpdatedAt       string                    `json:"updated_at,omitempty"`
	LastUsedAt      string                    `json:"last_used_at,omitempty"`
	UseCount        int                       `json:"use_count,omitempty"`
	ValidUntil      string                    `json:"valid_until,omitempty"`
	Freshness       string                    `json:"freshness_status"`
	LastVerifiedAt  string                    `json:"last_verified_at,omitempty"`
	RevalidateAfter string                    `json:"revalidate_after,omitempty"`
	StaleReason     string                    `json:"stale_reason,omitempty"`
	Usage           []memorystore.UsageRecord `json:"usage,omitempty"`
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

type memoryExtractRequest struct {
	SessionID     string `json:"session_id"`
	WorkspacePath string `json:"workspace_path"`
	Trigger       string `json:"trigger"`
}

func (h *Handler) memoryV2Enabled() bool {
	if h == nil || h.runtimeConfig == nil {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(h.runtimeConfig.Memory.Mode))
	return mode == "shadow" || mode == "on"
}

type memoryConfirmRequest struct {
	WorkspacePath string `json:"workspace_path"`
	Confirm       bool   `json:"confirm"`
	Format        string `json:"format"`
}

type memorySummaryListResponse struct {
	WorkspacePath string                `json:"workspace_path"`
	Items         []memorystore.Summary `json:"items"`
	Count         int                   `json:"count"`
}

type memoryUsageListResponse struct {
	WorkspacePath string                    `json:"workspace_path"`
	Items         []memorystore.UsageRecord `json:"items"`
	Count         int                       `json:"count"`
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

type memoryWhyNotSelectedDTO struct {
	ID            string `json:"id"`
	Stage         string `json:"stage"`
	Kind          string `json:"kind,omitempty"`
	Pinned        bool   `json:"pinned,omitempty"`
	SourceKind    string `json:"source_kind,omitempty"`
	TokenEstimate int    `json:"token_estimate,omitempty"`
}

type memoryWhyNotResponse struct {
	WorkspacePath   string                      `json:"workspace_path"`
	Mode            string                      `json:"mode"`
	QueryPreview    string                      `json:"query_preview"`
	QueryTruncated  bool                        `json:"query_truncated"`
	SummaryRevision int64                       `json:"summary_revision,omitempty"`
	CandidateCount  int                         `json:"candidate_count"`
	FilteredCount   int                         `json:"filtered_count"`
	Selected        []memoryWhyNotSelectedDTO   `json:"selected"`
	Dropped         []memorystore.DroppedRecord `json:"dropped"`
	TokenEstimate   int                         `json:"token_estimate"`
	TokenBudget     int                         `json:"token_budget"`
	Injected        bool                        `json:"injected"`
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
	response := memoryStatusResponse{
		WorkspacePath: workspace, Mode: report.Mode, Scope: report.Scope,
		DBPath: report.DBPath, SchemaVersion: report.SchemaVersion, Total: report.Total,
		ByStatus: report.ByStatus, ByFreshness: report.ByFreshness,
		SummaryRevision: report.SummaryRevision, SummaryStatus: report.SummaryStatus,
		SummaryFreshness: report.SummaryFreshness, PinnedCount: report.PinnedCount,
		InjectionMode: report.InjectionMode, Jobs: report.Jobs,
		Fallback: report.Fallback, FallbackReason: report.FallbackReason,
	}
	if h.runtimeConfig != nil {
		response.PinnedBudgetRatio = h.runtimeConfig.Memory.Injection.PinnedBudgetRatio
		response.GenerationEnabled = h.runtimeConfig.Memory.Generation.Enabled
		response.GenerationTrigger = h.runtimeConfig.Memory.Generation.Trigger
		response.ConsolidationEnabled = h.runtimeConfig.Memory.Consolidation.Enabled
		response.ManualEnabled = h.runtimeConfig.Memory.Manual.Enabled
	}
	h.writeJSON(w, http.StatusOK, response)
}

func (h *Handler) ListMemoryRecords(w http.ResponseWriter, r *http.Request) {
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope != "" && !strings.EqualFold(scope, string(memorystore.ScopeProject)) {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "scope currently supports only project"))
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	switch memorystore.Status(strings.ToLower(status)) {
	case "", memorystore.StatusCandidate, memorystore.StatusActive,
		memorystore.StatusSuperseded, memorystore.StatusDeleted, memorystore.StatusExpired:
	default:
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid memory status"))
		return
	}
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if cursor != "" {
		offset, cursorErr := strconv.Atoi(cursor)
		if cursorErr != nil || offset < 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "cursor must be a non-negative integer"))
			return
		}
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
	limit := parsePositiveIntQuery(r, "limit", 50)
	filter := memorystore.RecordFilter{
		Scope:           memorystore.Scope(strings.TrimSpace(r.URL.Query().Get("scope"))),
		Status:          memorystore.Status(strings.ToLower(status)),
		Kind:            strings.TrimSpace(r.URL.Query().Get("kind")),
		Tag:             strings.TrimSpace(r.URL.Query().Get("tag")),
		Query:           firstNonEmpty(strings.TrimSpace(r.URL.Query().Get("q")), strings.TrimSpace(r.URL.Query().Get("query"))),
		SourceSessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
		Limit:           limit,
		Cursor:          cursor,
	}
	page, err := store.ListRecords(r.Context(), filter)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	items := make([]memoryRecordDTO, 0, len(page.Items))
	canViewPrivate := h.authorizeMemoryAdmin(r) == nil
	if strings.EqualFold(status, string(memorystore.StatusDeleted)) && !canViewPrivate {
		h.writeError(w, http.StatusForbidden, errors.New(errors.ErrAgentPermission, "deleted memory requires admin authorization"))
		return
	}
	for _, record := range page.Items {
		if !canViewPrivate && (record.Sensitivity == "private" || memoryredact.DetectSecret(record.Text) != "") {
			continue
		}
		items = append(items, memoryRecordToDTO(record))
	}
	h.writeJSON(w, http.StatusOK, memoryRecordListResponse{
		WorkspacePath: workspace, Items: items, Count: len(items), NextCursor: page.NextCursor,
	})
}

func (h *Handler) GetMemoryWhyNot(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	query := firstNonEmpty(
		strings.TrimSpace(r.URL.Query().Get("q")),
		strings.TrimSpace(r.URL.Query().Get("query")),
	)
	if query == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "q is required"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	limit := parsePositiveIntQuery(r, "limit", 8)
	if limit > 50 {
		limit = 50
	}
	budget := parsePositiveIntQuery(r, "token_budget", 0)
	if budget <= 0 && h.runtimeConfig != nil {
		budget = h.runtimeConfig.Memory.Injection.TokenBudget
	}
	if budget <= 0 {
		budget = memorystore.DefaultTokenBudget
	}
	if budget > 8000 {
		budget = 8000
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	selection, err := store.DiagnoseInjection(memorystore.InjectOptions{
		Query: query, Limit: limit, TokenBudget: budget,
		SessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
		SkipTrace: true,
	})
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	selected := make([]memoryWhyNotSelectedDTO, 0, len(selection.IDs))
	seen := make(map[string]struct{}, len(selection.IDs))
	addSelected := func(record memorystore.Record, stage string) {
		if _, ok := seen[record.ID]; ok {
			return
		}
		seen[record.ID] = struct{}{}
		selected = append(selected, memoryWhyNotSelectedDTO{
			ID: record.ID, Stage: stage, Kind: record.Kind, Pinned: record.Pinned,
			SourceKind: record.SourceKind, TokenEstimate: memorystore.EstimateTokens(record.Text),
		})
	}
	for _, record := range selection.Stable {
		addSelected(record, "stable")
	}
	for _, record := range selection.Recall {
		addSelected(record, "recall")
	}
	if selection.Summary != nil {
		for _, id := range selection.Summary.RecordIDs {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			selected = append(selected, memoryWhyNotSelectedDTO{ID: id, Stage: "summary"})
		}
	}
	preview, _ := memoryredact.Redact(query)
	preview = strings.TrimSpace(preview)
	truncated := false
	if runes := []rune(preview); len(runes) > 160 {
		preview = string(runes[:160]) + "…"
		truncated = true
	}
	dropped := selection.Dropped
	if dropped == nil {
		dropped = []memorystore.DroppedRecord{}
	}
	h.writeJSON(w, http.StatusOK, memoryWhyNotResponse{
		WorkspacePath: workspace, Mode: store.Mode(), QueryPreview: preview,
		QueryTruncated: truncated, SummaryRevision: selection.Revision,
		CandidateCount: selection.CandidateCount, FilteredCount: selection.FilteredCount,
		Selected: selected, Dropped: dropped, TokenEstimate: selection.TokenEstimate,
		TokenBudget: budget, Injected: len(selected) > 0,
	})
}

func (h *Handler) CreateMemoryRecord(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
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
		Sensitivity: "internal", SourceKind: firstNonEmpty(req.Source, "manual"), SourceSessionID: req.SessionID,
		Revision: 1, CreatedAt: note.CreatedAt.UTC().Format(time.RFC3339),
		Freshness: "fresh",
	})
}

func (h *Handler) GetMemoryRecord(w http.ResponseWriter, r *http.Request) {
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "memory record not found"))
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
	record, err := store.GetRecord(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "memory record not found"))
		return
	}
	if h.authorizeMemoryAdmin(r) != nil &&
		(record.Sensitivity == "private" || record.Status == memorystore.StatusDeleted || memoryredact.DetectSecret(record.Text) != "") {
		h.writeError(w, http.StatusForbidden, errors.New(errors.ErrAgentPermission, "private/deleted memory requires admin authorization"))
		return
	}
	dto := memoryRecordToDTO(record)
	if usage, usageErr := store.ListUsage(r.Context(), memorystore.UsageFilter{MemoryID: record.ID, Limit: 20}); usageErr == nil {
		dto.Usage = usage
	}
	h.writeJSON(w, http.StatusOK, dto)
}

func (h *Handler) PatchMemoryRecord(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
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
	recordID := mux.Vars(r)["id"]
	patch := memorystore.RecordPatch{Tags: req.Tags, Pinned: req.Pinned}
	if req.Status != nil {
		status := memorystore.Status(strings.TrimSpace(*req.Status))
		patch.Status = &status
	}
	var record memorystore.Record
	if req.Text != nil {
		record, err = store.CorrectRecord(r.Context(), recordID, *req.Text)
		if err == nil && (req.Tags != nil || req.Pinned != nil || req.Status != nil) {
			record, err = store.UpdateRecord(r.Context(), record.ID, patch)
		}
	} else {
		record, err = store.UpdateRecord(r.Context(), recordID, patch)
	}
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
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
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
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
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
	canViewPrivate := h.authorizeMemoryAdmin(r) == nil
	for _, record := range page.Items {
		if !canViewPrivate && (record.Sensitivity == "private" || memoryredact.DetectSecret(record.Text) != "") {
			continue
		}
		items = append(items, memoryRecordToDTO(record))
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id": mux.Vars(r)["id"], "items": items, "count": len(items),
	})
}

func (h *Handler) ListMemoryRecallTraces(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeJSON(w, http.StatusOK, map[string]interface{}{"items": []memoryRecallTraceDTO{}, "count": 0})
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

func (h *Handler) ExtractMemory(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	var req memoryExtractRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	if _, err := h.resolveMemoryWorkspace(r, req.WorkspacePath); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	rt := h.MemoryRuntime()
	if rt == nil {
		var err error
		rt, err = h.EnableMemoryRuntime()
		if err != nil {
			h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrValidationFailed, err.Error()))
			return
		}
	}
	if rt == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrValidationFailed, "memory runtime is not enabled"))
		return
	}
	trigger := memoryruntime.Trigger(strings.ToLower(strings.TrimSpace(req.Trigger)))
	if trigger == "" {
		trigger = memoryruntime.TriggerManual
	}
	if trigger != memoryruntime.TriggerManual && trigger != memoryruntime.TriggerSessionEnd {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "trigger must be manual or session_end"))
		return
	}
	result, err := rt.RunSession(r.Context(), trigger, req.SessionID)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"trigger": result.Trigger, "sessions": result.Sessions, "processed": result.Processed,
		"skipped": result.Skipped, "candidates": result.Candidates, "stale": result.Stale,
		"failed": result.Failed, "ignored": result.Ignored, "errors": result.Errors,
	})
}

func (h *Handler) ListMemoryJobs(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeJSON(w, http.StatusOK, map[string]interface{}{"items": []memorystore.MemoryJob{}, "count": 0})
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
	jobs, err := store.ListJobs(r.Context(), parsePositiveIntQuery(r, "limit", 50))
	if err != nil {
		if store.Mode() == "off" {
			jobs = nil
		} else {
			h.writeError(w, http.StatusServiceUnavailable, err)
			return
		}
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"items": jobs, "count": len(jobs)})
}

func (h *Handler) ListMemorySummaries(w http.ResponseWriter, r *http.Request) {
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
	if !h.memoryV2Enabled() {
		h.writeJSON(w, http.StatusOK, memorySummaryListResponse{
			WorkspacePath: workspace, Items: []memorystore.Summary{}, Count: 0,
		})
		return
	}
	limit := parsePositiveIntQuery(r, "limit", 20)
	if limit > 100 {
		limit = 100
	}
	summaries, err := store.ListSummaries(r.Context(), limit)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	if h.authorizeMemoryAdmin(r) != nil {
		filtered := make([]memorystore.Summary, 0, len(summaries))
		for _, summary := range summaries {
			if memoryredact.DetectSecret(summary.Text) != "" {
				continue
			}
			filtered = append(filtered, summary)
		}
		summaries = filtered
	}
	h.writeJSON(w, http.StatusOK, memorySummaryListResponse{
		WorkspacePath: workspace, Items: summaries, Count: len(summaries),
	})
}

func (h *Handler) ConsolidateMemory(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
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
	result, err := h.RunMemoryConsolidationForWorkspace(r.Context(), workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"workspace_path": workspace, "promoted": result.Promoted,
		"superseded": result.Superseded, "rebuilt": result.Rebuilt,
		"rebuild_jobs": result.RebuildJobs, "open_questions": result.OpenQuestions,
		"summary_id": result.SummaryID,
		"revision":   result.Revision, "token_count": result.TokenCount,
	})
}

func (h *Handler) RebuildMemorySummaries(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
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
	result, err := h.RebuildMemorySummariesForWorkspace(r.Context(), workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"workspace_path": workspace, "rebuilt": result.Rebuilt,
		"rebuild_jobs": result.RebuildJobs, "summary_id": result.SummaryID,
		"revision": result.Revision, "token_count": result.TokenCount,
	})
}

func (h *Handler) ListMemoryUsage(w http.ResponseWriter, r *http.Request) {
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
	if !h.memoryV2Enabled() {
		h.writeJSON(w, http.StatusOK, memoryUsageListResponse{
			WorkspacePath: workspace, Items: []memorystore.UsageRecord{}, Count: 0,
		})
		return
	}
	usage, err := store.ListUsage(r.Context(), memorystore.UsageFilter{
		MemoryID:  strings.TrimSpace(r.URL.Query().Get("memory_id")),
		SessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
		TurnID:    strings.TrimSpace(r.URL.Query().Get("turn_id")),
		UseKind:   memorystore.UsageKind(strings.TrimSpace(r.URL.Query().Get("use_kind"))),
		Limit:     parsePositiveIntQuery(r, "limit", 50),
	})
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	h.writeJSON(w, http.StatusOK, memoryUsageListResponse{
		WorkspacePath: workspace, Items: usage, Count: len(usage),
	})
}

func (h *Handler) GetMemoryMetrics(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeJSON(w, http.StatusOK, memorystore.MemoryMetrics{})
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
	metrics, err := store.Metrics(r.Context())
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	h.writeJSON(w, http.StatusOK, metrics)
}

func (h *Handler) RestoreMemoryRecord(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
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
	record, err := store.RestoreRecord(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, memoryRecordToDTO(record))
}

func (h *Handler) RevalidateMemoryRecord(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
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
	record, err := store.RevalidateRecord(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, memoryRecordToDTO(record))
}

func (h *Handler) ActivateMemorySummary(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
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
	summary, err := store.ActivateSummary(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, summary)
}

func (h *Handler) GetMemoryFreshness(w http.ResponseWriter, r *http.Request) {
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
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	page, err := store.ListRecords(r.Context(), memorystore.RecordFilter{Status: memorystore.StatusActive, Limit: 200})
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	items := make([]memoryRecordDTO, 0)
	canViewPrivate := h.authorizeMemoryAdmin(r) == nil
	for _, record := range page.Items {
		if record.Freshness != memorystore.FreshnessFresh {
			if !canViewPrivate && (record.Sensitivity == "private" || memoryredact.DetectSecret(record.Text) != "") {
				continue
			}
			items = append(items, memoryRecordToDTO(record))
		}
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"workspace_path": workspace, "by_freshness": report.ByFreshness,
		"items": items, "count": len(items),
	})
}

func (h *Handler) SweepMemoryFreshness(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	var req memoryConfirmRequest
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
	count, err := store.SweepFreshness(r.Context(), time.Now().UTC())
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"workspace_path": workspace, "stale": count})
}

func (h *Handler) ListMemoryAudit(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeJSON(w, http.StatusOK, map[string]interface{}{"items": []memorystore.AuditEvent{}, "count": 0})
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
	events, err := store.ListEvents(r.Context(), parsePositiveIntQuery(r, "limit", 100))
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"workspace_path": workspace, "items": events, "count": len(events)})
}

func (h *Handler) ExportMemory(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		w.Header().Set("Content-Type", "application/jsonl")
		w.Header().Set("Content-Disposition", `attachment; filename="agent-memory.jsonl"`)
		w.WriteHeader(http.StatusOK)
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
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	var body string
	contentType := "application/jsonl"
	switch format {
	case "", "jsonl":
		body, err = store.ExportJSONL(r.Context())
	case "markdown", "md":
		body, err = store.ExportMarkdown(r.Context())
		contentType = "text/markdown"
	default:
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "format must be jsonl or markdown"))
		return
	}
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	filename := "agent-memory.jsonl"
	if contentType == "text/markdown" {
		filename = "agent-memory.md"
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func (h *Handler) ImportMemory(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
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
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to read import body"))
		return
	}
	dryRun := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("dry_run")), "true") ||
		strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("dryRun")), "true")
	result, err := store.ImportJSONL(r.Context(), body, dryRun)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func (h *Handler) ResetMemory(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	var req memoryConfirmRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	if !req.Confirm {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "reset requires confirm=true"))
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
	if err := store.Reset(r.Context()); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "action": "reset", "workspace_path": workspace})
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
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if workspacePathsEqual(left, right) {
		return true
	}
	leftReal, leftErr := filepath.EvalSymlinks(left)
	rightReal, rightErr := filepath.EvalSymlinks(right)
	return leftErr == nil && rightErr == nil && workspacePathsEqual(filepath.Clean(leftReal), filepath.Clean(rightReal))
}

func workspacePathsEqual(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func (h *Handler) openMemoryStore(workspace string) (*memorystore.Store, error) {
	cfg := memorystore.Config{ProjectRoot: workspace}
	if h != nil && h.runtimeConfig != nil {
		cfg.Mode = h.runtimeConfig.Memory.Mode
		cfg.StorePath = h.runtimeConfig.Memory.StorePath
		cfg.InjectionMode = h.runtimeConfig.Memory.Injection.Mode
		cfg.PinnedBudgetRatio = h.runtimeConfig.Memory.Injection.PinnedBudgetRatio
		cfg.DisableStageSplit = !h.runtimeConfig.Memory.Injection.StageSplit
		cfg.DisableInjection = !h.runtimeConfig.Memory.Injection.Enabled
		cfg.DisableManual = !h.runtimeConfig.Memory.Manual.Enabled
		cfg.RecallTraceEnabled = h.runtimeConfig.Memory.Injection.RecallTrace.Enabled
		cfg.RecallTraceSampleRate = h.runtimeConfig.Memory.Injection.RecallTrace.SampleRate
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
		Sensitivity: record.Sensitivity,
		Pinned:      record.Pinned, RetentionClass: record.RetentionClass, SourceKind: record.SourceKind,
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
