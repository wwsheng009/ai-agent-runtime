package capability

import "github.com/ai-gateway/ai-agent-runtime/internal/types"

// Kind 统一能力类型
type Kind string

const (
	KindSkill    Kind = "skill"
	KindTool     Kind = "tool"
	KindWorkflow Kind = "workflow"
	KindAgent    Kind = "agent"
)

// Source 能力来源信息
type Source struct {
	Path  string `json:"path,omitempty"`
	Dir   string `json:"dir,omitempty"`
	Layer string `json:"layer,omitempty"`
}

// Trigger 能力触发规则
type Trigger struct {
	Type   string   `json:"type"`
	Values []string `json:"values,omitempty"`
	Weight float64  `json:"weight,omitempty"`
}

// Dependency 能力依赖
type Dependency struct {
	Name        string                 `json:"name"`
	Kind        Kind                   `json:"kind"`
	Description string                 `json:"description,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// Descriptor 统一能力描述
type Descriptor struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Kind         Kind                   `json:"kind"`
	Description  string                 `json:"description,omitempty"`
	Version      string                 `json:"version,omitempty"`
	Category     string                 `json:"category,omitempty"`
	Labels       []string               `json:"labels,omitempty"`
	Capabilities []string               `json:"capabilities,omitempty"`
	Triggers     []Trigger              `json:"triggers,omitempty"`
	Dependencies []Dependency           `json:"dependencies,omitempty"`
	Source       *Source                `json:"source,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

// Candidate 路由候选
type Candidate struct {
	Descriptor *Descriptor `json:"descriptor,omitempty"`
	Score      float64     `json:"score"`
	MatchedBy  string      `json:"matched_by,omitempty"`
	Details    string      `json:"details,omitempty"`
}

// Result 统一能力执行结果
type Result struct {
	Descriptor   *Descriptor            `json:"descriptor,omitempty"`
	Source       string                 `json:"source,omitempty"`
	Success      bool                   `json:"success"`
	Output       string                 `json:"output,omitempty"`
	Error        string                 `json:"error,omitempty"`
	Observations []types.Observation    `json:"observations,omitempty"`
	Usage        *types.TokenUsage      `json:"usage,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

// Provider 返回统一能力清单
type Provider interface {
	CapabilityDescriptors() []*Descriptor
}
