package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
	"gopkg.in/yaml.v3"
)

type skillsRuntimePolicyPersister struct {
	mu         sync.Mutex
	configPath string
	cfg        *config.Config
}

func newSkillsRuntimePolicyPersister(configPath string, cfg *config.Config) *skillsRuntimePolicyPersister {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" || cfg == nil {
		return nil
	}
	return &skillsRuntimePolicyPersister{
		configPath: configPath,
		cfg:        cfg,
	}
}

func (p *skillsRuntimePolicyPersister) persistAuthPolicy(policy skillsapi.ScopeResolverConfig, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	next := cloneSkillsRuntimeConfig(p.cfg.SkillsRuntime)
	next.ScopeResolverEnabled = policy.Enabled
	next.JWTClaimsEnabled = policy.JWTClaimsEnabled
	next.TenantHeaders = append([]string(nil), policy.TenantHeaders...)
	next.ProjectHeaders = append([]string(nil), policy.ProjectHeaders...)
	next.UserHeaders = append([]string(nil), policy.UserHeaders...)
	next.RoleHeaders = append([]string(nil), policy.RoleHeaders...)
	next.TenantClaims = append([]string(nil), policy.TenantClaims...)
	next.ProjectClaims = append([]string(nil), policy.ProjectClaims...)
	next.UserClaims = append([]string(nil), policy.UserClaims...)
	next.RoleClaims = append([]string(nil), policy.RoleClaims...)
	next.AdminRoles = append([]string(nil), policy.AdminRoles...)
	next.APIKeyScopes = buildConfigScopeBindings(policy.APIKeyScopes)

	if err := persistSkillsRuntimeConfigSection(p.configPath, next); err != nil {
		return err
	}
	p.cfg.SkillsRuntime = next
	return nil
}

func (p *skillsRuntimePolicyPersister) persistUsagePolicy(policy skillsapi.UsagePolicy, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	next := cloneSkillsRuntimeConfig(p.cfg.SkillsRuntime)
	next.UsageTrackingEnabled = policy.TrackingEnabled
	next.QuotaEnabled = policy.QuotaEnabled
	next.DefaultMaxRequests = policy.DefaultMaxRequests
	next.DefaultMaxTokens = policy.DefaultMaxTokens
	next.QuotaPolicies = config.SkillsRuntimeQuotaPolicies{
		Tenants:  buildConfigQuotaLimits(policy.TenantQuotas),
		Projects: buildConfigQuotaLimits(policy.ProjectQuotas),
		Users:    buildConfigQuotaLimits(policy.UserQuotas),
	}

	if err := persistSkillsRuntimeConfigSection(p.configPath, next); err != nil {
		return err
	}
	p.cfg.SkillsRuntime = next
	return nil
}

func (p *skillsRuntimePolicyPersister) persistMutationPolicy(policy skillsapi.MutationPolicy, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	next := cloneSkillsRuntimeConfig(p.cfg.SkillsRuntime)
	next.ReadOnly = policy.ReadOnly
	next.DisableImport = policy.DisableImport
	next.DisablePersist = policy.DisablePersist
	next.DisableReloadOps = policy.DisableReloadOps
	next.DisableHotReloadOps = policy.DisableHotReload

	if err := persistSkillsRuntimeConfigSection(p.configPath, next); err != nil {
		return err
	}
	p.cfg.SkillsRuntime = next
	return nil
}

func persistSkillsRuntimeConfigSection(configPath string, skillsRuntime *config.SkillsRuntimeConfig) error {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return fmt.Errorf("config path is required")
	}

	ext := strings.ToLower(filepath.Ext(configPath))
	switch ext {
	case ".yaml", ".yml", "":
		return persistSkillsRuntimeYAML(configPath, skillsRuntime)
	case ".json":
		return persistSkillsRuntimeJSON(configPath, skillsRuntime)
	default:
		return fmt.Errorf("unsupported config format: %s", ext)
	}
}

