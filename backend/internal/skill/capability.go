package skill

import "github.com/ai-gateway/ai-agent-runtime/internal/capability"

// CapabilityDescriptor 返回统一能力描述
func (s *Skill) CapabilityDescriptor() *capability.Descriptor {
	if s == nil {
		return nil
	}

	labels := make([]string, 0, len(s.Tags)+1)
	if s.Category != "" {
		labels = append(labels, s.Category)
	}
	labels = append(labels, s.Tags...)

	triggers := make([]capability.Trigger, 0, len(s.Triggers))
	for _, trigger := range s.Triggers {
		triggers = append(triggers, capability.Trigger{
			Type:   trigger.Type,
			Values: append([]string(nil), trigger.Values...),
			Weight: trigger.Weight,
		})
	}

	dependencies := make([]capability.Dependency, 0, len(s.Tools))
	for _, toolName := range s.Tools {
		dependencies = append(dependencies, capability.Dependency{
			Name: toolName,
			Kind: capability.KindTool,
		})
	}
	if s.Workflow != nil {
		for _, step := range s.Workflow.Steps {
			if step.Tool == "" {
				continue
			}
			dependencies = append(dependencies, capability.Dependency{
				Name: step.Tool,
				Kind: capability.KindTool,
				Metadata: map[string]interface{}{
					"workflow_step_id":   step.ID,
					"workflow_step_name": step.Name,
				},
			})
		}
	}

	metadata := map[string]interface{}{
		"permissions":     append([]string(nil), s.Permissions...),
		"context_files":   append([]string(nil), s.Context.Files...),
		"context_symbols": append([]string(nil), s.Context.Symbols...),
		"context_env":     append([]string(nil), s.Context.Environment...),
		"has_workflow":    s.HasWorkflow(),
		"has_handler":     s.HasCustomHandler(),
	}
	if s.Workflow != nil {
		metadata["workflow_step_count"] = len(s.Workflow.Steps)
	}

	var source *capability.Source
	if s.Source != nil {
		source = &capability.Source{
			Path:  s.Source.Path,
			Dir:   s.Source.Dir,
			Layer: s.Source.Layer,
		}
	}

	return &capability.Descriptor{
		ID:           s.Name,
		Name:         s.Name,
		Kind:         capability.KindSkill,
		Description:  s.Description,
		Version:      s.Version,
		Category:     s.Category,
		Labels:       labels,
		Capabilities: append([]string(nil), s.Capabilities...),
		Triggers:     triggers,
		Dependencies: dependencies,
		Source:       source,
		Metadata:     metadata,
	}
}

// CapabilityDescriptors 返回所有 Skill 的统一能力描述
func (r *Registry) CapabilityDescriptors() []*capability.Descriptor {
	if r == nil {
		return nil
	}

	skills := r.List()
	descriptors := make([]*capability.Descriptor, 0, len(skills))
	for _, item := range skills {
		if descriptor := item.CapabilityDescriptor(); descriptor != nil {
			descriptors = append(descriptors, descriptor)
		}
	}
	return descriptors
}

// RouteResultsToCapabilityCandidates 将路由结果转换为统一能力候选
func RouteResultsToCapabilityCandidates(results []*RouteResult) []*capability.Candidate {
	if len(results) == 0 {
		return nil
	}

	candidates := make([]*capability.Candidate, 0, len(results))
	for _, result := range results {
		if result == nil || result.Skill == nil {
			continue
		}
		candidates = append(candidates, &capability.Candidate{
			Descriptor: result.Skill.CapabilityDescriptor(),
			Score:      result.Score,
			MatchedBy:  result.MatchedBy,
			Details:    result.Details,
		})
	}
	return candidates
}
