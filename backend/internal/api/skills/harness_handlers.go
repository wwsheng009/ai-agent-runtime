package skills

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/plugins"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// --- response / request DTOs -------------------------------------------------

type harnessPermissionsResponse struct {
	WorkspacePath string                             `json:"workspace_path"`
	SourcePath    string                             `json:"source_path,omitempty"`
	Exists        bool                               `json:"exists"`
	Version       int                                `json:"version,omitempty"`
	DenyTools     []string                           `json:"deny_tools,omitempty"`
	AllowTools    []string                           `json:"allow_tools,omitempty"`
	Rules         []runtimepolicy.PermissionsFileRule `json:"rules,omitempty"`
}

type harnessGrantDTO struct {
	Tool    string `json:"tool"`
	Pattern string `json:"pattern,omitempty"`
	Scope   string `json:"scope,omitempty"`
}

type harnessGrantsResponse struct {
	WorkspacePath string            `json:"workspace_path"`
	StorePath     string            `json:"store_path,omitempty"`
	Grants        []harnessGrantDTO `json:"grants"`
	Count         int               `json:"count"`
	Action        string            `json:"action,omitempty"`
	Removed       int               `json:"removed,omitempty"`
}

type harnessGrantsRequest struct {
	WorkspacePath     string `json:"workspace_path"`
	Action            string `json:"action"`
	Tool              string `json:"tool"`
	Pattern           string `json:"pattern"`
	Scope             string `json:"scope"`
	MatchEmptyPattern bool   `json:"match_empty_pattern"`
}

