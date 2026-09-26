package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file implements the layered permission configuration of
// docs/analysis/commandcode-permissions-design-borrowing-20260926.md §4.9:
//
//	~/.aicli/permissions.yaml                 (user, global)
//	<project>/.aicli/permissions.yaml         (project, shared)
//	<project>/.aicli/permissions.local.yaml   (local, personal + gitignored)
//
// Rules accumulate in that escalation order (first match still wins, so a
// user-level deny is evaluated before a project-level allow), deny_tools /
// allow_tools are monotonic unions, and disable_bypass is OR-ed across layers.

// Permission layer scopes.
const (
	PermissionsScopeUser    = "user"
	PermissionsScopeProject = "project"
	PermissionsScopeLocal   = "local"
)

// Local permissions file names under <project>/.aicli/ (personal, gitignored).
const (
	LocalPermissionsFileName    = "permissions.local.yaml"
	localPermissionsFileNameYML = "permissions.local.yml"
)

// PermissionsLayer is one resolved layer file and its parsed content.
type PermissionsLayer struct {
	Scope string
	Path  string
	File  *PermissionsFile
}

// ResolvePermissionsLayerPaths returns the existing layer files in escalation
// order user → project → local. Missing files and a missing project root are
// not errors.
func ResolvePermissionsLayerPaths(projectRoot string) ([]PermissionsLayer, error) {
	layers := make([]PermissionsLayer, 0, 3)
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		path, err := firstExistingPermissionsFile(filepath.Join(home, ".aicli"))
		if err != nil {
			return nil, err
		}
		if path != "" {
			layers = append(layers, PermissionsLayer{Scope: PermissionsScopeUser, Path: path})
		}
	}
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot != "" {
		dir := filepath.Join(projectRoot, ".aicli")
		path, err := firstExistingPermissionsFile(dir)
		if err != nil {
			return nil, err
		}
		if path != "" {
			layers = append(layers, PermissionsLayer{Scope: PermissionsScopeProject, Path: path})
		}
		path, err = firstExistingLocalPermissionsFile(dir)
		if err != nil {
			return nil, err
		}
		if path != "" {
			layers = append(layers, PermissionsLayer{Scope: PermissionsScopeLocal, Path: path})
		}
	}
	return layers, nil
}

// LoadLayeredPermissions loads and accumulates every permission layer. It
// returns the merged file plus the resolved layers (for `/debug` and logs).
// A nil merged file means no layer existed.
func LoadLayeredPermissions(projectRoot string) (*PermissionsFile, []PermissionsLayer, error) {
	layers, err := ResolvePermissionsLayerPaths(projectRoot)
	if err != nil {
		return nil, nil, err
	}
	for i := range layers {
		file, err := LoadPermissionsFile(layers[i].Path)
		if err != nil {
			return nil, nil, err
		}
		layers[i].File = file
	}
	return MergePermissionsLayers(layers), layers, nil
}

// MergePermissionsLayers accumulates layers into one PermissionsFile:
// deny/allow tool lists are unions (deny stays monotonic), rules keep layer
// order with a `<scope>/` name prefix, and disable_bypass is OR-ed.
func MergePermissionsLayers(layers []PermissionsLayer) *PermissionsFile {
	merged := &PermissionsFile{}
	for _, layer := range layers {
		if layer.File == nil {
			continue
		}
		if merged.Version == 0 {
			merged.Version = layer.File.Version
		}
		merged.DenyTools = uniqueToolNames(append(merged.DenyTools, layer.File.DenyTools...))
		merged.AllowTools = uniqueToolNames(append(merged.AllowTools, layer.File.AllowTools...))
		for _, rule := range layer.File.Rules {
			name := strings.TrimSpace(rule.Name)
			if name == "" {
				name = fmt.Sprintf("rule_%d", len(merged.Rules))
			}
			rule.Name = layer.Scope + "/" + name
			merged.Rules = append(merged.Rules, rule)
		}
		merged.DisableBypass = merged.DisableBypass || layer.File.DisableBypass
		merged.LayerPaths = append(merged.LayerPaths, layer.Path)
	}
	if len(merged.LayerPaths) == 0 {
		return nil
	}
	return merged
}

// firstExistingPermissionsFile resolves permissions.yaml → permissions.yml.
func firstExistingPermissionsFile(dir string) (string, error) {
	return firstExistingFileIn(dir, []string{DefaultPermissionsFileName, permissionsFileNameYML})
}

// firstExistingLocalPermissionsFile resolves the local layer candidates.
func firstExistingLocalPermissionsFile(dir string) (string, error) {
	return firstExistingFileIn(dir, []string{LocalPermissionsFileName, localPermissionsFileNameYML})
}

func firstExistingFileIn(dir string, names []string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", nil
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("stat permissions file %s: %w", path, err)
		}
		if info.IsDir() {
			continue
		}
		return path, nil
	}
	return "", nil
}
