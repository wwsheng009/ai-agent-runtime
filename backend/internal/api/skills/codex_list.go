package skills

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

type codexSkillsListRequest struct {
	Cwd                  string              `json:"cwd,omitempty"`
	Cwds                 []string            `json:"cwds,omitempty"`
	ConfigFile           string              `json:"config_file,omitempty"`
	ExtraRoots           []string            `json:"extra_roots,omitempty"`
	PerCwdExtraUserRoots map[string][]string `json:"per_cwd_extra_user_roots,omitempty"`
	ForceReload          bool                `json:"force_reload,omitempty"`
}

type codexSkillsListGroup struct {
	Cwd    string                      `json:"cwd"`
	Roots  []string                    `json:"roots,omitempty"`
	Skills []*skill.CodexSkillMetadata `json:"skills,omitempty"`
	Errors []skill.CodexSkillError     `json:"errors,omitempty"`
	Count  int                         `json:"count"`
}

type codexSkillsListResponse struct {
	Results     []codexSkillsListGroup `json:"results"`
	Count       int                    `json:"count"`
	GroupCount  int                    `json:"group_count"`
	ForceReload bool                   `json:"force_reload"`
	CacheHit    bool                   `json:"cache_hit,omitempty"`
}

type codexSkillsListPlan struct {
	Cwd        string
	Roots      []string
	ExtraRoots []string
}

// ListCodexSkills 返回 Codex 风格的 skills 发现结果，按 cwd 分组并携带 errors。
func (h *Handler) ListCodexSkills(w http.ResponseWriter, r *http.Request) {
	req, err := decodeCodexSkillsListRequest(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}

	cwds := resolveCodexListCwds(r, req)
	if len(cwds) == 0 {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "failed to resolve current working directory", cwdErr))
			return
		}
		cwds = []string{cwd}
	}

	configFile := strings.TrimSpace(req.ConfigFile)
	if configFile == "" {
		configFile = strings.TrimSpace(h.runtimeConfigFile)
	}
	if configFile != "" {
		configFile = resolveCodexListUpwardPath(configFile)
	}

	plans := make([]codexSkillsListPlan, 0, len(cwds))
	for _, cwd := range cwds {
		anchor := resolveCodexListCwdPath(cwd)
		extraRoots := resolveCodexListExtraRoots(anchor, req, r)
		roots := resolveCodexListRoots(anchor, configFile, extraRoots)
		plans = append(plans, codexSkillsListPlan{
			Cwd:        anchor,
			Roots:      roots,
			ExtraRoots: extraRoots,
		})
	}

	cacheVersion := h.currentCodexSkillsListCacheVersion()
	cacheKey := buildCodexSkillsListCacheKey(configFile, plans)
	if !req.ForceReload {
		if cached, ok := h.getCodexSkillsListCache(cacheKey); ok {
			cached.ForceReload = false
			cached.CacheHit = true
			h.auditCodexSkillsList(r, cached)
			h.writeJSON(w, http.StatusOK, cached)
			return
		}
	}

	results := make([]codexSkillsListGroup, 0, len(plans))
	total := 0
	for _, plan := range plans {
		outcome := skill.DiscoverCodexSkillLoadOutcome(plan.Cwd, configFile, plan.ExtraRoots)
		group := codexSkillsListGroup{
			Cwd:    plan.Cwd,
			Roots:  plan.Roots,
			Skills: outcome.Skills,
			Errors: outcome.Errors,
			Count:  len(outcome.Skills),
		}
		results = append(results, group)
		total += len(outcome.Skills)
	}

	response := codexSkillsListResponse{
		Results:     results,
		Count:       total,
		GroupCount:  len(results),
		ForceReload: req.ForceReload,
		CacheHit:    false,
	}
	h.setCodexSkillsListCache(cacheKey, response, cacheVersion)
	h.auditCodexSkillsList(r, response)
	h.writeJSON(w, http.StatusOK, response)
}

func (h *Handler) auditCodexSkillsList(r *http.Request, response codexSkillsListResponse) {
	outcome := "cache_miss"
	if response.CacheHit {
		outcome = "cache_hit"
	}

	requestID := ""
	if r != nil {
		requestID = logger.GetRequestID(r.Context())
	}

	fields := []interface{}{
		logger.String("action", "skills_list"),
		logger.String("outcome", outcome),
		logger.Bool("cache_hit", response.CacheHit),
		logger.Bool("force_reload", response.ForceReload),
		logger.Int("count", response.Count),
		logger.Int("group_count", response.GroupCount),
	}
	if requestID != "" {
		fields = append(fields, logger.RequestID(requestID))
	}

	logger.Admin().Named("codex_skills_list").Info("codex skills list", fieldsToZap(fields)...)
}