type harnessMemoryNoteDTO struct {
	ID        string   `json:"id"`
	Text      string   `json:"text"`
	Tags      []string `json:"tags,omitempty"`
	Source    string   `json:"source,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	Score     float64  `json:"score,omitempty"`
}

type harnessMemoryResponse struct {
	WorkspacePath string                 `json:"workspace_path"`
	Root          string                 `json:"root,omitempty"`
	Path          string                 `json:"path,omitempty"`
	Query         string                 `json:"query,omitempty"`
	Notes         []harnessMemoryNoteDTO `json:"notes,omitempty"`
	Hits          []harnessMemoryNoteDTO `json:"hits,omitempty"`
	Count         int                    `json:"count"`
	Note          *harnessMemoryNoteDTO  `json:"note,omitempty"`
	Action        string                 `json:"action,omitempty"`
}

type harnessMemoryRequest struct {
	WorkspacePath string   `json:"workspace_path"`
	Text          string   `json:"text"`
	Tags          []string `json:"tags"`
	Source        string   `json:"source"`
	SessionID     string   `json:"session_id"`
}

type harnessPluginDTO struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	Root        string   `json:"root,omitempty"`
	Trust       string   `json:"trust"`
	Enabled     bool     `json:"enabled"`
	Active      bool     `json:"active"`
	Warnings    []string `json:"warnings,omitempty"`
}

type harnessPluginsResponse struct {
	WorkspacePath string             `json:"workspace_path"`
	StatePath     string             `json:"state_path,omitempty"`
	Plugins       []harnessPluginDTO `json:"plugins"`
	Count         int                `json:"count"`
	Plugin        *harnessPluginDTO  `json:"plugin,omitempty"`
	Action        string             `json:"action,omitempty"`
}

type harnessPluginUpdateRequest struct {
	WorkspacePath string `json:"workspace_path"`
	Action        string `json:"action"`
	Trust         string `json:"trust"`
	Enabled       *bool  `json:"enabled"`
}

// --- handlers ----------------------------------------------------------------

// GetHarnessPermissions returns static project permissions.yaml content.
func (h *Handler) GetHarnessPermissions(w http.ResponseWriter, r *http.Request) {
	workspace, err := h.resolveHarnessWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}

	file, err := runtimepolicy.LoadProjectPermissions(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	resp := harnessPermissionsResponse{
		WorkspacePath: workspace,
		Exists:        false,
	}
	if file != nil {
		resp.Exists = true
		resp.SourcePath = file.SourcePath
		resp.Version = file.Version
		resp.DenyTools = append([]string(nil), file.DenyTools...)
		resp.AllowTools = append([]string(nil), file.AllowTools...)
		resp.Rules = append([]runtimepolicy.PermissionsFileRule(nil), file.Rules...)
	} else if path, pathErr := runtimepolicy.ResolveProjectPermissionsPath(workspace); pathErr == nil {
		resp.SourcePath = path
	}
	h.writeJSON(w, http.StatusOK, resp)
}

// GetHarnessGrants lists durable project remembered grants.
func (h *Handler) GetHarnessGrants(w http.ResponseWriter, r *http.Request) {
	workspace, err := h.resolveHarnessWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := runtimepolicy.OpenProjectGrantStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, buildHarnessGrantsResponse(workspace, store, "", 0))
}

// UpdateHarnessGrants remembers or revokes durable project grants.
//
// Body:
//
//	{"action":"remember","tool":"write","pattern":"README.md","scope":"project"}
//	{"action":"revoke","tool":"write","pattern":"README.md"}
//	{"action":"revoke","tool":"write","match_empty_pattern":true}  // tool-wide only
func (h *Handler) UpdateHarnessGrants(w http.ResponseWriter, r *http.Request) {
	var req harnessGrantsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}

	workspace, err := h.resolveHarnessWorkspace(r, req.WorkspacePath)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := runtimepolicy.OpenProjectGrantStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	action := strings.ToLower(strings.TrimSpace(req.Action))
	tool := strings.TrimSpace(req.Tool)
	pattern := strings.TrimSpace(req.Pattern)
	removed := 0

	switch action {
	case "remember", "add", "grant":
		if tool == "" {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "tool is required"))
			return
		}
		if err := store.Remember(runtimepolicy.Grant{
			Tool:    tool,
			Pattern: pattern,
			Scope:   strings.TrimSpace(req.Scope),
		}); err != nil {
			if runtimepolicy.IsDangerousGrantError(err) {
				h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "dangerous tools cannot be remembered as always-allow"))
				return
			}
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
			return
		}
		action = "remember"
	case "revoke", "remove", "delete":
		if tool == "" {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "tool is required"))
			return
		}
		removed = store.Revoke(tool, pattern, req.MatchEmptyPattern)
		action = "revoke"
	default:
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "action must be remember or revoke"))
		return
	}

	h.writeJSON(w, http.StatusOK, buildHarnessGrantsResponse(workspace, store, action, removed))
}

// GetHarnessMemory lists or searches project memory notes.
func (h *Handler) GetHarnessMemory(w http.ResponseWriter, r *http.Request) {
	workspace, err := h.resolveHarnessWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		query = strings.TrimSpace(r.URL.Query().Get("query"))
	}
	limit := parsePositiveIntQuery(r, "limit", 50)
	if limit > 200 {
		limit = 200
	}

	resp := harnessMemoryResponse{
		WorkspacePath: workspace,
		Root:          store.Root(),
		Path:          store.Path(),
		Query:         query,
	}

	if query != "" {
		hits, searchErr := store.Search(memorystore.SearchOptions{Query: query, Limit: limit})
		if searchErr != nil {
			h.writeError(w, http.StatusInternalServerError, searchErr)
			return
		}
		resp.Hits = make([]harnessMemoryNoteDTO, 0, len(hits))
		for _, hit := range hits {
			dto := noteToHarnessDTO(hit.Note)
			dto.Score = hit.Score
			resp.Hits = append(resp.Hits, dto)
		}
		resp.Count = len(resp.Hits)
	} else {
		notes, listErr := store.List(limit)
		if listErr != nil {
			h.writeError(w, http.StatusInternalServerError, listErr)
			return
		}
		resp.Notes = make([]harnessMemoryNoteDTO, 0, len(notes))
		for _, note := range notes {
			resp.Notes = append(resp.Notes, noteToHarnessDTO(note))
		}
		resp.Count = len(resp.Notes)
	}
	h.writeJSON(w, http.StatusOK, resp)
}

// UpdateHarnessMemory appends a project memory note.
//
// Body: {"text":"...","tags":["ops"],"source":"settings"}
func (h *Handler) UpdateHarnessMemory(w http.ResponseWriter, r *http.Request) {
	var req harnessMemoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	workspace, err := h.resolveHarnessWorkspace(r, req.WorkspacePath)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "text is required"))
		return
	}
	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "settings"
	}
	note, err := store.Append(memorystore.AppendNoteOptions{
		Text:      text,
		Tags:      req.Tags,
		Source:    source,
		SessionID: strings.TrimSpace(req.SessionID),
	})
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	dto := noteToHarnessDTO(note)
	h.writeJSON(w, http.StatusOK, harnessMemoryResponse{
		WorkspacePath: workspace,
		Root:          store.Root(),
		Path:          store.Path(),
		Note:          &dto,
		Count:         1,
		Action:        "append",
	})
}

// GetHarnessPlugins lists discovered local plugins with trust/enable state.
func (h *Handler) GetHarnessPlugins(w http.ResponseWriter, r *http.Request) {
	workspace, err := h.resolveHarnessWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	state := plugins.NewStateStore("")
	catalog, err := plugins.Discover(plugins.DiscoverOptions{
		ProjectRoot:     workspace,
		State:           state,
		IncludeDisabled: true,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	list := catalog.List()
	out := make([]harnessPluginDTO, 0, len(list))
	for _, pkg := range list {
		out = append(out, packageToHarnessDTO(pkg))
	}
	h.writeJSON(w, http.StatusOK, harnessPluginsResponse{
		WorkspacePath: workspace,
		StatePath:     state.Path(),
		Plugins:       out,
		Count:         len(out),
	})
}

// UpdateHarnessPlugin trusts/untrusts or enables/disables a local plugin.
//
// Body examples:
//
//	{"action":"trust"}
//	{"action":"untrust"}
//	{"action":"enable"}
//	{"action":"disable"}
//	{"trust":"trusted","enabled":true}
func (h *Handler) UpdateHarnessPlugin(w http.ResponseWriter, r *http.Request) {
	pluginID := strings.TrimSpace(mux.Vars(r)["id"])
	if pluginID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "plugin id is required"))
		return
	}

	var req harnessPluginUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	workspace, err := h.resolveHarnessWorkspace(r, req.WorkspacePath)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}

	state := plugins.NewStateStore("")
	// Resolve package root for state path annotation when present.
	catalog, err := plugins.Discover(plugins.DiscoverOptions{
		ProjectRoot:     workspace,
		State:           state,
		IncludeDisabled: true,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	pkg, ok := catalog.Get(pluginID)
	if !ok {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, fmt.Sprintf("plugin %q not found", pluginID)))
		return
	}
	pkgRoot := ""
	if pkg != nil {
		pkgRoot = pkg.Root
		pluginID = strings.TrimSpace(pkg.Manifest.Name)
		if pluginID == "" {
			pluginID = strings.TrimSpace(mux.Vars(r)["id"])
		}
	}

	action := strings.ToLower(strings.TrimSpace(req.Action))
	switch action {
	case "trust":
		if _, err := state.SetTrust(pluginID, plugins.TrustTrusted, pkgRoot); err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
	case "untrust":
		if _, err := state.SetTrust(pluginID, plugins.TrustUntrusted, pkgRoot); err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
	case "enable":
		if _, err := state.SetEnabled(pluginID, true, pkgRoot); err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
	case "disable":
		if _, err := state.SetEnabled(pluginID, false, pkgRoot); err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
	case "", "update":
		if trust := strings.TrimSpace(req.Trust); trust != "" {
			level := plugins.TrustLevel(strings.ToLower(trust))
			if level != plugins.TrustTrusted && level != plugins.TrustUntrusted {
				h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "trust must be trusted or untrusted"))
				return
			}
			if _, err := state.SetTrust(pluginID, level, pkgRoot); err != nil {
				h.writeError(w, http.StatusInternalServerError, err)
				return
			}
			action = "trust"
			if level == plugins.TrustUntrusted {
				action = "untrust"
			}
		}
		if req.Enabled != nil {
			if _, err := state.SetEnabled(pluginID, *req.Enabled, pkgRoot); err != nil {
				h.writeError(w, http.StatusInternalServerError, err)
				return
			}
			if action == "" || action == "update" {
				if *req.Enabled {
					action = "enable"
				} else {
					action = "disable"
				}
			}
		}
		if action == "" || action == "update" {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "action or trust/enabled is required"))
			return
		}
	default:
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "action must be trust, untrust, enable, or disable"))
		return
	}

	// Re-discover to reflect updated trust/enable.
	catalog, err = plugins.Discover(plugins.DiscoverOptions{
		ProjectRoot:     workspace,
		State:           state,
		IncludeDisabled: true,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	updated, ok := catalog.Get(pluginID)
	if !ok {
		// fall back to prior package with state overlay missing
		dto := packageToHarnessDTO(pkg)
		h.writeJSON(w, http.StatusOK, harnessPluginsResponse{
			WorkspacePath: workspace,
			StatePath:     state.Path(),
			Plugin:        &dto,
			Count:         1,
			Action:        action,
		})
		return
	}
	dto := packageToHarnessDTO(updated)
	list := catalog.List()
	out := make([]harnessPluginDTO, 0, len(list))
	for _, item := range list {
		out = append(out, packageToHarnessDTO(item))
	}
	h.writeJSON(w, http.StatusOK, harnessPluginsResponse{
		WorkspacePath: workspace,
		StatePath:     state.Path(),
		Plugins:       out,
		Plugin:        &dto,
		Count:         len(out),
		Action:        action,
	})
}

// --- helpers -----------------------------------------------------------------

func (h *Handler) resolveHarnessWorkspace(r *http.Request, bodyWorkspace string) (string, error) {
	candidates := []string{
		strings.TrimSpace(bodyWorkspace),
	}
	if r != nil {
		candidates = append(candidates, strings.TrimSpace(r.URL.Query().Get("workspace_path")))
	}
	if h != nil && h.runtimeConfig != nil {
		candidates = append(candidates, strings.TrimSpace(h.runtimeConfig.Workspace.Root))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, strings.TrimSpace(cwd))
	}

	var lastErr error
	for _, raw := range candidates {
		if raw == "" {
			continue
		}
		abs, err := filepath.Abs(raw)
		if err != nil {
			lastErr = fmt.Errorf("resolve workspace_path: %w", err)
			continue
		}
		abs = filepath.Clean(abs)
		info, err := os.Stat(abs)
		if err != nil {
			lastErr = fmt.Errorf("workspace_path %q: %w", abs, err)
			continue
		}
		if !info.IsDir() {
			lastErr = fmt.Errorf("workspace_path %q is not a directory", abs)
			continue
		}
		return abs, nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("workspace_path is required")
}

func buildHarnessGrantsResponse(workspace string, store *runtimepolicy.FileGrantStore, action string, removed int) harnessGrantsResponse {
	grants := []harnessGrantDTO{}
	storePath := ""
	if store != nil {
		storePath = store.Path()
		for _, grant := range store.List() {
			grants = append(grants, harnessGrantDTO{
				Tool:    grant.Tool,
				Pattern: grant.Pattern,
				Scope:   grant.Scope,
			})
		}
	}
	return harnessGrantsResponse{
		WorkspacePath: workspace,
		StorePath:     storePath,
		Grants:        grants,
		Count:         len(grants),
		Action:        action,
		Removed:       removed,
	}
}

func noteToHarnessDTO(note memorystore.Note) harnessMemoryNoteDTO {
	dto := harnessMemoryNoteDTO{
		ID:        note.ID,
		Text:      note.Text,
		Tags:      append([]string(nil), note.Tags...),
		Source:    note.Source,
		SessionID: note.SessionID,
	}
	if !note.CreatedAt.IsZero() {
		dto.CreatedAt = note.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return dto
}

func packageToHarnessDTO(pkg *plugins.Package) harnessPluginDTO {
	if pkg == nil {
		return harnessPluginDTO{}
	}
	id := strings.TrimSpace(pkg.Manifest.Name)
	if id == "" {
		id = filepath.Base(pkg.Root)
	}
	return harnessPluginDTO{
		ID:          id,
		Name:        id,
		Version:     pkg.Manifest.Version,
		Description: pkg.Manifest.Description,
		Author:      pkg.Manifest.Author,
		Root:        pkg.Root,
		Trust:       string(pkg.Trust),
		Enabled:     pkg.Enabled,
		Active:      pkg.IsActive(),
		Warnings:    append([]string(nil), pkg.Warnings...),
	}
}

func parsePositiveIntQuery(r *http.Request, key string, fallback int) int {
	if r == nil {
		return fallback
	}
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