func persistSkillsRuntimeYAML(configPath string, skillsRuntime *config.SkillsRuntimeConfig) error {
	raw, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config file: %w", err)
	}

	var document yaml.Node
	if len(bytes.TrimSpace(raw)) == 0 {
		document = yaml.Node{
			Kind: yaml.DocumentNode,
			Content: []*yaml.Node{{
				Kind: yaml.MappingNode,
			}},
		}
	} else if err := yaml.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("parse config yaml: %w", err)
	}

	root, err := ensureYAMLRootMapping(&document)
	if err != nil {
		return err
	}
	sectionNode, err := marshalYAMLValueNode(skillsRuntime)
	if err != nil {
		return err
	}
	upsertYAMLMappingValue(root, "skills_runtime", sectionNode)

	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		_ = encoder.Close()
		return fmt.Errorf("encode config yaml: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("finalize config yaml: %w", err)
	}

	return writeFilePreserveMode(configPath, output.Bytes())
}

func persistSkillsRuntimeJSON(configPath string, skillsRuntime *config.SkillsRuntimeConfig) error {
	raw, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config file: %w", err)
	}

	root := make(map[string]interface{})
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &root); err != nil {
			return fmt.Errorf("parse config json: %w", err)
		}
	}
	root["skills_runtime"] = skillsRuntime

	output, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config json: %w", err)
	}
	output = append(output, '\n')
	return writeFilePreserveMode(configPath, output)
}

func ensureYAMLRootMapping(document *yaml.Node) (*yaml.Node, error) {
	if document.Kind == 0 {
		document.Kind = yaml.DocumentNode
	}
	if document.Kind != yaml.DocumentNode {
		return nil, fmt.Errorf("config root must be a yaml document")
	}
	if len(document.Content) == 0 {
		document.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	root := document.Content[0]
	if root.Kind == 0 {
		root.Kind = yaml.MappingNode
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config root must be a mapping")
	}
	return root, nil
}

func marshalYAMLValueNode(value interface{}) (*yaml.Node, error) {
	raw, err := yaml.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal skills runtime section: %w", err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode skills runtime section: %w", err)
	}
	if len(document.Content) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode}, nil
	}
	return document.Content[0], nil
}

func upsertYAMLMappingValue(root *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			root.Content[i+1] = value
			return
		}
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

func writeFilePreserveMode(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create config directory: %w", err)
		}
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}
	return nil
}

func cloneSkillsRuntimeConfig(current *config.SkillsRuntimeConfig) *config.SkillsRuntimeConfig {
	if current == nil {
		return &config.SkillsRuntimeConfig{}
	}

	raw, err := json.Marshal(current)
	if err != nil {
		cloned := *current
		return &cloned
	}

	var cloned config.SkillsRuntimeConfig
	if err := json.Unmarshal(raw, &cloned); err != nil {
		copyValue := *current
		return &copyValue
	}
	return &cloned
}

func buildConfigScopeBindings(configured map[string]skillsapi.UsageScope) map[string]config.SkillsRuntimeScopeBinding {
	if len(configured) == 0 {
		return nil
	}
	bindings := make(map[string]config.SkillsRuntimeScopeBinding, len(configured))
	for key, value := range configured {
		bindings[key] = config.SkillsRuntimeScopeBinding{
			TenantID:  value.TenantID,
			ProjectID: value.ProjectID,
			UserID:    value.UserID,
		}
	}
	return bindings
}

func buildConfigQuotaLimits(configured map[string]skillsapi.UsageQuotaLimit) map[string]config.SkillsRuntimeQuotaLimit {
	if len(configured) == 0 {
		return nil
	}
	limits := make(map[string]config.SkillsRuntimeQuotaLimit, len(configured))
	for key, value := range configured {
		limits[key] = config.SkillsRuntimeQuotaLimit{
			MaxRequests: value.MaxRequests,
			MaxTokens:   value.MaxTokens,
		}
	}
	return limits
}