func decodeCodexSkillsListRequest(r *http.Request) (*codexSkillsListRequest, error) {
	req := &codexSkillsListRequest{}
	if r == nil {
		return req, nil
	}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil && err != io.EOF {
		return nil, errors.New(errors.ErrValidationFailed, "failed to parse request body")
	}
	if req.ForceReload == false {
		if forceReload, specified := queryBoolFlag(r, "force_reload"); specified {
			req.ForceReload = forceReload
		}
	}
	if strings.TrimSpace(req.ConfigFile) == "" && r.URL != nil {
		req.ConfigFile = strings.TrimSpace(r.URL.Query().Get("config_file"))
	}
	if strings.TrimSpace(req.Cwd) == "" && len(req.Cwds) == 0 && r.URL != nil {
		if rawCwds := r.URL.Query()["cwd"]; len(rawCwds) > 0 {
			req.Cwds = append(req.Cwds, rawCwds...)
		}
	}
	return req, nil
}

func resolveCodexListCwds(r *http.Request, req *codexSkillsListRequest) []string {
	seen := make(map[string]struct{})
	cwds := make([]string, 0, 1+len(req.Cwds))
	add := func(value string) {
		value = resolveCodexListCwdPath(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		cwds = append(cwds, value)
	}

	add(req.Cwd)
	for _, cwd := range req.Cwds {
		add(cwd)
	}

	if len(cwds) == 0 && r != nil && r.URL != nil {
		add(r.URL.Query().Get("cwd"))
	}

	return cwds
}

func resolveCodexListCwdPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	resolved := resolveCodexListUpwardPath(path)
	if resolved == "" {
		resolved = path
	}
	resolved = filepath.Clean(resolved)
	if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
		resolved = filepath.Dir(resolved)
	}
	return filepath.Clean(resolved)
}

func resolveCodexListRoots(cwd, configFile string, extraRoots []string) []string {
	roots := skill.DiscoverCodexCompatibleSkillDirs(cwd, configFile)
	roots = append(roots, extraRoots...)
	return dedupeCodexListRoots(roots)
}

func resolveCodexListExtraRoots(cwd string, req *codexSkillsListRequest, r *http.Request) []string {
	extraRoots := make([]string, 0, 8)
	if req != nil {
		extraRoots = append(extraRoots, req.ExtraRoots...)
		if len(req.PerCwdExtraUserRoots) > 0 {
			if cwdRoots, ok := req.PerCwdExtraUserRoots[cwd]; ok {
				extraRoots = append(extraRoots, cwdRoots...)
			}
			if cwdRoots, ok := req.PerCwdExtraUserRoots[filepath.Clean(cwd)]; ok {
				extraRoots = append(extraRoots, cwdRoots...)
			}
		}
	}
	if r != nil && r.URL != nil {
		if rawRoots := r.URL.Query()["extra_root"]; len(rawRoots) > 0 {
			extraRoots = append(extraRoots, rawRoots...)
		}
	}
	return resolveCodexListExtraRootsFromValues(cwd, extraRoots)
}

func resolveCodexListExtraRootsFromValues(cwd string, roots []string) []string {
	if len(roots) == 0 {
		return nil
	}
	resolved := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if !filepath.IsAbs(root) && cwd != "" {
			root = filepath.Join(cwd, root)
		}
		root = resolveCodexListUpwardPath(root)
		if root == "" {
			continue
		}
		if info, err := os.Stat(root); err == nil && !info.IsDir() {
			root = filepath.Dir(root)
		}
		root = filepath.Clean(root)
		if root == "" || root == "." {
			continue
		}
		resolved = append(resolved, root)
	}
	return resolved
}

func resolveCodexListUpwardPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}

	cleaned := filepath.Clean(trimmed)
	if filepath.IsAbs(cleaned) {
		return cleaned
	}
	if pathExistsForCodexList(cleaned) {
		return cleaned
	}

	relative := strings.TrimPrefix(cleaned, "."+string(filepath.Separator))
	if relative == "." || relative == "" {
		return cleaned
	}

	cwd, err := os.Getwd()
	if err != nil {
		return cleaned
	}

	for dir := cwd; dir != ""; {
		candidate := filepath.Join(dir, relative)
		if pathExistsForCodexList(candidate) {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return cleaned
}

func pathExistsForCodexList(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func dedupeCodexListRoots(roots []string) []string {
	seen := make(map[string]struct{})
	deduped := make([]string, 0, len(roots))
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" || root == "." {
			continue
		}
		if _, exists := seen[root]; exists {
			continue
		}
		seen[root] = struct{}{}
		deduped = append(deduped, root)
	}
	return deduped
}
