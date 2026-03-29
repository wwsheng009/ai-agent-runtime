package skill

// WorkflowStepSummary 保留 workflow discovery 所需的最小步骤信息。
type WorkflowStepSummary struct {
	ID        string                 `yaml:"id" json:"id"`
	Name      string                 `yaml:"name,omitempty" json:"name,omitempty"`
	Tool      string                 `yaml:"tool" json:"tool"`
	Args      map[string]interface{} `yaml:"args,omitempty" json:"args,omitempty"`
	DependsOn []string               `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	Condition string                 `yaml:"condition,omitempty" json:"condition,omitempty"`
}

// SkillSummary 表示 discovery 阶段保留的轻量技能摘要。
type SkillSummary struct {
	Name              string                `yaml:"name" json:"name"`
	Description       string                `yaml:"description" json:"description"`
	Version           string                `yaml:"version,omitempty" json:"version,omitempty"`
	Category          string                `yaml:"category,omitempty" json:"category,omitempty"`
	Capabilities      []string              `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Tags              []string              `yaml:"tags,omitempty" json:"tags,omitempty"`
	Triggers          []Trigger             `yaml:"triggers" json:"triggers"`
	Tools             []string              `yaml:"tools,omitempty" json:"tools,omitempty"`
	Context           ContextConfig         `yaml:"context,omitempty" json:"context,omitempty"`
	Permissions       []string              `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	WorkflowSteps     []WorkflowStepSummary `yaml:"workflow_steps,omitempty" json:"workflow_steps,omitempty"`
	WorkflowStepCount int                   `yaml:"workflow_step_count,omitempty" json:"workflow_step_count,omitempty"`
	Handler           SkillHandler          `yaml:"-" json:"-"`
	Source            *SkillSource          `yaml:"-" json:"source,omitempty"`
}

// HasWorkflow 返回摘要是否声明 workflow。
func (s *SkillSummary) HasWorkflow() bool {
	if s == nil {
		return false
	}
	return s.WorkflowStepCount > 0 || len(s.WorkflowSteps) > 0
}

// SummaryFromSkill 从完整或 stub skill 派生轻量摘要。
func SummaryFromSkill(item *Skill) *SkillSummary {
	if item == nil {
		return nil
	}

	summary := &SkillSummary{
		Name:         item.Name,
		Description:  item.Description,
		Version:      item.Version,
		Category:     item.Category,
		Capabilities: append([]string(nil), item.Capabilities...),
		Tags:         append([]string(nil), item.Tags...),
		Triggers:     cloneTriggers(item.Triggers),
		Tools:        append([]string(nil), item.Tools...),
		Context: ContextConfig{
			Files:       append([]string(nil), item.Context.Files...),
			Environment: append([]string(nil), item.Context.Environment...),
			Symbols:     append([]string(nil), item.Context.Symbols...),
		},
		Permissions: append([]string(nil), item.Permissions...),
		Handler:     item.Handler,
	}

	if item.Workflow != nil && len(item.Workflow.Steps) > 0 {
		summary.WorkflowStepCount = len(item.Workflow.Steps)
		summary.WorkflowSteps = make([]WorkflowStepSummary, 0, len(item.Workflow.Steps))
		for _, step := range item.Workflow.Steps {
			summary.WorkflowSteps = append(summary.WorkflowSteps, WorkflowStepSummary{
				ID:        step.ID,
				Name:      step.Name,
				Tool:      step.Tool,
				Args:      cloneMapInterface(step.Args),
				DependsOn: append([]string(nil), step.DependsOn...),
				Condition: step.Condition,
			})
		}
	}

	if item.Source != nil {
		sourceCopy := *item.Source
		summary.Source = &sourceCopy
	}

	return summary
}

// ToSkillStub 将摘要转换为可用于 registry/router 的轻量 Skill。
func (s *SkillSummary) ToSkillStub() *Skill {
	if s == nil {
		return nil
	}

	stub := &Skill{
		Name:         s.Name,
		Description:  s.Description,
		Version:      s.Version,
		Category:     s.Category,
		Capabilities: append([]string(nil), s.Capabilities...),
		Tags:         append([]string(nil), s.Tags...),
		Triggers:     cloneTriggers(s.Triggers),
		Tools:        append([]string(nil), s.Tools...),
		Context: ContextConfig{
			Files:       append([]string(nil), s.Context.Files...),
			Environment: append([]string(nil), s.Context.Environment...),
			Symbols:     append([]string(nil), s.Context.Symbols...),
		},
		Permissions: append([]string(nil), s.Permissions...),
		Handler:     s.Handler,
	}

	if len(s.WorkflowSteps) > 0 {
		steps := make([]WorkflowStep, 0, len(s.WorkflowSteps))
		for _, step := range s.WorkflowSteps {
			steps = append(steps, WorkflowStep{
				ID:        step.ID,
				Name:      step.Name,
				Tool:      step.Tool,
				Args:      cloneMapInterface(step.Args),
				DependsOn: append([]string(nil), step.DependsOn...),
				Condition: step.Condition,
			})
		}
		stub.Workflow = &Workflow{Steps: steps}
	}

	if s.Source != nil {
		sourceCopy := *s.Source
		sourceCopy.DiscoveryOnly = true
		stub.Source = &sourceCopy
	}

	return stub
}

func cloneTriggers(triggers []Trigger) []Trigger {
	if len(triggers) == 0 {
		return nil
	}
	cloned := make([]Trigger, 0, len(triggers))
	for _, trigger := range triggers {
		cloned = append(cloned, Trigger{
			Type:   trigger.Type,
			Values: append([]string(nil), trigger.Values...),
			Weight: trigger.Weight,
		})
	}
	return cloned
}

func cloneSkillSummary(item *SkillSummary) *SkillSummary {
	if item == nil {
		return nil
	}
	cloned := *item
	cloned.Capabilities = append([]string(nil), item.Capabilities...)
	cloned.Tags = append([]string(nil), item.Tags...)
	cloned.Triggers = cloneTriggers(item.Triggers)
	cloned.Tools = append([]string(nil), item.Tools...)
	cloned.Context = ContextConfig{
		Files:       append([]string(nil), item.Context.Files...),
		Environment: append([]string(nil), item.Context.Environment...),
		Symbols:     append([]string(nil), item.Context.Symbols...),
	}
	cloned.Permissions = append([]string(nil), item.Permissions...)
	cloned.Handler = item.Handler
	if len(item.WorkflowSteps) > 0 {
		cloned.WorkflowSteps = make([]WorkflowStepSummary, 0, len(item.WorkflowSteps))
		for _, step := range item.WorkflowSteps {
			cloned.WorkflowSteps = append(cloned.WorkflowSteps, WorkflowStepSummary{
				ID:        step.ID,
				Name:      step.Name,
				Tool:      step.Tool,
				Args:      cloneMapInterface(step.Args),
				DependsOn: append([]string(nil), step.DependsOn...),
				Condition: step.Condition,
			})
		}
	}
	if item.Source != nil {
		sourceCopy := *item.Source
		cloned.Source = &sourceCopy
	}
	return &cloned
}
