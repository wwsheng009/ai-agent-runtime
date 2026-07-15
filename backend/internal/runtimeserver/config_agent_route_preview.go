package runtimeserver

import (
	"fmt"
	"strings"
	"time"

	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
)

func (s *LocalConfigDocumentService) PreviewAgentRoute(
	req skillsapi.AgentRoutePreviewRequest,
) (*skillsapi.AgentRoutePreviewResult, error) {
	documentPath := s.documentPath()
	if s == nil || strings.TrimSpace(documentPath) == "" {
		return nil, fmt.Errorf("config path is required")
	}

	format := detectConfigDocumentFormat(documentPath)
	currentDocument, err := s.loadEffectiveDocument(format)
	if err != nil {
		return nil, err
	}
	content, _, err := s.resolveDocumentBytesWithCurrent(
		req.Document,
		format,
		currentDocument.Parsed,
	)
	if err != nil {
		return nil, err
	}
	cfg, err := decodeConfigDocumentAgentConfig(content, format)
	if err != nil {
		return nil, err
	}

	catalog := modelrouting.NewConfigCatalog(cfg)
	scope, routingSource, routing, err := modelrouting.ResolveConfigScope(
		cfg,
		req.Scope,
		req.Workflow,
	)
	if err != nil {
		return nil, err
	}
	parent := modelrouting.ResolveParentDefaults(cfg, catalog, modelrouting.ConfigParentOverrides{
		Provider:        req.Parent.Provider,
		Model:           req.Parent.Model,
		ReasoningEffort: req.Parent.ReasoningEffort,
	})
	if req.Parent.MaxTokens > 0 {
		parent.MaxTokens = req.Parent.MaxTokens
	}
	if parentTimeout, err := parseOptionalRouteDuration(req.Parent.Timeout); err != nil {
		return nil, fmt.Errorf("invalid parent timeout: %w", err)
	} else if parentTimeout > 0 {
		parent.Timeout = parentTimeout
	}
	taskTimeout, err := parseOptionalRouteDuration(req.Task.Timeout)
	if err != nil {
		return nil, fmt.Errorf("invalid task timeout: %w", err)
	}
	readOnly := true
	if req.Task.ReadOnly != nil {
		readOnly = *req.Task.ReadOnly
	}

	decision, err := (modelrouting.Resolver{
		Config:  routing,
		Catalog: catalog,
	}).Resolve(parent, modelrouting.TaskHint{
		Role:                strings.TrimSpace(req.Task.Role),
		Goal:                strings.TrimSpace(req.Task.Goal),
		Difficulty:          strings.TrimSpace(req.Task.Difficulty),
		DifficultyRationale: strings.TrimSpace(req.Task.DifficultyRationale),
		Provider:            strings.TrimSpace(req.Task.Provider),
		Model:               strings.TrimSpace(req.Task.Model),
		ReasoningEffort:     strings.TrimSpace(req.Task.ReasoningEffort),
		BudgetTokens:        req.Task.BudgetTokens,
		Timeout:             taskTimeout,
		ReadOnly:            readOnly,
	})
	if err != nil {
		return nil, err
	}

	return &skillsapi.AgentRoutePreviewResult{
		Scope:          scope,
		RoutingSource:  routingSource,
		RoutingEnabled: modelrouting.RoutingEnabled(routing),
		Parent: skillsapi.AgentRoutePreviewParent{
			Provider:        parent.Provider,
			Model:           parent.Model,
			ReasoningEffort: parent.ReasoningEffort,
			MaxTokens:       parent.MaxTokens,
			Timeout:         formatRouteDuration(parent.Timeout),
		},
		Decision: skillsapi.AgentRoutePreviewDecision{
			Difficulty:          decision.Difficulty,
			DifficultySource:    decision.DifficultySource,
			DifficultyRationale: decision.DifficultyRationale,
			Provider:            decision.Provider,
			Model:               decision.Model,
			ReasoningEffort:     decision.ReasoningEffort,
			MaxTokens:           decision.MaxTokens,
			Timeout:             formatRouteDuration(decision.Timeout),
			Source:              decision.Source,
			Warnings:            append([]string(nil), decision.Warnings...),
			FallbackUsed:        decision.FallbackUsed,
			FallbackReason:      decision.FallbackReason,
		},
	}, nil
}

func parseOptionalRouteDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	return time.ParseDuration(value)
}

func formatRouteDuration(value time.Duration) string {
	if value <= 0 {
		return ""
	}
	return value.String()
}

var _ skillsapi.AgentRoutePreviewService = (*LocalConfigDocumentService)(nil)
