package skills

import (
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"os"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspaceregistry"
)

// workspaceDirectoryView is the HTTP shape of a registered workspace
// directory. exists is computed live (the directory may have been removed
// externally); session_count is best-effort and only served to non-workspace
// callers — the workspace UI aggregates counts locally from GET /sessions.
type workspaceDirectoryView struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	Name         string `json:"name,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	LastUsedAt   int64  `json:"last_used_at,omitempty"`
	Exists       bool   `json:"exists"`
	SessionCount int    `json:"session_count,omitempty"`
}

// workspaceDirectoryRegistry returns the injected store (tests) or lazily
// loads the default ~/.aicli/workspace_directories.yaml registry.
//
// The lookup always goes through the sync.Once: reading the field directly on
// a fast path would race with the lazy assignment performed inside Do.
func (h *Handler) workspaceDirectoryRegistry() *workspaceregistry.Store {
	h.workspaceDirectoriesOnce.Do(func() {
		if h.workspaceDirectories == nil {
			h.workspaceDirectories = workspaceregistry.Load()
		}
	})
	return h.workspaceDirectories
}

// writeWorkspaceDirectoryError maps registry sentinel errors onto HTTP
// statuses: 400 validation / missing directory, 404 unknown id, 503
// fail-closed (no-home) or persistence failure.
func (h *Handler) writeWorkspaceDirectoryError(w http.ResponseWriter, err error) {
	switch {
	case stderrors.Is(err, workspaceregistry.ErrValidationFailed),
		stderrors.Is(err, workspaceregistry.ErrDirectoryNotFound),
		stderrors.Is(err, workspaceregistry.ErrNotDirectory):
		h.writeError(w, http.StatusBadRequest, err)
	case stderrors.Is(err, workspaceregistry.ErrNotFound):
		h.writeError(w, http.StatusNotFound, err)
	case stderrors.Is(err, workspaceregistry.ErrStoreUnavailable):
		h.writeError(w, http.StatusServiceUnavailable, err)
	default:
		h.writeError(w, http.StatusInternalServerError, err)
	}
}

// RegisterWorkspaceDirectoryRoutes registers the /workspace-directories
// endpoints on router (also used by RegisterRoutes and tests).
func (h *Handler) RegisterWorkspaceDirectoryRoutes(router *mux.Router) {
	router.HandleFunc("/workspace-directories", h.ListWorkspaceDirectories).Methods(http.MethodGet)
	router.HandleFunc("/workspace-directories", h.CreateWorkspaceDirectory).Methods(http.MethodPost)
	router.HandleFunc("/workspace-directories/{id}", h.UpdateWorkspaceDirectory).Methods(http.MethodPatch)
	router.HandleFunc("/workspace-directories/{id}", h.DeleteWorkspaceDirectory).Methods(http.MethodDelete)
}

// ListWorkspaceDirectories handles GET /api/runtime/workspace-directories.
func (h *Handler) ListWorkspaceDirectories(w http.ResponseWriter, r *http.Request) {
	records := h.workspaceDirectoryRegistry().List()

	// One optional shared-store scan feeds every directory's session_count.
	// Scan failure (e.g. locked SQLite) only drops the optional field.
	sessions, scanErr := h.scanSessionsForDirectoryStats(r)

	directories := make([]workspaceDirectoryView, 0, len(records))
	for _, rec := range records {
		view := workspaceDirectoryView{
			ID:         rec.ID,
			Path:       rec.Path,
			Name:       rec.Name,
			CreatedAt:  rec.CreatedAt,
			LastUsedAt: rec.LastUsedAt,
			Exists:     directoryExistsOnServer(rec.Path),
		}
		if scanErr == nil {
			view.SessionCount = countSessionsForDirectory(sessions, rec.Path)
		}
		directories = append(directories, view)
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"directories": directories,
		"count":       len(directories),
	})
}

// CreateWorkspaceDirectory handles POST /api/runtime/workspace-directories.
// Registration only records existing directories; duplicates are idempotent
// (200 + existing=true), first registration answers 201.
func (h *Handler) CreateWorkspaceDirectory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}

	record, existing, err := h.workspaceDirectoryRegistry().Add(req.Path, req.Name)
	if err != nil {
		h.writeWorkspaceDirectoryError(w, err)
		return
	}

	status := http.StatusCreated
	if existing {
		status = http.StatusOK
	}
	h.writeJSON(w, status, map[string]interface{}{
		"directory": record,
		"existing":  existing,
	})
}

// UpdateWorkspaceDirectory handles PATCH /api/runtime/workspace-directories/{id}.
// Only the display alias is mutable: paths are immutable (rebinding a path
// would drift sessions already bound to it).
func (h *Handler) UpdateWorkspaceDirectory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name *string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}
	if req.Name == nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"name is required"))
		return
	}

	record, err := h.workspaceDirectoryRegistry().Rename(mux.Vars(r)["id"], *req.Name)
	if err != nil {
		h.writeWorkspaceDirectoryError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"directory": record,
	})
}

// DeleteWorkspaceDirectory handles DELETE /api/runtime/workspace-directories/{id}.
// Only the registry entry is dropped: files on the server are never touched
// and bound sessions are preserved (sessions_affected is informational).
func (h *Handler) DeleteWorkspaceDirectory(w http.ResponseWriter, r *http.Request) {
	record, found, err := h.workspaceDirectoryRegistry().Remove(mux.Vars(r)["id"])
	if err != nil {
		h.writeWorkspaceDirectoryError(w, err)
		return
	}
	if !found {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrAPINotFound,
			"workspace directory not found"))
		return
	}

	affected := 0
	if sessions, scanErr := h.scanSessionsForDirectoryStats(r); scanErr == nil {
		affected = countSessionsForDirectory(sessions, record.Path)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted":           true,
		"id":                record.ID,
		"sessions_affected": affected,
	})
}

// scanSessionsForDirectoryStats performs the optional shared-store scan used
// for session_count / sessions_affected. A nil sessionManager yields no data
// instead of an error (registry endpoints must not require session storage).
func (h *Handler) scanSessionsForDirectoryStats(r *http.Request) ([]*chat.Session, error) {
	if h.sessionManager == nil {
		return nil, nil
	}
	ctx, cancel := sessionStoreQueryContext(r)
	defer cancel()
	return h.sessionManager.SearchSessions(ctx, &chat.SessionSearchOptions{})
}

// countSessionsForDirectory counts sessions bound to dirPath via
// sessionmeta.WorkspacePath (exact cleaned compare, case-folded on Windows).
func countSessionsForDirectory(sessions []*chat.Session, dirPath string) int {
	count := 0
	for _, session := range sessions {
		if session == nil {
			continue
		}
		if v, ok := session.Metadata.Context[sessionmeta.WorkspacePath].(string); ok &&
			workspaceregistry.PathMatches(v, dirPath) {
			count++
		}
	}
	return count
}

// directoryExistsOnServer reports whether path is currently an existing
// directory (registration does not pin the filesystem).
func directoryExistsOnServer(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
