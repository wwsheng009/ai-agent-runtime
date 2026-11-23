package runtimeapi

import (
	"context"
	"database/sql"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorypolicy"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type memoryStatusResponse struct {
	WorkspacePath                 string                      `json:"workspace_path"`
	Mode                          string                      `json:"mode"`
	Scope                         string                      `json:"scope"`
	DBPath                        string                      `json:"db_path,omitempty"`
	SchemaVersion                 int                         `json:"schema_version"`
	Total                         int                         `json:"total"`
	ManualCount                   int                         `json:"manual_count"`
	ByStatus                      map[string]int              `json:"by_status,omitempty"`
	ByFreshness                   map[string]int              `json:"by_freshness,omitempty"`
	SummaryRevision               int64                       `json:"summary_revision,omitempty"`
	SummaryStatus                 string                      `json:"summary_status,omitempty"`
	SummaryFreshness              string                      `json:"summary_freshness,omitempty"`
	PinnedCount                   int                         `json:"pinned_count,omitempty"`
	PinnedBudgetRatio             float64                     `json:"pinned_budget_ratio,omitempty"`
	InjectionMode                 string                      `json:"injection_mode,omitempty"`
	GenerationEnabled             bool                        `json:"generation_enabled"`
	GenerationTrigger             string                      `json:"generation_trigger,omitempty"`
	ConsolidationEnabled          bool                        `json:"consolidation_enabled"`
	ManualEnabled                 bool                        `json:"manual_enabled"`
	TurnDigestEnabled             bool                        `json:"turn_digest_enabled"`
	MemoryToolsEnabled            bool                        `json:"memory_tools_enabled"`
	MemoryToolsReason             string                      `json:"memory_tools_reason,omitempty"`
	CaptureExtractionEnabled      bool                        `json:"capture_extraction_enabled"`
	JobDeadlineRepairEnabled      bool                        `json:"job_deadline_repair_enabled"`
	Jobs                          map[string]int              `json:"jobs,omitempty"`
	JobsByKind                    map[string]map[string]int   `json:"jobs_by_kind,omitempty"`
	TurnDigests                   map[string]int              `json:"turn_digests"`
	TurnWindows                   map[string]int              `json:"turn_windows"`
	OldestPendingDigestAgeSeconds int64                       `json:"oldest_pending_digest_age_seconds,omitempty"`
	PendingDigestSessions         int                         `json:"pending_digest_sessions,omitempty"`
	PendingDigestBacklogBatches   int                         `json:"pending_digest_backlog_batches,omitempty"`
	RecoveredDigestSessions       int64                       `json:"recovered_digest_sessions,omitempty"`
	Fallback                      bool                        `json:"fallback"`
	FallbackReason                string                      `json:"fallback_reason,omitempty"`
	SearchBackend                 string                      `json:"search_backend,omitempty"`
	ExtractionJobCounts           map[string]int              `json:"extraction_job_counts,omitempty"`
	PendingExtractionJobs         int                         `json:"pending_extraction_jobs,omitempty"`
	PendingExtractionSessions     int                         `json:"pending_extraction_sessions,omitempty"`
	OldestPendingJobAgeSeconds    int64                       `json:"oldest_pending_job_age_seconds,omitempty"`
	DeadLetterJobs                int                         `json:"dead_letter_jobs,omitempty"`
	MissingDeadlineJobs           int                         `json:"missing_deadline_jobs,omitempty"`
	LastExtractionAt              string                      `json:"last_extraction_at,omitempty"`
	LastMaintenanceAt             string                      `json:"last_maintenance_at,omitempty"`
	LastSkipReason                string                      `json:"last_skip_reason,omitempty"`
	LastMaintenanceStage          string                      `json:"last_maintenance_stage,omitempty"`
	LastMaintenanceResult         string                      `json:"last_maintenance_result,omitempty"`
	MemoryRuntimePresent          bool                        `json:"memory_runtime_present"`
	MemoryRuntimeState            string                      `json:"memory_runtime_state"`
	MemoryRuntimeReason           string                      `json:"memory_runtime_reason,omitempty"`
	MemoryRuntimeError            string                      `json:"memory_runtime_error,omitempty"`
	MemoryRuntimeWorkspace        string                      `json:"memory_runtime_workspace,omitempty"`
	DigestRuntime                 *memoryruntime.DigestStats  `json:"digest_runtime,omitempty"`
	ExtractionJobs                []memorystore.JobDiagnostic `json:"extraction_jobs,omitempty"`
}

type memoryRecordDTO struct {
	ID              string                    `json:"id"`
	Scope           string                    `json:"scope"`
	Kind            string                    `json:"kind"`
	Status          string                    `json:"status"`
	Text            string                    `json:"text"`
	Title           string                    `json:"title,omitempty"`
	Summary         string                    `json:"summary,omitempty"`
	CanonicalID     string                    `json:"canonical_id,omitempty"`
	SupersededBy    string                    `json:"superseded_by,omitempty"`
	Archived        bool                      `json:"archived"`
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
	ValidFrom       string                    `json:"valid_from,omitempty"`
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
	AsOf          string            `json:"as_of,omitempty"`
	SearchBackend string            `json:"search_backend,omitempty"`
}

type memoryRecordVersionDTO struct {
	ID            string          `json:"id"`
	MemoryID      string          `json:"memory_id"`
	Version       int64           `json:"version"`
	ScopeKind     string          `json:"scope_kind"`
	ScopeKey      string          `json:"scope_key,omitempty"`
	Record        memoryRecordDTO `json:"record"`
	ChangedBy     string          `json:"changed_by"`
	ChangeReason  string          `json:"change_reason"`
	ChangeType    string          `json:"change_type"`
	SourceEventID string          `json:"source_event_id,omitempty"`
	ValidFrom     string          `json:"valid_from,omitempty"`
	ValidUntil    string          `json:"valid_until,omitempty"`
	CreatedAt     string          `json:"created_at,omitempty"`
}

