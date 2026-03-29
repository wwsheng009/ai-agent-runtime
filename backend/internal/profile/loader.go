package profile

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadProfile loads the required profile.yaml file.
func LoadProfile(root string) (*ProfileSpec, error) {
	paths := ResolveProfilePaths(root)
	spec := &ProfileSpec{}
	if err := loadYAMLFile(paths.ProfileFile, spec); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrProfileNotFound, paths.ProfileFile)
		}
		return nil, err
	}
	return spec, nil
}

// LoadAgent loads agents/<agent>/agent.yaml when present.
func LoadAgent(root, agent string) (*AgentSpec, error) {
	paths := ResolveAgentPaths(root, agent)
	spec := &AgentSpec{}
	ok, err := loadOptionalYAMLFile(paths.ConfigFile, spec)
	if err != nil || !ok {
		return nil, err
	}
	return spec, nil
}

// LoadWorkspace loads agents/<agent>/workspace/workspace.yaml when present.
func LoadWorkspace(root, agent string) (*WorkspaceSpec, error) {
	paths := ResolveAgentPaths(root, agent)
	spec := &WorkspaceSpec{}
	ok, err := loadOptionalYAMLFile(paths.WorkspaceConfigFile, spec)
	if err != nil || !ok {
		return nil, err
	}
	return spec, nil
}

// LoadToolPolicy loads agents/<agent>/tools/policy.yaml when present.
func LoadToolPolicy(root, agent string) (*ToolPolicySpec, error) {
	paths := ResolveAgentPaths(root, agent)
	spec := &ToolPolicySpec{}
	ok, err := loadOptionalYAMLFile(paths.ToolPolicyFile, spec)
	if err != nil || !ok {
		return nil, err
	}
	return spec, nil
}

func loadOptionalYAMLFile(path string, target interface{}) (bool, error) {
	if !fileExists(path) {
		return false, nil
	}
	if err := loadYAMLFile(path, target); err != nil {
		return false, err
	}
	return true, nil
}

func loadYAMLFile(path string, target interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse yaml %s: %w", path, err)
	}
	return nil
}
