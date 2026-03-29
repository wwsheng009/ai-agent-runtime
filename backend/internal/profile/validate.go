package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var (
	// ErrProfileNotFound means profile.yaml was not found.
	ErrProfileNotFound = errors.New("profile not found")
	// ErrAgentUnresolved means no agent could be selected.
	ErrAgentUnresolved = errors.New("profile agent could not be resolved")
	// ErrAgentNotFound means the requested/default agent is not declared or present.
	ErrAgentNotFound = errors.New("profile agent not found")
)

func normalizeRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("profile root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve profile root %s: %w", root, err)
	}
	return abs, nil
}

func resolveAgentID(requested string, spec *ProfileSpec, rootPaths Paths) (string, error) {
	if trimmed := strings.TrimSpace(requested); trimmed != "" {
		return trimmed, nil
	}
	if spec != nil {
		if trimmed := strings.TrimSpace(spec.Profile.DefaultAgent); trimmed != "" {
			return trimmed, nil
		}
	}
	candidates := detectedAgentIDs(spec, rootPaths)
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return "", fmt.Errorf("%w: explicit agent or profile.default_agent is required", ErrAgentUnresolved)
}

func detectedAgentIDs(spec *ProfileSpec, rootPaths Paths) []string {
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if spec != nil {
		for id := range spec.Agents {
			add(id)
		}
	}
	entries, err := os.ReadDir(rootPaths.AgentsDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				add(entry.Name())
			}
		}
	}
	sort.Strings(ids)
	return ids
}

func agentDeclaredOrPresent(id string, spec *ProfileSpec, paths AgentPaths) bool {
	if strings.TrimSpace(id) == "" {
		return false
	}
	if spec != nil {
		if _, ok := spec.Agent(id); ok {
			return true
		}
	}
	if dirExists(paths.Dir) {
		return true
	}
	return false
}
