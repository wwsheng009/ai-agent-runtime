package skill

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
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
	MetadataPath  string `yaml:"-" json:"metadata_path,omitempty"`
	Format        string `yaml:"-" json:"format,omitempty"`
	DiscoveryOnly bool   `yaml:"-" json:"-"`
}

// Skill 技能定义
type Skill struct {
	// 基本信息
	Name             string   `yaml:"name" json:"name"`
	Description      string   `yaml:"description" json:"description"`
	ShortDescription string   `yaml:"shortDescription,omitempty" json:"shortDescription,omitempty"`
	Version          string   `yaml:"version" json:"version"`
	Category         string   `yaml:"category" json:"category"`
	Capabilities     []string `yaml:"capabilities" json:"capabilities"`
	Tags             []string `yaml:"tags" json:"tags"`

	// 触发规则
	Triggers    []Trigger `yaml:"triggers" json:"triggers"`
	DirectRoute *bool     `yaml:"directRoute,omitempty" json:"directRoute,omitempty"`
	// ExecutionMode 控制 skill 的执行形态：
	//   "" / "auto"   -> 默认链路（Handler → Workflow → executeDefault）
	//   "model"       -> 交由模型选择程序
	//   "document"    -> 指令文档、无执行器（SK-7）
	ExecutionMode string `yaml:"execution_mode,omitempty" json:"execution_mode,omitempty"`

	// 工具列表 (引用 MCP tools)
	Tools []string `yaml:"tools" json:"tools"`

	// Prompt 模板
	SystemPrompt string `yaml:"systemPrompt,omitempty" json:"systemPrompt,omitempty"`
	UserPrompt   string `yaml:"userPrompt,omitempty" json:"userPrompt,omitempty"`
	Body         string `yaml:"-" json:"-"`

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
	// Codex-style metadata captured during discovery/hydration.
	Codex *CodexSkillMetadata `yaml:"-" json:"-"`
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

// AllowsDirectRoute reports whether a matched skill may run before the LLM.
// Tool workflows default to false because an incidental keyword match must not
// turn the complete user prompt into tool arguments. Prompt-only and custom
// handler skills retain the historical direct-routing behavior.
func (s *Skill) AllowsDirectRoute() bool {
	if s == nil {
		return false
	}
	if s.DirectRoute != nil {
		return *s.DirectRoute
	}
	return !s.HasWorkflow()
}

// IsDocumentMode 报告 skill 是否以"指令文档、无执行器"形态运行（SK-7）。
// 显式声明 execution_mode: document，或自动识别为 Codex 兼容技能且无
// handler/workflow（此时文档正文不再做为独立子调用的 system prompt）。
// 该方法不受配置灰度控制，仅反映 skill 自身声明与结构；调用处需用
// IsDocumentModeEnabled 在配置关闭时禁用自动识别（见 SK-7）。
func (s *Skill) IsDocumentMode() bool {
	return s.IsDocumentModeEnabled(true)
}

// IsDocumentModeEnabled 在配置约束下报告文档模式是否生效。
// autoEnabled 来自 skills.document_mode 配置（auto 时为 true）。
// 显式 execution_mode: document 恒生效；自动识别仅在 autoEnabled 时生效。
func (s *Skill) IsDocumentModeEnabled(autoEnabled bool) bool {
	if s == nil {
		return false
	}
	if s.ExecutionMode == ExecutionModeDocument {
		return true
	}
	if s.ExecutionMode != ExecutionModeAuto {
		return false
	}
	if !autoEnabled {
		return false
	}
	// 自动识别：Codex 格式、无自定义处理器、无工作流。
	return s.Source != nil && s.Source.Format == SkillSourceFormatCodex &&
		!s.HasCustomHandler() && !s.HasWorkflow()
}

// UserInvocable 报告技能是否允许用户显式调用（/skills 菜单、命令补全）。
// 仅 Codex 风格技能读取标准字段，其余形态默认允许。
func (s *Skill) UserInvocable() bool {
	if s == nil {
		return false
	}
	if s.Codex != nil {
		return s.Codex.UserInvocableEnabled()
	}
	return true
}

// ModelInvocable 报告技能是否允许被模型隐式选中（catalog / 函数暴露 / 路由）。
// disable-model-invocation 只影响隐式面，显式 /skill 调用仍可用。
func (s *Skill) ModelInvocable() bool {
	if s == nil {
		return false
	}
	if s.Codex != nil {
		return s.Codex.ImplicitInvocationAllowed()
	}
	return true
}

// SetSource 设置技能来源信息
func (s *Skill) SetSource(path, dir, layer string) {
	if s == nil {
		return
	}
	promptPath := ""
	metadataPath := ""
	format := SkillSourceFormatLegacy
	if s.Source != nil {
		promptPath = s.Source.PromptPath
		metadataPath = s.Source.MetadataPath
		if strings.TrimSpace(s.Source.Format) != "" {
			format = s.Source.Format
		}
	}
	s.Source = &SkillSource{
		Path:          path,
		Dir:           dir,
		Layer:         layer,
		PromptPath:    promptPath,
		MetadataPath:  metadataPath,
		Format:        format,
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