type memoryRecordVersionsResponse struct {
	WorkspacePath string                   `json:"workspace_path"`
	MemoryID      string                   `json:"memory_id"`
	Items         []memoryRecordVersionDTO `json:"items"`
	Count         int                      `json:"count"`
	HasMore       bool                     `json:"has_more,omitempty"`
}

type memoryRecordRollbackRequest struct {
	WorkspacePath    string `json:"workspace_path"`
	TargetVersion    int64  `json:"target_version"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
	RevertSupersede  bool   `json:"revert_supersede"`
}

type memoryRecordCreateRequest struct {
	WorkspacePath string   `json:"workspace_path"`
	Text          string   `json:"text"`
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	CanonicalID   string   `json:"canonical_id"`
	Kind          string   `json:"kind"`
	Tags          []string `json:"tags"`
	Pinned        *bool    `json:"pinned"`
	Source        string   `json:"source"`
	SessionID     string   `json:"session_id"`
}

type memoryRecordPatchRequest struct {
	WorkspacePath string    `json:"workspace_path"`
	Text          *string   `json:"text"`
	Title         *string   `json:"title"`
	Summary       *string   `json:"summary"`
	CanonicalID   *string   `json:"canonical_id"`
	SupersededBy  *string   `json:"superseded_by"`
	Archived      *bool     `json:"archived"`
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

type memoryTurnWindowPurgeRequest struct {
	WorkspacePath string `json:"workspace_path"`
	Confirm       bool   `json:"confirm"`
	SessionID     string `json:"session_id"`
	All           bool   `json:"all"`
}

type memorySummaryListResponse struct {
	WorkspacePath string                `json:"workspace_path"`
	Items         []memorystore.Summary `json:"items"`
	Count         int                   `json:"count"`
}

type memoryCoreResponse struct {
	WorkspacePath string                  `json:"workspace_path"`
	Scope         string                  `json:"scope"`
	Status        string                  `json:"status"`
	Core          *memorystore.CoreMemory `json:"core,omitempty"`
}

type memoryCoreListResponse struct {
	WorkspacePath string                   `json:"workspace_path"`
	Scope         string                   `json:"scope"`
	Status        string                   `json:"status"`
	Items         []memorystore.CoreMemory `json:"items"`
	Count         int                      `json:"count"`
}

type memoryCoreRebuildResponse struct {
	WorkspacePath string                  `json:"workspace_path"`
	JobID         string                  `json:"job_id"`
	JobStatus     memorystore.JobStatus   `json:"job_status"`
	SourceCount   int                     `json:"source_count"`
	UsageRows     int                     `json:"usage_rows"`
	Skipped       string                  `json:"skipped,omitempty"`
	Candidate     *memorystore.CoreMemory `json:"candidate,omitempty"`
}

type memoryCoreArchiveRequest struct {
	Reason string `json:"reason"`
}

type memoryUsageListResponse struct {
	WorkspacePath string                    `json:"workspace_path"`
	Items         []memorystore.UsageRecord `json:"items"`
	Count         int                       `json:"count"`
}

type memoryCoreUsageListResponse struct {
	WorkspacePath string                        `json:"workspace_path"`
	Items         []memorystore.CoreUsageRecord `json:"items"`
	Count         int                           `json:"count"`
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
	CategoryBudget  map[string]int              `json:"category_budget,omitempty"`
	CategoryUsed    map[string]int              `json:"category_used,omitempty"`
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
		ManualCount: report.ManualCount,
		ByStatus:    report.ByStatus, ByFreshness: report.ByFreshness,
		SummaryRevision: report.SummaryRevision, SummaryStatus: report.SummaryStatus,
		SummaryFreshness: report.SummaryFreshness, PinnedCount: report.PinnedCount,
		InjectionMode: report.InjectionMode, Jobs: report.Jobs, JobsByKind: report.JobsByKind, TurnDigests: report.TurnDigests,
		TurnWindows:                   report.TurnWindows,
		OldestPendingDigestAgeSeconds: report.OldestPendingDigestAgeSeconds,
		PendingDigestSessions:         report.PendingDigestSessions,
		PendingDigestBacklogBatches:   report.PendingDigestBacklogBatches,
		RecoveredDigestSessions:       report.RecoveredDigestSessions,
		SearchBackend:                 report.SearchBackend,
		ExtractionJobCounts:           report.ExtractionJobCounts,
		PendingExtractionJobs:         report.PendingExtractionJobs,
		PendingExtractionSessions:     report.PendingExtractionSessions,
		OldestPendingJobAgeSeconds:    report.OldestPendingJobAgeSeconds,
		DeadLetterJobs:                report.DeadLetterJobs,
		MissingDeadlineJobs:           report.MissingDeadlineJobs,
		LastExtractionAt:              report.LastExtractionAt,
		ExtractionJobs:                report.ExtractionJobs,
		Fallback:                      report.Fallback, FallbackReason: report.FallbackReason,
	}
	configuredMode := ""
	if h.runtimeConfig != nil {
		configuredMode = strings.ToLower(strings.TrimSpace(h.runtimeConfig.Memory.Mode))
	}
	runtimeErr := h.MemoryRuntimeBootError()
	switch configuredMode {
	case "", "off":
		response.MemoryRuntimeState = "off"
		response.MemoryRuntimeReason = "mode_off"
	case "shadow", "on":
		if runtime := h.MemoryRuntime(); runtime != nil {
			response.MemoryRuntimeState = "active"
			response.MemoryRuntimeReason = "runtime_active"
		} else if runtimeErr != "" {
			response.MemoryRuntimeState = "unavailable"
			response.MemoryRuntimeReason = "startup_failed"
		} else {
			response.MemoryRuntimeState = "unavailable"
			response.MemoryRuntimeReason = "runtime_not_initialized"
		}
	default:
		response.MemoryRuntimeState = "unavailable"
		response.MemoryRuntimeReason = "invalid_mode"
	}
	canViewDiagnostics := h.authorizeMemoryAdmin(r) == nil
	if !canViewDiagnostics {
		// A status probe is intentionally available to ordinary callers, but
		// worker failures may contain provider responses or secret-shaped
		// payloads. Preserve the diagnostic shape while omitting those fields
		// for non-admin callers. Redaction alone is insufficient here because
		// even a non-secret provider error can disclose internal details.
		response.ExtractionJobs = sanitizeMemoryJobDiagnostics(response.ExtractionJobs)
		response.FallbackReason = ""
	} else {
		response.MemoryRuntimeError = runtimeErr
	}
	if h.runtimeConfig != nil {
		response.PinnedBudgetRatio = h.runtimeConfig.Memory.Injection.PinnedBudgetRatio
		response.GenerationEnabled = h.runtimeConfig.Memory.Generation.Enabled
		response.GenerationTrigger = h.runtimeConfig.Memory.Generation.Trigger
		response.ConsolidationEnabled = h.runtimeConfig.Memory.Consolidation.Enabled
		response.ManualEnabled = h.runtimeConfig.Memory.Manual.Enabled
		response.TurnDigestEnabled = h.runtimeConfig.Memory.Generation.TurnDigest.Enabled
		response.MemoryToolsEnabled = configuredMode == "on" && h.runtimeConfig.Memory.Tools.Enabled
		switch {
		case configuredMode != "on":
			response.MemoryToolsReason = "requires_mode_on"
		case !h.runtimeConfig.Memory.Tools.Enabled:
			response.MemoryToolsReason = "tools_disabled"
		default:
			response.MemoryToolsReason = "enabled"
		}
		response.CaptureExtractionEnabled = h.runtimeConfig.Memory.Maintenance.CaptureExtraction.Enabled
		response.JobDeadlineRepairEnabled = h.runtimeConfig.Memory.Maintenance.JobDeadlineRepair.Enabled
	}
	if rt := h.MemoryRuntime(); rt != nil {
		response.MemoryRuntimePresent = true
		response.MemoryRuntimeWorkspace = rt.Config.Workspace
		if workspacePathsEqual(workspace, rt.Config.Workspace) {
			stats := rt.DigestStats()
			if !canViewDiagnostics {
				stats.LastError = ""
				stats.LastErrorAt = time.Time{}
				stats.LastSkipReason = sanitizeMemoryDiagnosticText(stats.LastSkipReason)
				stats.LastMaintenanceStage = sanitizeMemoryDiagnosticText(stats.LastMaintenanceStage)
				stats.LastMaintenanceResult = ""
			}
			response.DigestRuntime = &stats
			if !stats.LastMaintenanceAt.IsZero() {
				response.LastMaintenanceAt = stats.LastMaintenanceAt.UTC().Format(time.RFC3339Nano)
			}
			response.LastSkipReason = stats.LastSkipReason
			response.LastMaintenanceStage = stats.LastMaintenanceStage
			response.LastMaintenanceResult = stats.LastMaintenanceResult
		}
	}
	h.writeJSON(w, http.StatusOK, response)
}

func sanitizeMemoryJobDiagnostics(jobs []memorystore.JobDiagnostic) []memorystore.JobDiagnostic {
	if len(jobs) == 0 {
		return jobs
	}
	sanitized := append([]memorystore.JobDiagnostic(nil), jobs...)
	for i := range sanitized {
		sanitized[i].LastError = ""
		sanitized[i].DeadLetterReason = ""
	}
	return sanitized
}

func sanitizeMemoryDiagnosticText(text string) string {
	text, _ = memoryredact.Redact(strings.TrimSpace(text))
	if runes := []rune(text); len(runes) > 512 {
		text = string(runes[:509]) + "..."
	}
	return text
}

func (h *Handler) ListMemoryRecords(w http.ResponseWriter, r *http.Request) {
	asOfText := strings.TrimSpace(r.URL.Query().Get("as_of"))
	var asOf *time.Time
	if asOfText != "" {
		if err := h.authorizeMemoryAdmin(r); err != nil {
			h.writeError(w, http.StatusForbidden, err)
			return
		}
		if !h.memoryV2Enabled() {
			h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "historical memory query requires memory mode"))
			return
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, asOfText)
		if parseErr != nil || parsed.IsZero() {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "as_of must be an RFC3339 timestamp"))
			return
		}
		asOf = &parsed
	}
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
	canViewPrivate := h.authorizeMemoryAdmin(r) == nil
	includeArchived := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include_archived")), "true")
	if includeArchived && !canViewPrivate {
		h.writeError(w, http.StatusForbidden, errors.New(errors.ErrAgentPermission, "archived memory requires admin authorization"))
		return
	}
	query := memorystore.RecordSearchQuery{
		Status:          memorystore.Status(strings.ToLower(status)),
		Kind:            strings.TrimSpace(r.URL.Query().Get("kind")),
		Tag:             strings.TrimSpace(r.URL.Query().Get("tag")),
		Text:            firstNonEmpty(strings.TrimSpace(r.URL.Query().Get("q")), strings.TrimSpace(r.URL.Query().Get("query"))),
		SourceKind:      strings.TrimSpace(r.URL.Query().Get("source_kind")),
		SourceSessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
		IncludeArchived: includeArchived,
		Limit:           limit,
		Cursor:          cursor,
		AsOf:            asOf,
	}
	page, err := store.SearchRecords(r.Context(), query)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	items := make([]memoryRecordDTO, 0, len(page.Items))
	if strings.EqualFold(status, string(memorystore.StatusDeleted)) && !canViewPrivate {
		h.writeError(w, http.StatusForbidden, errors.New(errors.ErrAgentPermission, "deleted memory requires admin authorization"))
		return
	}
	for _, hit := range page.Items {
		record := hit.Record
		if !canViewPrivate && (record.Sensitivity == "private" || memoryredact.DetectSecret(record.Text) != "") {
			continue
		}
		items = append(items, memoryRecordToDTO(record))
	}
	h.writeJSON(w, http.StatusOK, memoryRecordListResponse{
		WorkspacePath: workspace, Items: items, Count: len(items), NextCursor: page.NextCursor,
		SearchBackend: page.Backend,
		AsOf: func() string {
			if asOf == nil {
				return ""
			}
			return asOf.UTC().Format(time.RFC3339Nano)
		}(),
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
	limit := parsePositiveIntQuery(r, "limit", 0)
	if limit <= 0 {
		limit = 8
		if h.runtimeConfig != nil && h.runtimeConfig.Memory.Injection.MaxItems > 0 {
			limit = h.runtimeConfig.Memory.Injection.MaxItems
		}
	}
	if limit > 50 {
		limit = 50
	}
	budget := parsePositiveIntQuery(r, "token_budget", 0)
	if budget <= 0 && h.runtimeConfig != nil {
		budget = h.runtimeConfig.Memory.Injection.EffectiveTokenBudget()
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
	if selection.Core != nil {
		seen[selection.Core.ID] = struct{}{}
		selected = append(selected, memoryWhyNotSelectedDTO{
			ID: selection.Core.ID, Stage: "core", Kind: "core",
			TokenEstimate: memorystore.EstimateTokens(selection.Core.Content),
		})
	}
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
		TokenBudget: budget, CategoryBudget: selection.CategoryBudget,
		CategoryUsed: selection.CategoryUsed, Injected: len(selected) > 0,
	})
}

// GetMemoryCore exposes only the currently reviewed active Core Memory. It
// deliberately shares Store.GetCore with the agent tool, including the
// read-time evidence revalidation; a stale source must never remain silently
// injectable merely because the Core was activated earlier.
func (h *Handler) GetMemoryCore(w http.ResponseWriter, r *http.Request) {
	if h.runtimeConfig != nil &&
		!strings.EqualFold(strings.TrimSpace(h.runtimeConfig.Memory.Mode), "on") {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "core memory requires memory.mode=on"))
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
	core, err := store.GetCore(r.Context())
	if stderrors.Is(err, sql.ErrNoRows) {
		h.writeJSON(w, http.StatusOK, memoryCoreResponse{
			WorkspacePath: workspace,
			Scope:         string(memorystore.ScopeProject),
			Status:        "not_found",
		})
		return
	}
	if err != nil {
		// Do not reflect source text or internal identifiers in a regular
		// read response. The durable event/audit path remains available to
		// authorized operators.
		if stderrors.Is(err, memorystore.ErrCoreSourceIneligible) {
			h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "active core memory is unavailable because its evidence is no longer eligible"))
			return
		}
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrValidationFailed, "active core memory is unavailable"))
		return
	}
	h.writeJSON(w, http.StatusOK, memoryCoreResponse{
		WorkspacePath: workspace,
		Scope:         string(core.ScopeKind),
		Status:        memorystore.CoreStatusActive,
		Core:          &core,
	})
}

// RebuildMemoryCore is an explicit admin trigger for one bounded candidate
// generation pass. It is synchronous for now: the durable core_rebuild job
// still provides coalescing, lease fencing, retry state, and transaction
// boundaries. Periodic background rebuild is a separate, opt-in maintenance
// schedule controlled by memory.core.rebuild.enabled/interval.
func (h *Handler) RebuildMemoryCore(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "core rebuild requires memory v2"))
		return
	}
	var req struct {
		WorkspacePath string `json:"workspace_path"`
	}
	body := http.MaxBytesReader(w, r.Body, 4*1024)
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	// Decode once more to require exactly one JSON value and force the bounded
	// reader to consume trailing whitespace too. Otherwise a valid first object
	// could hide an oversized body or a second JSON document after it.
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, req.WorkspacePath)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	result, err := h.RebuildMemoryCoreForWorkspace(r.Context(), workspace)
	if err != nil {
		// Keep provider/store/lease details out of the HTTP response. They are
		// available through the durable job/audit diagnostics for admins.
		h.writeError(w, http.StatusInternalServerError, errors.New(errors.ErrValidationFailed, "core rebuild unavailable"))
		return
	}
	h.writeJSON(w, http.StatusOK, memoryCoreRebuildResponse{
		WorkspacePath: workspace,
		JobID:         result.JobID,
		JobStatus:     result.JobStatus,
		SourceCount:   result.SourceCount,
		UsageRows:     result.UsageRows,
		Skipped:       result.Skipped,
		Candidate:     result.Candidate,
	})
}

// ListMemoryCores is the operator review surface for candidate and historical
// project Core versions. Candidate content is intentionally admin-only.
func (h *Handler) ListMemoryCores(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "core memory management requires memory v2"))
		return
	}
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	if status == "" {
		status = memorystore.CoreStatusCandidate
	}
	switch status {
	case memorystore.CoreStatusCandidate, memorystore.CoreStatusActive,
		memorystore.CoreStatusStale, memorystore.CoreStatusArchived:
	default:
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid core status"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrValidationFailed, "memory store unavailable"))
		return
	}
	defer store.Close()
	items, err := store.ListCore(r.Context(), memorystore.CoreMemoryFilter{
		ScopeKind: memorystore.ScopeProject,
		Status:    status,
		Limit:     parsePositiveIntQuery(r, "limit", 20),
	})
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrValidationFailed, "core list unavailable"))
		return
	}
	h.writeJSON(w, http.StatusOK, memoryCoreListResponse{
		WorkspacePath: workspace,
		Scope:         string(memorystore.ScopeProject),
		Status:        status,
		Items:         items,
		Count:         len(items),
	})
}

// ActivateMemoryCore is the explicit human review gate. The approver identity
// is host-assigned rather than accepted from an untrusted request field.
func (h *Handler) ActivateMemoryCore(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "core memory management requires memory v2"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrValidationFailed, "memory store unavailable"))
		return
	}
	defer store.Close()
	core, err := store.ActivateCore(r.Context(), mux.Vars(r)["id"], "admin:runtime-api")
	switch {
	case stderrors.Is(err, sql.ErrNoRows):
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "core not found"))
	case stderrors.Is(err, memorystore.ErrCoreNotCandidate),
		stderrors.Is(err, memorystore.ErrCoreSourceIneligible):
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "core candidate is no longer eligible for activation"))
	case err != nil:
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrValidationFailed, "core activation unavailable"))
	default:
		h.writeJSON(w, http.StatusOK, core)
	}
}

// ArchiveMemoryCore removes a candidate or active version from injection while
// keeping its audit history. It is deliberately separate from activation.
func (h *Handler) ArchiveMemoryCore(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "core memory management requires memory v2"))
		return
	}
	var req memoryCoreArchiveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid request"))
		return
	}
	if utf8.RuneCountInString(req.Reason) > 500 {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "archive reason is too long"))
		return
	}
	if strings.TrimSpace(req.Reason) != "" && memoryredact.DetectSecret(req.Reason) != "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "archive reason is not allowed"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrValidationFailed, "memory store unavailable"))
		return
	}
	defer store.Close()
	err = store.ArchiveCore(r.Context(), mux.Vars(r)["id"], "admin:runtime-api", req.Reason)
	switch {
	case stderrors.Is(err, sql.ErrNoRows):
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "core not found"))
	case err != nil:
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrValidationFailed, "core archive unavailable"))
	default:
		h.writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	}
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
		Text: req.Text, Title: req.Title, Summary: req.Summary, CanonicalID: req.CanonicalID,
		Tags: req.Tags, Kind: req.Kind, Pinned: req.Pinned,
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
		Status: "active", Text: note.Text, Title: note.Title, Summary: note.Summary,
		CanonicalID: note.CanonicalID, Archived: note.Archived,
		Tags: note.Tags, Pinned: req.Pinned == nil || *req.Pinned,
		Sensitivity: "internal", SourceKind: firstNonEmpty(req.Source, "manual"), SourceSessionID: req.SessionID,
		Revision: 1, CreatedAt: note.CreatedAt.UTC().Format(time.RFC3339),
		Freshness: "fresh",
	})
}

// ListMemoryRecordSources exposes the bounded, redacted evidence for a memory
// record to administrators only. Evidence quotes are not included in ordinary
// memory record responses because they may contain conversation context.
func (h *Handler) ListMemoryRecordSources(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
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

	recordID := strings.TrimSpace(mux.Vars(r)["id"])
	if _, err := store.GetRecord(r.Context(), recordID); err != nil {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "memory record not found"))
		return
	}
	sources, err := store.ListRecordSources(r.Context(), recordID)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"memory_id": recordID,
		"items":     sources,
		"count":     len(sources),
	})
}

// ListMemoryExtractionDiagnostics returns bounded, redacted reasons why
// extraction candidates were ignored. It is an operator surface and therefore
// follows the configured memory-admin authorization policy.
func (h *Handler) ListMemoryExtractionDiagnostics(w http.ResponseWriter, r *http.Request) {
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

	jobID := strings.TrimSpace(r.URL.Query().Get("job_id"))
	items, err := store.ListExtractionDiagnostics(r.Context(), jobID, parsePositiveIntQuery(r, "limit", 100))
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"job_id": jobID,
		"items":  items,
		"count":  len(items),
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
		(record.Sensitivity == "private" || record.Status == memorystore.StatusDeleted ||
			record.Archived || memoryredact.DetectSecret(record.Text) != "") {
		h.writeError(w, http.StatusForbidden, errors.New(errors.ErrAgentPermission, "private/deleted/archived memory requires admin authorization"))
		return
	}
	dto := memoryRecordToDTO(record)
	if usage, usageErr := store.ListUsage(r.Context(), memorystore.UsageFilter{MemoryID: record.ID, Limit: 20}); usageErr == nil {
		dto.Usage = usage
	}
	h.writeJSON(w, http.StatusOK, dto)
}

// ListMemoryRecordVersions exposes the immutable record history to memory
// administrators. Historical record text is returned only through the same
// admin-gated surface as deleted/private records; the DTO intentionally omits
// raw source evidence and project internals.
func (h *Handler) ListMemoryRecordVersions(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
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
	recordID := strings.TrimSpace(mux.Vars(r)["id"])
	limit := parsePositiveIntQuery(r, "limit", 100)
	if limit > 500 {
		limit = 500
	}
	versions, hasMore, err := store.ListRecordVersionsPage(r.Context(), recordID, limit)
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "memory record not found"))
			return
		}
		h.writeError(w, http.StatusServiceUnavailable,
			errors.New(errors.ErrValidationFailed, "memory record history unavailable"))
		return
	}
	items := make([]memoryRecordVersionDTO, 0, len(versions))
	for _, version := range versions {
		items = append(items, memoryRecordVersionToDTO(version))
	}
	h.writeJSON(w, http.StatusOK, memoryRecordVersionsResponse{
		WorkspacePath: workspace,
		MemoryID:      recordID,
		Items:         items,
		Count:         len(items),
		HasMore:       hasMore,
	})
}

// RollbackMemoryRecord restores one explicitly selected immutable version.
// The store enforces the expected-revision compare-and-swap and all purge /
// maintenance fences; this handler only maps those safe conflicts to HTTP.
func (h *Handler) RollbackMemoryRecord(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	var req memoryRecordRollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	if req.TargetVersion <= 0 || req.ExpectedRevision <= 0 || strings.TrimSpace(req.Reason) == "" {
		h.writeError(w, http.StatusBadRequest,
			errors.New(errors.ErrValidationFailed, "rollback requires target_version, expected_revision, and reason"))
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
	record, err := store.RollbackRecord(r.Context(), mux.Vars(r)["id"], memorystore.RecordRollbackOptions{
		TargetVersion:    req.TargetVersion,
		ExpectedRevision: req.ExpectedRevision,
		Reason:           req.Reason,
		ChangedBy:        "runtime_api:memory_admin",
		RevertSupersede:  req.RevertSupersede,
	})
	if err != nil {
		switch {
		case stderrors.Is(err, sql.ErrNoRows):
			h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "memory record or version not found"))
		case stderrors.Is(err, memorystore.ErrSessionPurgeGenerationMismatch),
			stderrors.Is(err, memorystore.ErrMaintenanceLeaseBusy),
			stderrors.Is(err, memorystore.ErrMaintenanceLeaseLost),
			strings.Contains(err.Error(), "changed concurrently"),
			strings.Contains(err.Error(), "successor changed"),
			strings.Contains(err.Error(), "requires revert_supersede"):
			h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory record rollback conflict"))
		default:
			// Store errors may contain SQL/provider details or secret-shaped
			// validation context. Keep the operator API classifiable without
			// reflecting those internals to the caller.
			h.writeError(w, http.StatusBadRequest,
				errors.New(errors.ErrValidationFailed, "invalid memory record rollback request"))
		}
		return
	}
	h.writeJSON(w, http.StatusOK, memoryRecordToDTO(record))
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
	patch := memorystore.RecordPatch{
		Tags: req.Tags, Pinned: req.Pinned, Title: req.Title, Summary: req.Summary,
		CanonicalID: req.CanonicalID, SupersededBy: req.SupersededBy, Archived: req.Archived,
	}
	if req.Status != nil {
		status := memorystore.Status(strings.TrimSpace(*req.Status))
		patch.Status = &status
	}
	var record memorystore.Record
	if req.Text != nil {
		record, err = store.CorrectRecord(r.Context(), recordID, *req.Text)
		if err == nil && (req.Tags != nil || req.Pinned != nil || req.Status != nil ||
			req.Title != nil || req.Summary != nil || req.CanonicalID != nil ||
			req.SupersededBy != nil || req.Archived != nil) {
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
	age := time.Duration(parsePositiveIntQuery(r, "age_seconds", 0)) * time.Second
	jobs, err := store.ListJobDiagnostics(r.Context(), memorystore.JobDiagnosticFilter{
		Kind: r.URL.Query().Get("kind"), Status: r.URL.Query().Get("status"),
		SessionID: r.URL.Query().Get("session_id"), OlderThan: age,
		Limit: parsePositiveIntQuery(r, "limit", 50),
	})
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

// SummarizeMemoryJobs returns the bounded aggregate view used by memory
// operators. It deliberately avoids per-job keys, session identities, and
// provider diagnostics.
func (h *Handler) SummarizeMemoryJobs(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeJSON(w, http.StatusOK, memorystore.JobOperatorSummary{
			ByKind:     memorystore.JobCountGroups{Counts: map[string]int{}},
			ByStatus:   memorystore.JobCountGroups{Counts: map[string]int{}},
			ByPriority: memorystore.JobCountGroups{Counts: map[string]int{}},
		})
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable,
			errors.New(errors.ErrValidationFailed, "memory store unavailable"))
		return
	}
	defer store.Close()
	summary, err := store.SummarizeJobs(r.Context())
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable,
			errors.New(errors.ErrValidationFailed, "memory job summary unavailable"))
		return
	}
	h.writeJSON(w, http.StatusOK, summary)
}

type memoryJobDeadlineRepairRequest struct {
	WorkspacePath string `json:"workspace_path,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Apply         bool   `json:"apply,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// RepairMemoryJobDeadlines first returns a bounded dry-run plan. Applying the
// plan is explicit and takes the same project maintenance lease as the
// periodic maintenance owner, so an operator cannot race a worker or another
// repair invocation.
func (h *Handler) RepairMemoryJobDeadlines(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	var req memoryJobDeadlineRepairRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid request"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, req.WorkspacePath)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable,
			errors.New(errors.ErrValidationFailed, "memory store unavailable"))
		return
	}
	defer store.Close()
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if !req.Apply {
		result, planErr := store.PlanMissingJobDeadlines(r.Context(), limit)
		if planErr != nil {
			h.writeError(w, http.StatusServiceUnavailable,
				errors.New(errors.ErrValidationFailed, "memory deadline repair plan unavailable"))
			return
		}
		h.writeJSON(w, http.StatusOK, result)
		return
	}

	lease, acquired, err := store.AcquireMaintenanceLease(
		r.Context(), "operator-deadline-repair-"+uuid.NewString(), "operator", 2*time.Minute,
	)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable,
			errors.New(errors.ErrValidationFailed, "memory deadline repair unavailable"))
		return
	}
	if !acquired {
		h.writeError(w, http.StatusConflict,
			errors.New(errors.ErrValidationFailed, "memory maintenance is busy"))
		return
	}
	ownedCtx := memorystore.WithMaintenanceLease(r.Context(), lease)
	result, repairErr := store.RepairMissingJobDeadlines(ownedCtx, memorystore.JobDeadlineRepairOptions{
		Limit: limit, Reason: req.Reason,
	})
	releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	releaseErr := store.ReleaseMaintenanceLease(releaseCtx, lease)
	cancel()
	if repairErr != nil || releaseErr != nil {
		h.writeError(w, http.StatusServiceUnavailable,
			errors.New(errors.ErrValidationFailed, "memory deadline repair unavailable"))
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// PreviewMemoryDigestReplay exposes the bounded, read-only recovery
// classification report. It intentionally requires the bound runtime because
// canonical receipt and current session-generation checks belong to the
// runtime's source adapter, not to a memory-db-only handler.
func (h *Handler) PreviewMemoryDigestReplay(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		limit := parsePositiveIntQuery(r, "limit", 100)
		if limit > 500 {
			limit = 500
		}
		h.writeJSON(w, http.StatusOK, memoryruntime.DigestReplayReport{
			Limit:   limit,
			ByClass: map[memorystore.DigestReplayClassification]int{},
			Items:   []memorystore.DigestReplayResult{},
		})
		return
	}
	runtime := h.MemoryRuntime()
	if runtime == nil {
		h.writeError(w, http.StatusServiceUnavailable,
			errors.New(errors.ErrValidationFailed, "memory runtime unavailable"))
		return
	}
	report, err := runtime.PreviewDigestReplayRecovery(
		r.Context(), parsePositiveIntQuery(r, "limit", 100),
	)
	if err != nil {
		// The report itself contains only safe classifications. Do not expose
		// source/provider/database diagnostics through this admin endpoint.
		h.writeError(w, http.StatusServiceUnavailable,
			errors.New(errors.ErrValidationFailed, "memory recovery report unavailable"))
		return
	}
	h.writeJSON(w, http.StatusOK, report)
}

