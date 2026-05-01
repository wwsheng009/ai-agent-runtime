package skill

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ExposureCandidate describes one routed skill exposure candidate.
type ExposureCandidate struct {
	FunctionName string  `json:"function_name,omitempty"`
	SkillName    string  `json:"skill_name,omitempty"`
	Score        float64 `json:"score,omitempty"`
	MatchedBy    string  `json:"matched_by,omitempty"`
	Details      string  `json:"details,omitempty"`
}

// ExposureProjection is the shared normalized projection for skill exposure metadata.
type ExposureProjection struct {
	Mode                  string              `json:"mode,omitempty"`
	IncludeBuiltin        bool                `json:"include_builtin"`
	BuiltinFunctions      []string            `json:"builtin_functions,omitempty"`
	SkillFunctions        []string            `json:"skill_functions,omitempty"`
	FinalFunctionNames    []string            `json:"final_function_names,omitempty"`
	TopK                  int                 `json:"top_k,omitempty"`
	RoutedSkills          []string            `json:"routed_skills,omitempty"`
	ExplicitMentions      []string            `json:"explicit_mentions,omitempty"`
	PreviouslyCalled      []string            `json:"previously_called,omitempty"`
	Candidates            []ExposureCandidate `json:"candidates,omitempty"`
	CatalogTotalFunctions int                 `json:"catalog_total_functions,omitempty"`
	CatalogBuiltinTools   int                 `json:"catalog_builtin_tools,omitempty"`
	CatalogSkillFunctions int                 `json:"catalog_skill_functions,omitempty"`
	BuiltinFunctionCount  int                 `json:"builtin_function_count,omitempty"`
	SkillFunctionCount    int                 `json:"skill_function_count,omitempty"`
	FinalFunctionCount    int                 `json:"final_function_count,omitempty"`
	RoutedSkillCount      int                 `json:"routed_skill_count,omitempty"`
	CandidateCount        int                 `json:"candidate_count,omitempty"`
}

// BuildToolSurfaceSummary produces the normalized tool surface summary used in request metadata.
func BuildToolSurfaceSummary(tools []types.ToolDefinition) map[string]interface{} {
	if len(tools) == 0 {
		return nil
	}

	names := make([]string, 0, len(tools))
	sourceCounts := make(map[string]int)
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		names = append(names, name)

		if source := toolresult.NormalizeSource(stringValue(tool.Metadata[toolresult.SourceKey])); source != "" {
			sourceCounts[source]++
		}
	}

	if len(names) == 0 {
		return nil
	}

	summary := map[string]interface{}{
		"count": len(names),
		"names": names,
	}
	if len(sourceCounts) > 0 {
		summary["sources"] = sourceCounts
	}
	return summary
}

// BuildToolSurfaceMetadata wraps the normalized tool surface summary for direct request metadata injection.
func BuildToolSurfaceMetadata(tools []types.ToolDefinition) map[string]interface{} {
	summary := BuildToolSurfaceSummary(tools)
	if len(summary) == 0 {
		return nil
	}
	return map[string]interface{}{
		"tool_surface": summary,
	}
}

// BuildSkillExposureSummary produces the normalized skill exposure summary used in request metadata.
func BuildSkillExposureSummary(projection ExposureProjection) map[string]interface{} {
	summary := map[string]interface{}{
		"mode":                 projection.Mode,
		"include_builtin":      projection.IncludeBuiltin,
		"builtin_functions":    cloneStringSlice(projection.BuiltinFunctions),
		"skill_functions":      cloneStringSlice(projection.SkillFunctions),
		"final_function_names": cloneStringSlice(projection.FinalFunctionNames),
	}
	if projection.TopK > 0 {
		summary["top_k"] = projection.TopK
	}
	if projection.CatalogTotalFunctions > 0 {
		summary["catalog_total_functions"] = projection.CatalogTotalFunctions
	}
	if projection.CatalogBuiltinTools > 0 {
		summary["catalog_builtin_tools"] = projection.CatalogBuiltinTools
	}
	if projection.CatalogSkillFunctions > 0 {
		summary["catalog_skill_functions"] = projection.CatalogSkillFunctions
	}
	if projection.BuiltinFunctionCount > 0 {
		summary["builtin_function_count"] = projection.BuiltinFunctionCount
	}
	if projection.SkillFunctionCount > 0 {
		summary["skill_function_count"] = projection.SkillFunctionCount
	}
	if projection.FinalFunctionCount > 0 {
		summary["final_function_count"] = projection.FinalFunctionCount
	}
	if projection.RoutedSkillCount > 0 {
		summary["routed_skill_count"] = projection.RoutedSkillCount
	}
	if projection.CandidateCount > 0 {
		summary["candidate_count"] = projection.CandidateCount
	}
	if len(projection.RoutedSkills) > 0 {
		summary["routed_skills"] = cloneStringSlice(projection.RoutedSkills)
	}
	if len(projection.ExplicitMentions) > 0 {
		summary["explicit_mentions"] = cloneStringSlice(projection.ExplicitMentions)
	}
	if len(projection.PreviouslyCalled) > 0 {
		summary["previously_called"] = cloneStringSlice(projection.PreviouslyCalled)
	}
	if len(projection.Candidates) > 0 {
		summary["candidates"] = cloneExposureCandidates(projection.Candidates)
	}
	return summary
}

// BuildSkillExposureMetadata wraps the normalized skill exposure summary for direct request metadata injection.
func BuildSkillExposureMetadata(projection ExposureProjection) map[string]interface{} {
	return map[string]interface{}{
		"skill_exposure": BuildSkillExposureSummary(projection),
	}
}

func cloneStringSlice(input []string) []string {
	if len(input) == 0 {
		return nil
	}
	return append([]string(nil), input...)
}

func cloneExposureCandidates(input []ExposureCandidate) []ExposureCandidate {
	if len(input) == 0 {
		return nil
	}
	return append([]ExposureCandidate(nil), input...)
}

func stringValue(value interface{}) string {
	text, _ := value.(string)
	return text
}
