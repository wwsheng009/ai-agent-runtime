package profile

// ProfileSpec is the root YAML model for profile.yaml.
type ProfileSpec struct {
	Profile   ProfileMetaSpec       `yaml:"profile" json:"profile"`
	Runtime   RuntimeSpec           `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Providers ProviderOverridesSpec `yaml:"providers,omitempty" json:"providers,omitempty"`
	MCP       MCPSpec               `yaml:"mcp,omitempty" json:"mcp,omitempty"`
	Skills    SkillsSpec            `yaml:"skills,omitempty" json:"skills,omitempty"`
	Tools     ToolPolicySpec        `yaml:"tools,omitempty" json:"tools,omitempty"`
	Agents    map[string]AgentSpec  `yaml:"agents,omitempty" json:"agents,omitempty"`
}

// ProfileMetaSpec contains profile metadata.
type ProfileMetaSpec struct {
	Name         string `yaml:"name,omitempty" json:"name,omitempty"`
	DefaultAgent string `yaml:"default_agent,omitempty" json:"default_agent,omitempty"`
}

// ProviderOverridesSpec contains profile-scoped provider defaults.
type ProviderOverridesSpec struct {
	DefaultProvider string            `yaml:"default_provider,omitempty" json:"default_provider,omitempty"`
	ModelAliases    map[string]string `yaml:"model_aliases,omitempty" json:"model_aliases,omitempty"`
}

// RuntimeSpec contains profile-scoped runtime declarations.
type RuntimeSpec struct {
	Overrides map[string]interface{} `yaml:"overrides,omitempty" json:"overrides,omitempty"`
	Extras    map[string]interface{} `yaml:",inline" json:"extras,omitempty"`
}

// MCPSpec contains profile-scoped MCP declarations.
type MCPSpec struct {
	MergeStrategy string                 `yaml:"merge_strategy,omitempty" json:"merge_strategy,omitempty"`
	Extras        map[string]interface{} `yaml:",inline" json:"extras,omitempty"`
}

// SkillsSpec contains profile-scoped skill declarations.
type SkillsSpec struct {
	Extras map[string]interface{} `yaml:",inline" json:"extras,omitempty"`
}

// AgentSpec contains inline and file-based agent settings.
type AgentSpec struct {
	Provider string         `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model    string         `yaml:"model,omitempty" json:"model,omitempty"`
	Tools    ToolPolicySpec `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// WorkspaceSpec contains workspace-level overrides.
type WorkspaceSpec struct {
	Provider string         `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model    string         `yaml:"model,omitempty" json:"model,omitempty"`
	Tools    ToolPolicySpec `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// ToolPolicySpec is the raw YAML model for tool policy declarations.
type ToolPolicySpec struct {
	Allowlist []string               `yaml:"allowlist,omitempty" json:"allowlist,omitempty"`
	Denylist  []string               `yaml:"denylist,omitempty" json:"denylist,omitempty"`
	ReadOnly  *bool                  `yaml:"read_only,omitempty" json:"read_only,omitempty"`
	Sandbox   map[string]interface{} `yaml:"sandbox,omitempty" json:"sandbox,omitempty"`
}

// Agent returns the inline profile agent spec, if present.
func (s *ProfileSpec) Agent(id string) (AgentSpec, bool) {
	if s == nil || len(s.Agents) == 0 {
		return AgentSpec{}, false
	}
	agent, ok := s.Agents[id]
	return agent, ok
}