// ReplayMemoryDigest applies the safe replay decision for one digest. The
// runtime verifies the canonical receipt, closed range, and session generation
// before the store can recreate a job. Non-replayable classifications are
// returned as conflicts without exposing raw source/provider diagnostics.
func (h *Handler) ReplayMemoryDigest(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	var req memoryJobOperatorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid request"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	rt := h.MemoryRuntime()
	if rt == nil || rt.Store == nil {
		h.writeError(w, http.StatusServiceUnavailable,
			errors.New(errors.ErrValidationFailed, "memory runtime unavailable"))
		return
	}
	if !sameWorkspacePath(workspace, rt.Config.Workspace) {
		h.writeError(w, http.StatusConflict,
			errors.New(errors.ErrValidationFailed, "memory runtime workspace mismatch"))
		return
	}
	result, err := rt.ReplayDigest(r.Context(), mux.Vars(r)["id"], req.Reason)
	switch {
	case stderrors.Is(err, memorystore.ErrDigestNotFound):
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "memory digest not found"))
	case stderrors.Is(err, memorystore.ErrDigestReplayIneligible),
		stderrors.Is(err, memorystore.ErrJobConflict),
		stderrors.Is(err, memorystore.ErrSessionPurgeGenerationMismatch),
		stderrors.Is(err, memorystore.ErrMaintenanceLeaseBusy):
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory digest is not replayable"))
	case err != nil:
		h.writeError(w, http.StatusServiceUnavailable,
			errors.New(errors.ErrValidationFailed, "memory digest replay unavailable"))
	case result.Classification != memorystore.DigestReplayApplied &&
		result.Classification != memorystore.DigestReplayAlreadyQueued &&
		result.Classification != memorystore.DigestReplayAlreadyProcessed:
		h.writeJSON(w, http.StatusConflict, result)
	default:
		h.writeJSON(w, http.StatusOK, result)
	}
}

type memoryJobOperatorRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) operateMemoryJob(w http.ResponseWriter, r *http.Request, retry bool) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	var req memoryJobOperatorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid request"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrValidationFailed, "memory store unavailable"))
		return
	}
	defer store.Close()
	if retry {
		err = store.RequeueJob(r.Context(), mux.Vars(r)["id"], req.Reason)
	} else {
		err = store.DeadLetterJob(r.Context(), mux.Vars(r)["id"], req.Reason)
	}
	switch {
	case stderrors.Is(err, memorystore.ErrJobNotFound):
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "memory job not found"))
	case stderrors.Is(err, memorystore.ErrJobConflict), stderrors.Is(err, memorystore.ErrSessionPurgeGenerationMismatch):
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory job state conflict"))
	case err != nil:
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrValidationFailed, "memory job operation unavailable"))
	default:
		h.writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	}
}
func (h *Handler) RetryMemoryJob(w http.ResponseWriter, r *http.Request) {
	h.operateMemoryJob(w, r, true)
}
func (h *Handler) DeadLetterMemoryJob(w http.ResponseWriter, r *http.Request) {
	h.operateMemoryJob(w, r, false)
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

func (h *Handler) ListMemoryCoreUsage(w http.ResponseWriter, r *http.Request) {
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
		h.writeJSON(w, http.StatusOK, memoryCoreUsageListResponse{
			WorkspacePath: workspace, Items: []memorystore.CoreUsageRecord{},
		})
		return
	}
	usage, err := store.ListCoreUsage(r.Context(), memorystore.CoreUsageFilter{
		CoreID:    strings.TrimSpace(r.URL.Query().Get("core_id")),
		SessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
		TurnID:    strings.TrimSpace(r.URL.Query().Get("turn_id")),
		UseKind:   memorystore.UsageKind(strings.TrimSpace(r.URL.Query().Get("use_kind"))),
		Limit:     parsePositiveIntQuery(r, "limit", 50),
	})
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	h.writeJSON(w, http.StatusOK, memoryCoreUsageListResponse{
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

func (h *Handler) PurgeMemoryTurnWindows(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	var req memoryTurnWindowPurgeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	if !req.Confirm {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "purge requires confirm=true"))
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if req.All == (sessionID != "") {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "purge requires exactly one of session_id or all=true"))
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
	result, err := store.PurgeTurnWindows(r.Context(), sessionID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	scope := "project"
	if sessionID != "" {
		scope = "session"
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true, "action": "purge_turn_windows", "workspace_path": workspace,
		"scope": scope, "session_id": sessionID,
		"windows_deleted": result.Windows, "digests_released": result.Digests,
	})
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
		cfg.CategoryBudgetsEnabled = true
		cfg.CategoryBudgets = memorystore.InjectionCategoryBudgets{
			Core:      h.runtimeConfig.Memory.Injection.Budgets.Core,
			Task:      h.runtimeConfig.Memory.Injection.Budgets.Task,
			Retrieved: h.runtimeConfig.Memory.Injection.Budgets.Retrieved,
			Procedure: h.runtimeConfig.Memory.Injection.Budgets.Procedure,
			Reference: h.runtimeConfig.Memory.Injection.Budgets.Reference,
		}
		cfg.DisableStageSplit = !h.runtimeConfig.Memory.Injection.StageSplit
		cfg.DisableInjection = !h.runtimeConfig.Memory.Injection.Enabled
		cfg.DisableManual = !h.runtimeConfig.Memory.Manual.Enabled
		cfg.RecallTraceEnabled = h.runtimeConfig.Memory.Injection.RecallTrace.Enabled
		cfg.RecallTraceSampleRate = h.runtimeConfig.Memory.Injection.RecallTrace.SampleRate
		cfg.Retention = memorypolicy.StoreRetention(h.runtimeConfig.Memory)
		cfg.StaleBehavior = memorypolicy.StaleBehavior(h.runtimeConfig.Memory)
		cfg.DisableSecretFilter = memorypolicy.DisableSecretFilter(h.runtimeConfig.Memory)
		cfg.TurnDigestBatchSize = h.runtimeConfig.Memory.Generation.TurnDigest.MaxTurnsPerBatch
	}
	return memorystore.New(cfg)
}

