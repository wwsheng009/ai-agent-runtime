package skills

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

type codexSkillsListCacheKey struct {
	ConfigFile string                         `json:"config_file"`
	Groups     []codexSkillsListCacheGroupKey `json:"groups"`
}

type codexSkillsListCacheGroupKey struct {
	Cwd   string   `json:"cwd"`
	Roots []string `json:"roots"`
}

func buildCodexSkillsListCacheKey(configFile string, plans []codexSkillsListPlan) string {
	key := codexSkillsListCacheKey{
		ConfigFile: normalizeCodexSkillsListCachePath(configFile),
		Groups:     make([]codexSkillsListCacheGroupKey, 0, len(plans)),
	}
	for _, plan := range plans {
		key.Groups = append(key.Groups, codexSkillsListCacheGroupKey{
			Cwd:   normalizeCodexSkillsListCachePath(plan.Cwd),
			Roots: normalizeCodexSkillsListCachePaths(plan.Roots),
		})
	}

	data, err := json.Marshal(key)
	if err != nil {
		return ""
	}
	return string(data)
}

func normalizeCodexSkillsListCachePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func normalizeCodexSkillsListCachePaths(paths []string) []string {
	if len(paths) == 0 {
		return []string{}
	}
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		if normalizedPath := normalizeCodexSkillsListCachePath(path); normalizedPath != "" {
			normalized = append(normalized, normalizedPath)
		}
	}
	return normalized
}

func (h *Handler) currentCodexSkillsListCacheVersion() uint64 {
	if h == nil {
		return 0
	}
	h.codexSkillsListMu.RLock()
	defer h.codexSkillsListMu.RUnlock()
	return h.codexSkillsListCacheVersion
}

func (h *Handler) getCodexSkillsListCache(key string) (codexSkillsListResponse, bool) {
	if h == nil {
		return codexSkillsListResponse{}, false
	}
	h.codexSkillsListMu.RLock()
	defer h.codexSkillsListMu.RUnlock()
	if h.codexSkillsListCache == nil {
		return codexSkillsListResponse{}, false
	}
	response, ok := h.codexSkillsListCache[key]
	if !ok {
		return codexSkillsListResponse{}, false
	}
	return cloneCodexSkillsListResponse(response), true
}

func (h *Handler) setCodexSkillsListCache(key string, response codexSkillsListResponse, requestVersion uint64) {
	if h == nil || key == "" {
		return
	}

	cached := cloneCodexSkillsListResponse(response)
	cached.ForceReload = false

	h.codexSkillsListMu.Lock()
	defer h.codexSkillsListMu.Unlock()
	if requestVersion != h.codexSkillsListCacheVersion {
		return
	}
	if h.codexSkillsListCache == nil {
		h.codexSkillsListCache = make(map[string]codexSkillsListResponse)
	}
	h.codexSkillsListCache[key] = cached
}

func (h *Handler) invalidateCodexSkillsListCache() {
	if h == nil {
		return
	}

	h.codexSkillsListMu.Lock()
	defer h.codexSkillsListMu.Unlock()
	h.codexSkillsListCacheVersion++
	h.codexSkillsListCache = make(map[string]codexSkillsListResponse)
}

func cloneCodexSkillsListResponse(response codexSkillsListResponse) codexSkillsListResponse {
	cloned := response
	if response.Results == nil {
		cloned.Results = nil
		return cloned
	}

	cloned.Results = make([]codexSkillsListGroup, len(response.Results))
	for index, group := range response.Results {
		cloned.Results[index] = cloneCodexSkillsListGroup(group)
	}
	return cloned
}

func cloneCodexSkillsListGroup(group codexSkillsListGroup) codexSkillsListGroup {
	cloned := group
	if group.Roots != nil {
		cloned.Roots = append([]string(nil), group.Roots...)
	}
	if group.Skills != nil {
		cloned.Skills = make([]*skill.CodexSkillMetadata, len(group.Skills))
		for index, item := range group.Skills {
			if item == nil {
				continue
			}
			cloned.Skills[index] = item.CloneWithoutBody()
		}
	}
	if group.Errors != nil {
		cloned.Errors = append([]skill.CodexSkillError(nil), group.Errors...)
	}
	return cloned
}
