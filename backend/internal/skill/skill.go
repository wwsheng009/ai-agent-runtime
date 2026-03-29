package skill

import (
	"github.com/ai-gateway/ai-agent-runtime/internal/types"
)

const (
	SkillSourceLayerUnknown  = "unknown"
	SkillSourceLayerSystem   = "system"
	SkillSourceLayerExternal = "external"
	SkillSourceLayerRuntime  = "runtime"
)

// SkillSource 技能来源信息
type SkillSource struct {
	Path          string `yaml:"-" json:"path,omitempty"`
	Dir           string `yaml:"-" json:"dir,omitempty"`
	Layer         string `yaml:"-" json:"layer,omitempty"`
	PromptPath    string `yaml:"-" json:"prompt_path,omitempty"`
	DiscoveryOnly bool   `yaml:"-" json:"-"`
}

// Skill 技能定义
type Skill struct {
	// 基本信息
	Name         string   `yaml:"name" json:"name"`
	Description  string   `yaml:"description" json:"description"`
	Version      string   `yaml:"version" json:"version"`
	Category     string   `yaml:"category" json:"category"`
	Capabilities []string `yaml:"capabilities" json:"capabilities"`
	Tags         []string `yaml:"tags" json:"tags"`

	// 触发规则
	Triggers []Trigger `yaml:"triggers" json:"triggers"`

	// 工具列表 (引用 MCP tools)
	Tools []string `yaml:"tools" json:"tools"`

	// Prompt 模板
	SystemPrompt string `yaml:"systemPrompt,omitempty" json:"systemPrompt,omitempty"`
	UserPrompt   string `yaml:"userPrompt,omitempty" json:"userPrompt,omitempty"`

	// 工作流定义 (可选)
	Workflow *Workflow `yaml:"workflow,omitempty" json:"workflow,omitempty"`

	// 上下文注入
	Context ContextConfig `yaml:"context" json:"context"`

	// 权限要求
	Permissions []string `yaml:"permissions" json:"permissions"`

	// 自定义处理器 (用于内置 Skills)
	Handler SkillHandler `yaml:"-" json:"-"`

	// 运行时来源信息
	Source *SkillSource `yaml:"-" json:"source,omitempty"`
}

// Trigger 触发规则
type Trigger struct {
	Type   string   `yaml:"type" json:"type"`     // keyword | pattern | embedding
	Values []string `yaml:"values" json:"values"` // 匹配值
	Weight float64  `yaml:"weight" json:"weight"` // 权重
}

// Workflow 工作流定义
type Workflow struct {
	Steps []WorkflowStep `yaml:"steps" json:"steps"`
}

// WorkflowStep 工作流步骤
type WorkflowStep struct {
	ID        string                 `yaml:"id" json:"id"`
	Name      string                 `yaml:"name" json:"name"`
	Tool      string                 `yaml:"tool" json:"tool"`
	Args      map[string]interface{} `yaml:"args" json:"args"`
	DependsOn []string               `yaml:"dependsOn" json:"dependsOn"`
	Condition string                 `yaml:"condition,omitempty" json:"condition,omitempty"`
}

// ContextConfig 上下文配置
type ContextConfig struct {
	Files       []string `yaml:"files" json:"files"`
	Environment []string `yaml:"environment" json:"environment"`
	Symbols     []string `yaml:"symbols" json:"symbols"`
}

// SkillHandler 技能处理器接口
type SkillHandler interface {
	Execute(ctx interface{}, req *types.Request) (*types.Result, error)
}

// SkillHandlerFunc 技能处理器函数类型
type SkillHandlerFunc func(ctx interface{}, req *types.Request) (*types.Result, error)

// Execute 实现 SkillHandler 接口
func (f SkillHandlerFunc) Execute(ctx interface{}, req *types.Request) (*types.Result, error) {
	return f(ctx, req)
}

// HasWorkflow 检查是否有工作流
func (s *Skill) HasWorkflow() bool {
	return s.Workflow != nil && len(s.Workflow.Steps) > 0
}

// HasCustomHandler 检查是否有自定义处理器
func (s *Skill) HasCustomHandler() bool {
	return s.Handler != nil
}

// SetSource 设置技能来源信息
func (s *Skill) SetSource(path, dir, layer string) {
	if s == nil {
		return
	}
	promptPath := ""
	if s.Source != nil {
		promptPath = s.Source.PromptPath
	}
	s.Source = &SkillSource{
		Path:          path,
		Dir:           dir,
		Layer:         layer,
		PromptPath:    promptPath,
		DiscoveryOnly: false,
	}
}

// SetSourceLayer 更新来源层级
func (s *Skill) SetSourceLayer(layer string) {
	if s == nil {
		return
	}
	if s.Source == nil {
		s.Source = &SkillSource{}
	}
	s.Source.Layer = layer
}

// SetPromptSource 设置 companion prompt 来源文件
func (s *Skill) SetPromptSource(path string) {
	if s == nil {
		return
	}
	if s.Source == nil {
		s.Source = &SkillSource{}
	}
	s.Source.PromptPath = path
}