func (h *Handler) authorizeMemoryAdmin(r *http.Request) error {
	if !h.adminAuthRequiredEnabled() {
		return nil
	}
	if h.hasValidSearchAdminToken(r) || h.hasTrustedAdminRole(r) {
		return nil
	}
	return errors.New(errors.ErrAgentPermission, "memory admin endpoints require a valid admin token or admin role")
}

func memoryRecordToDTO(record memorystore.Record) memoryRecordDTO {
	return memoryRecordDTO{
		ID: record.ID, Scope: string(record.Scope), Kind: record.Kind, Status: string(record.Status),
		Text: record.Text, Title: record.Title, Summary: record.Summary, CanonicalID: record.CanonicalID,
		SupersededBy: record.SupersededBy, Archived: record.Archived,
		Tags: append([]string(nil), record.Tags...), Confidence: record.Confidence,
		Sensitivity: record.Sensitivity,
		Pinned:      record.Pinned, RetentionClass: record.RetentionClass, SourceKind: record.SourceKind,
		SourceSessionID: record.SourceSessionID, SourceTurnID: record.SourceTurnID,
		SourceRefs: append([]string(nil), record.SourceRefs...), Revision: record.Revision,
		CreatedAt: formatMemoryTime(record.CreatedAt), UpdatedAt: formatMemoryTime(record.UpdatedAt),
		LastUsedAt: formatMemoryTime(record.LastUsedAt), UseCount: record.UseCount,
		ValidFrom:  formatMemoryTime(record.ValidFrom),
		ValidUntil: formatMemoryTime(record.ValidUntil), Freshness: string(record.Freshness),
		LastVerifiedAt:  formatMemoryTime(record.LastVerifiedAt),
		RevalidateAfter: formatMemoryTime(record.RevalidateAfter), StaleReason: record.StaleReason,
	}
}

func memoryRecordVersionToDTO(version memorystore.RecordVersion) memoryRecordVersionDTO {
	return memoryRecordVersionDTO{
		ID: version.ID, MemoryID: version.MemoryID, Version: version.Version,
		ScopeKind: string(version.ScopeKind), ScopeKey: version.ScopeKey,
		Record:    memoryRecordToDTO(version.Record),
		ChangedBy: version.ChangedBy, ChangeReason: version.ChangeReason,
		ChangeType: version.ChangeType, SourceEventID: version.SourceEventID,
		ValidFrom:  formatMemoryTimeNano(version.ValidFrom),
		ValidUntil: formatMemoryTimeNano(version.ValidUntil),
		CreatedAt:  formatMemoryTimeNano(version.CreatedAt),
	}
}

func formatMemoryTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func formatMemoryTimeNano(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
