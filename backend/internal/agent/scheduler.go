package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/google/uuid"
)

// FilePatch 预留给 writer agent 的 patch 回执。
type FilePatch struct {
	Path               string   `json:"path,omitempty" yaml:"path,omitempty"`
	Diff               string   `json:"diff,omitempty" yaml:"diff,omitempty"`
	Summary            string   `json:"summary,omitempty" yaml:"summary,omitempty"`
	ApplyStatus        string   `json:"apply_status,omitempty" yaml:"apply_status,omitempty"`
	AppliedBy          []string `json:"applied_by,omitempty" yaml:"applied_by,omitempty"`
	ArtifactRefs       []string `json:"artifact_refs,omitempty" yaml:"artifact_refs,omitempty"`
	VerificationStatus string   `json:"verification_status,omitempty" yaml:"verification_status,omitempty"`
	VerifiedBy         []string `json:"verified_by,omitempty" yaml:"verified_by,omitempty"`
}

// SubagentTask 描述一个子代理任务包。
type SubagentTask struct {
	ID             string      `json:"id,omitempty" yaml:"id,omitempty"`
	Role           string      `json:"role,omitempty" yaml:"role,omitempty"`
	Goal           string      `json:"goal" yaml:"goal"`
	ToolsWhitelist []string    `json:"tools_whitelist,omitempty" yaml:"tools_whitelist,omitempty"`
	DependsOn      []string    `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
	PatchContext   []FilePatch `json:"patches,omitempty" yaml:"patches,omitempty"`
	Model          string      `json:"model,omitempty" yaml:"model,omitempty"`
	BudgetTokens   int         `json:"budget_tokens,omitempty" yaml:"budget_tokens,omitempty"`
	TimeoutSec     int         `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	ReadOnly       bool        `json:"read_only,omitempty" yaml:"read_only,omitempty"`
}

// SubagentResult 是父代理可见的结构化回执。
type SubagentResult struct {
	ID               string            `json:"id,omitempty" yaml:"id,omitempty"`
	Role             string            `json:"role,omitempty" yaml:"role,omitempty"`
	SessionID        string            `json:"session_id,omitempty" yaml:"session_id,omitempty"`
	ParentSessionID  string            `json:"parent_session_id,omitempty" yaml:"parent_session_id,omitempty"`
	ParentToolCallID string            `json:"parent_tool_call_id,omitempty" yaml:"parent_tool_call_id,omitempty"`
	ReadOnly         bool              `json:"read_only,omitempty" yaml:"read_only,omitempty"`
	BudgetTokens     int               `json:"budget_tokens,omitempty" yaml:"budget_tokens,omitempty"`
	Success          bool              `json:"success" yaml:"success"`
	Summary          string            `json:"summary" yaml:"summary"`
	Patches          []FilePatch       `json:"patches,omitempty" yaml:"patches,omitempty"`
	Findings         []string          `json:"findings,omitempty" yaml:"findings,omitempty"`
	Usage            *types.TokenUsage `json:"usage,omitempty" yaml:"usage,omitempty"`
	Error            string            `json:"error,omitempty" yaml:"error,omitempty"`
}

// SubagentSchedulerConfig 控制子代理并发与递归深度。
type SubagentSchedulerConfig struct {
	MaxConcurrent       int  `json:"maxConcurrent" yaml:"maxConcurrent"`
	MaxDepth            int  `json:"maxDepth" yaml:"maxDepth"`
	EnforceSingleWriter bool `json:"enforceSingleWriter" yaml:"enforceSingleWriter"`
}

// SubagentRunOptions 描述一次 parent -> child 协同批次的上下文。
type SubagentRunOptions struct {
	TraceID          string
	ParentSessionID  string
	ParentToolCallID string
	Depth            int
}

// SubagentScheduler 在 Go 侧调度 fresh child agents。
type SubagentScheduler struct {
	parent *Agent
	config SubagentSchedulerConfig
}

// NewSubagentScheduler 创建一个最小子代理调度器。
func NewSubagentScheduler(parent *Agent, config SubagentSchedulerConfig) *SubagentScheduler {
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = 4
	}
	if config.MaxDepth <= 0 {
		config.MaxDepth = 1
	}
	if !config.EnforceSingleWriter {
		config.EnforceSingleWriter = true
	}
	return &SubagentScheduler{
		parent: parent,
		config: config,
	}
}

// RunChildren 执行一批子代理任务，并只返回结构化摘要。
func (s *SubagentScheduler) RunChildren(ctx context.Context, options SubagentRunOptions, tasks []SubagentTask) ([]SubagentResult, error) {
	if s == nil {
		return nil, fmt.Errorf("subagent scheduler is nil")
	}
	if options.Depth > s.config.MaxDepth {
		err := fmt.Errorf("max subagent depth exceeded")
		s.emitSubagentDenied(options, "", "max_depth", err.Error(), map[string]interface{}{
			"depth":     options.Depth,
			"max_depth": s.config.MaxDepth,
		})
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, nil
	}

	preparedTasks, err := s.prepareTasks(tasks)
	if err != nil {
		s.emitSubagentDenied(options, "", classifySubagentDeniedPolicy(err.Error()), err.Error(), map[string]interface{}{
			"task_count": len(tasks),
		})
		return nil, err
	}

	readers, writers, err := s.partitionTasks(preparedTasks)
	if err != nil {
		s.emitSubagentDenied(options, "", classifySubagentDeniedPolicy(err.Error()), err.Error(), map[string]interface{}{
			"task_count": len(tasks),
		})
		return nil, err
	}

	results := make([]SubagentResult, len(tasks))
	done := make([]bool, len(tasks))
	completedByID := make(map[string]SubagentResult, len(tasks))
	remaining := len(tasks)

	for remaining > 0 {
		readyReaders, readyWriters, depErr := s.readyTasks(readers, writers, done, completedByID)
		if depErr != nil {
			s.emitSubagentDenied(options, "", "dependency", depErr.Error(), map[string]interface{}{
				"task_count": len(tasks),
			})
			return results, depErr
		}
		if len(readyReaders) == 0 && len(readyWriters) == 0 {
			err := fmt.Errorf("subagent dependency deadlock detected")
			s.emitSubagentDenied(options, "", "dependency", err.Error(), map[string]interface{}{
				"task_count": len(tasks),
			})
			return results, err
		}

		if err := s.runReaderWave(ctx, options, readyReaders, results, done, completedByID); err != nil {
			return results, err
		}
		remaining -= len(readyReaders)

		for _, item := range readyWriters {
			task := s.enrichTaskFromDependencies(item.task, completedByID)
			result, err := s.runChild(ctx, options, task)
			results[item.index] = result
			done[item.index] = true
			completedByID[task.ID] = result
			remaining--
			if err != nil {
				return results, err
			}
		}
	}

	s.finalizeWriterPatches(options, preparedTasks, results)
	return results, nil
}

func (s *SubagentScheduler) runChild(ctx context.Context, options SubagentRunOptions, task SubagentTask) (SubagentResult, error) {
	if s.parent == nil {
		return SubagentResult{}, fmt.Errorf("parent agent is nil")
	}
	if task.Goal == "" {
		s.emitSubagentDenied(options, task.ID, "validation", "subagent goal is required", map[string]interface{}{
			"subagent_id": task.ID,
		})
		return SubagentResult{
			ID:               task.ID,
			Role:             task.Role,
			ParentSessionID:  options.ParentSessionID,
			ParentToolCallID: options.ParentToolCallID,
			ReadOnly:         task.ReadOnly,
			BudgetTokens:     task.BudgetTokens,
			Success:          false,
			Error:            "subagent goal is required",
		}, nil
	}

	childCtx := ctx
	var cancel context.CancelFunc
	if task.TimeoutSec > 0 {
		childCtx, cancel = context.WithTimeout(ctx, time.Duration(task.TimeoutSec)*time.Second)
		defer cancel()
	}

	childConfig := *s.parent.GetConfig()
	childConfig.Name = firstNonEmptyString(task.ID, childConfig.Name+"-subagent")
	if task.Model != "" {
		childConfig.Model = task.Model
	}
	if task.BudgetTokens > 0 {
		childConfig.DefaultMaxTokens = task.BudgetTokens
	}
	if task.ToolsWhitelist == nil {
		task.ToolsWhitelist = DefaultToolsForRole(task.Role)
	}
	childConfig.SystemPrompt = s.parent.GetPromptBuilder().BuildSubagentPrompt(s.parent.GetConfig(), task)
	childAgent := NewAgentWithLLM(&childConfig, s.parent.mcpManager, s.parent.llmRuntime)
	childAgent.SetSubagentScheduler(NewSubagentScheduler(childAgent, s.config))
	childAgent.SetEventBus(s.parent.GetEventBus())
	childAgent.SetPromptBuilder(s.parent.GetPromptBuilder())
	childAgent.inheritToolHooksFrom(s.parent)
	childAgent.SetToolExecutionPolicy(s.childPolicy(task))
	childSessionID := buildSubagentSessionID(task.ID)
	if hookMgr := s.parent.GetHookManager(); hookMgr != nil {
		hookMgr.DispatchAsync(context.Background(), runtimehooks.EventSubagentStart, map[string]interface{}{
			"subagent_id":         task.ID,
			"role":                task.Role,
			"goal":                task.Goal,
			"depth":               options.Depth,
			"read_only":           task.ReadOnly,
			"budget_tokens":       task.BudgetTokens,
			"parent_session_id":   options.ParentSessionID,
			"parent_tool_call_id": options.ParentToolCallID,
			"child_session_id":    childSessionID,
			"child_agent_name":    childConfig.Name,
			"trace_id":            options.TraceID,
		})
	}
	s.parent.emitRuntimeEvent("subagent.started", childSessionID, "", map[string]interface{}{
		"subagent_id":         task.ID,
		"role":                task.Role,
		"goal":                task.Goal,
		"depth":               options.Depth,
		"read_only":           task.ReadOnly,
		"budget_tokens":       task.BudgetTokens,
		"parent_session_id":   options.ParentSessionID,
		"parent_tool_call_id": options.ParentToolCallID,
		"child_agent_name":    childConfig.Name,
		"trace_id":            options.TraceID,
	})

	loop := NewReActLoop(childAgent, s.parent.llmRuntime, &LoopReActConfig{
		MaxSteps:        childConfig.MaxSteps,
		EnableThought:   true,
		EnableToolCalls: true,
		Temperature:     childConfig.Temperature,
	})

	result, err := loop.run(childCtx, task.Goal, loopRunOptions{
		TraceID:       options.TraceID,
		SessionID:     childSessionID,
		IncludePrompt: true,
		Depth:         options.Depth,
		BudgetTokens:  task.BudgetTokens,
		ToolWhitelist: task.ToolsWhitelist,
	})
	if err != nil {
		report := SubagentResult{
			ID:               task.ID,
			Role:             task.Role,
			SessionID:        childSessionID,
			ParentSessionID:  options.ParentSessionID,
			ParentToolCallID: options.ParentToolCallID,
			ReadOnly:         task.ReadOnly,
			BudgetTokens:     task.BudgetTokens,
			Success:          false,
			Error:            err.Error(),
			Summary:          err.Error(),
		}
		s.parent.emitRuntimeEvent("subagent.completed", childSessionID, "", map[string]interface{}{
			"subagent_id":         task.ID,
			"role":                task.Role,
			"read_only":           task.ReadOnly,
			"success":             false,
			"error":               err.Error(),
			"budget_tokens":       task.BudgetTokens,
			"parent_session_id":   options.ParentSessionID,
			"parent_tool_call_id": options.ParentToolCallID,
			"child_agent_name":    childConfig.Name,
			"trace_id":            options.TraceID,
		})
		if hookMgr := s.parent.GetHookManager(); hookMgr != nil {
			hookMgr.DispatchAsync(context.Background(), runtimehooks.EventSubagentStop, map[string]interface{}{
				"subagent_id":         task.ID,
				"role":                task.Role,
				"read_only":           task.ReadOnly,
				"success":             false,
				"error":               err.Error(),
				"budget_tokens":       task.BudgetTokens,
				"parent_session_id":   options.ParentSessionID,
				"parent_tool_call_id": options.ParentToolCallID,
				"child_session_id":    childSessionID,
				"child_agent_name":    childConfig.Name,
				"trace_id":            options.TraceID,
			})
		}
		return report, nil
	}

	report := SubagentResult{
		ID:               task.ID,
		Role:             task.Role,
		SessionID:        childSessionID,
		ParentSessionID:  options.ParentSessionID,
		ParentToolCallID: options.ParentToolCallID,
		ReadOnly:         task.ReadOnly,
		BudgetTokens:     task.BudgetTokens,
		Success:          result.Success,
		Summary:          result.Output,
		Usage:            result.Usage,
	}
	if task.ReadOnly {
		report.Findings = collectFindings(result.Observations)
	} else {
		report.Patches = collectPatches(result.Observations)
	}
	if result.Error != "" {
		report.Error = result.Error
	}
	s.parent.emitRuntimeEvent("subagent.completed", childSessionID, "", map[string]interface{}{
		"subagent_id":         task.ID,
		"role":                task.Role,
		"read_only":           task.ReadOnly,
		"success":             report.Success,
		"error":               report.Error,
		"budget_tokens":       task.BudgetTokens,
		"parent_session_id":   options.ParentSessionID,
		"parent_tool_call_id": options.ParentToolCallID,
		"child_agent_name":    childConfig.Name,
		"usage_total_tokens":  usageTotal(report.Usage),
		"trace_id":            options.TraceID,
	})
	if hookMgr := s.parent.GetHookManager(); hookMgr != nil {
		hookMgr.DispatchAsync(context.Background(), runtimehooks.EventSubagentStop, map[string]interface{}{
			"subagent_id":         task.ID,
			"role":                task.Role,
			"read_only":           task.ReadOnly,
			"success":             report.Success,
			"error":               report.Error,
			"budget_tokens":       task.BudgetTokens,
			"parent_session_id":   options.ParentSessionID,
			"parent_tool_call_id": options.ParentToolCallID,
			"child_session_id":    childSessionID,
			"child_agent_name":    childConfig.Name,
			"usage_total_tokens":  usageTotal(report.Usage),
			"trace_id":            options.TraceID,
		})
	}
	return report, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func collectFindings(observations []types.Observation) []string {
	findings := make([]string, 0, len(observations))
	for _, observation := range observations {
		if output, ok := observation.Output.(string); ok && output != "" {
			findings = append(findings, output)
		} else if observation.Error != "" {
			findings = append(findings, observation.Error)
		}
		if len(findings) >= 4 {
			break
		}
	}
	return findings
}

func collectPatches(observations []types.Observation) []FilePatch {
	patches := make([]FilePatch, 0, len(observations))
	for _, observation := range observations {
		if !observation.Success || !isWriteLikeToolName(observation.Tool) {
			continue
		}

		toolMetadata := metricMapValue(observation, "tool_metadata")
		path := firstNonEmptyString(
			stringMapValue(toolMetadata, "file_path"),
			stringMapValue(toolMetadata, "path"),
			pathFromToolInput(observation.Input),
		)
		summary := patchSummary(observation.Tool, toolMetadata, observation.Output)
		diff := firstNonEmptyString(
			stringMapValue(toolMetadata, "diff"),
			stringMapValue(toolMetadata, "patch"),
		)
		if diff == "" {
			if outputText, ok := observation.Output.(string); ok {
				diff = extractUnifiedDiff(outputText)
			}
		}
		if path == "" && diff != "" {
			path = pathFromDiff(diff)
		}
		if summary == "" && diff != "" {
			summary = "captured unified diff from tool output"
		}
		if path == "" && summary == "" && diff == "" {
			continue
		}
		artifactRefs := metricStringSliceValue(observation, "artifact_refs")

		patches = append(patches, FilePatch{
			Path:               path,
			Diff:               diff,
			Summary:            summary,
			ApplyStatus:        derivePatchApplyStatus(observation.Tool, toolMetadata, observation.Output),
			ArtifactRefs:       artifactRefs,
			VerificationStatus: "unverified",
		})
	}
	return patches
}

func (s *SubagentScheduler) finalizeWriterPatches(options SubagentRunOptions, tasks []SubagentTask, results []SubagentResult) {
	if len(tasks) == 0 || len(results) == 0 {
		return
	}

	taskIndex := make(map[string]int, len(tasks))
	writerIDs := make([]string, 0, 1)
	for index, task := range tasks {
		taskIndex[task.ID] = index
		if !task.ReadOnly {
			writerIDs = append(writerIDs, task.ID)
		}
	}
	if len(writerIDs) == 0 {
		return
	}

	verifiersByWriter := make(map[string][]string, len(writerIDs))
	for _, task := range tasks {
		if !task.ReadOnly || len(task.DependsOn) == 0 {
			continue
		}
		for _, dep := range task.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			if _, ok := taskIndex[dep]; !ok {
				continue
			}
			verifiersByWriter[dep] = append(verifiersByWriter[dep], task.ID)
		}
	}

	for _, writerID := range writerIDs {
		writerIndex, ok := taskIndex[writerID]
		if !ok || writerIndex < 0 || writerIndex >= len(results) {
			continue
		}
		writer := results[writerIndex]
		if len(writer.Patches) == 0 {
			continue
		}

		verifierIDs := verifiersByWriter[writerID]
		if len(verifierIDs) == 0 {
			for i := range writer.Patches {
				ensurePatchAppliedBy(&writer.Patches[i], writerID)
			}
			results[writerIndex] = writer
			s.emitPatchAppliedEvents(options, writerID, writer.Patches)
			continue
		}

		verifiedBy := make([]string, 0, len(verifierIDs))
		allVerified := true
		for _, verifierID := range verifierIDs {
			verifierIndex, exists := taskIndex[verifierID]
			if !exists || verifierIndex < 0 || verifierIndex >= len(results) {
				allVerified = false
				continue
			}
			verifier := results[verifierIndex]
			if verifier.Success && strings.TrimSpace(verifier.Error) == "" {
				verifiedBy = append(verifiedBy, verifierID)
				continue
			}
			allVerified = false
		}

		status := "needs_review"
		if allVerified && len(verifiedBy) > 0 && allPatchesApplied(writer.Patches) {
			status = "verified"
		}
		for i := range writer.Patches {
			ensurePatchAppliedBy(&writer.Patches[i], writerID)
			writer.Patches[i].VerificationStatus = status
			if len(verifiedBy) > 0 {
				writer.Patches[i].VerifiedBy = append([]string(nil), verifiedBy...)
			} else {
				writer.Patches[i].VerifiedBy = nil
			}
		}
		results[writerIndex] = writer
		s.emitPatchAppliedEvents(options, writerID, writer.Patches)
	}
}

func ensurePatchAppliedBy(patch *FilePatch, writerID string) {
	if patch == nil {
		return
	}
	if strings.TrimSpace(patch.ApplyStatus) == "" {
		patch.ApplyStatus = "applied"
	}
	if strings.TrimSpace(writerID) == "" {
		return
	}
	for _, existing := range patch.AppliedBy {
		if existing == writerID {
			return
		}
	}
	patch.AppliedBy = append(patch.AppliedBy, writerID)
}

func allPatchesApplied(patches []FilePatch) bool {
	if len(patches) == 0 {
		return false
	}
	for _, patch := range patches {
		if !patchIsApplied(patch) {
			return false
		}
	}
	return true
}

func patchIsApplied(patch FilePatch) bool {
	status := strings.TrimSpace(patch.ApplyStatus)
	return status == "" || status == "applied"
}

func (s *SubagentScheduler) emitPatchAppliedEvents(options SubagentRunOptions, writerID string, patches []FilePatch) {
	if s == nil || s.parent == nil || len(patches) == 0 {
		return
	}
	for _, patch := range patches {
		payload := map[string]interface{}{
			"trace_id":            options.TraceID,
			"subagent_id":         writerID,
			"parent_session_id":   options.ParentSessionID,
			"parent_tool_call_id": options.ParentToolCallID,
			"path":                patch.Path,
			"summary":             patch.Summary,
			"apply_status":        patch.ApplyStatus,
			"applied_by":          append([]string(nil), patch.AppliedBy...),
			"verification_status": patch.VerificationStatus,
			"verified_by":         append([]string(nil), patch.VerifiedBy...),
			"artifact_refs":       append([]string(nil), patch.ArtifactRefs...),
			"artifact_ref_count":  len(patch.ArtifactRefs),
			"diff_present":        strings.TrimSpace(patch.Diff) != "",
		}
		s.parent.emitRuntimeEvent("patch.applied", options.ParentSessionID, "spawn_subagents", payload)
	}
}

func buildSubagentSessionID(taskID string) string {
	base := firstNonEmptyString(taskID, "subagent")
	return fmt.Sprintf("subagent_%s_%s", base, strings.ReplaceAll(uuid.NewString(), "-", ""))
}

func (s *SubagentScheduler) runReaderWave(ctx context.Context, options SubagentRunOptions, readers []indexedSubagentTask, results []SubagentResult, done []bool, completedByID map[string]SubagentResult) error {
	if len(readers) == 0 {
		return nil
	}

	sem := make(chan struct{}, s.config.MaxConcurrent)
	errs := make([]error, len(readers))
	waveResults := make([]SubagentResult, len(readers))
	var wg sync.WaitGroup

	for i, item := range readers {
		index := i
		task := s.enrichTaskFromDependencies(item.task, completedByID)
		wg.Add(1)
		go func(task indexedSubagentTask, prepared SubagentTask) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result, err := s.runChild(ctx, options, prepared)
			waveResults[index] = result
			errs[index] = err
		}(item, task)
	}

	wg.Wait()
	for i, item := range readers {
		results[item.index] = waveResults[i]
		done[item.index] = true
		completedByID[item.task.ID] = waveResults[i]
		if errs[i] != nil {
			return errs[i]
		}
	}
	return nil
}

func (s *SubagentScheduler) readyTasks(readers []indexedSubagentTask, writers []indexedSubagentTask, done []bool, completedByID map[string]SubagentResult) ([]indexedSubagentTask, []indexedSubagentTask, error) {
	readyReaders := make([]indexedSubagentTask, 0, len(readers))
	for _, item := range readers {
		if done[item.index] {
			continue
		}
		satisfied, err := dependenciesSatisfied(item.task, completedByID)
		if err != nil {
			return nil, nil, err
		}
		if satisfied {
			readyReaders = append(readyReaders, item)
		}
	}

	readyWriters := make([]indexedSubagentTask, 0, len(writers))
	for _, item := range writers {
		if done[item.index] {
			continue
		}
		satisfied, err := dependenciesSatisfied(item.task, completedByID)
		if err != nil {
			return nil, nil, err
		}
		if satisfied {
			readyWriters = append(readyWriters, item)
		}
	}
	return readyReaders, readyWriters, nil
}

func dependenciesSatisfied(task SubagentTask, completedByID map[string]SubagentResult) (bool, error) {
	for _, dependency := range task.DependsOn {
		dependency = strings.TrimSpace(dependency)
		if dependency == "" {
			continue
		}
		if dependency == task.ID {
			return false, fmt.Errorf("subagent %q cannot depend on itself", task.ID)
		}
		if _, ok := completedByID[dependency]; !ok {
			return false, nil
		}
	}
	return true, nil
}

func (s *SubagentScheduler) enrichTaskFromDependencies(task SubagentTask, completedByID map[string]SubagentResult) SubagentTask {
	if len(task.DependsOn) == 0 {
		return task
	}

	seen := make(map[string]bool, len(task.PatchContext))
	merged := make([]FilePatch, 0, len(task.PatchContext))
	for _, patch := range task.PatchContext {
		key := patchIdentity(patch)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, patch)
	}

	for _, dependency := range task.DependsOn {
		result, ok := completedByID[strings.TrimSpace(dependency)]
		if !ok {
			continue
		}
		for _, patch := range result.Patches {
			key := patchIdentity(patch)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, patch)
		}
	}

	task.PatchContext = merged
	return task
}

func metricMapValue(observation types.Observation, key string) map[string]interface{} {
	value, ok := observation.GetMetric(key)
	if !ok {
		return nil
	}
	typed, _ := value.(map[string]interface{})
	return typed
}

func pathFromToolInput(input interface{}) string {
	args, ok := input.(map[string]interface{})
	if !ok {
		return ""
	}
	for _, key := range []string{"file_path", "path", "target", "destination"} {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringMapValue(values map[string]interface{}, key string) string {
	if len(values) == 0 {
		return ""
	}
	if value, ok := values[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func intMapValue(values map[string]interface{}, key string) (int, bool) {
	if len(values) == 0 {
		return 0, false
	}
	switch typed := values[key].(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func patchSummary(tool string, metadata map[string]interface{}, output interface{}) string {
	switch {
	case strings.Contains(strings.ToLower(tool), "write"):
		action := firstNonEmptyString(stringMapValue(metadata, "action"), "updated")
		if oldSize, ok := intMapValue(metadata, "old_size"); ok {
			if newSize, ok := intMapValue(metadata, "new_size"); ok {
				return fmt.Sprintf("%s file (%d -> %d bytes)", action, oldSize, newSize)
			}
		}
		return action + " file"
	case strings.Contains(strings.ToLower(tool), "edit"), strings.Contains(strings.ToLower(tool), "patch"):
		if replacements, ok := intMapValue(metadata, "replacements"); ok {
			return fmt.Sprintf("applied %d replacement(s)", replacements)
		}
		if editsApplied, ok := intMapValue(metadata, "edits_applied"); ok {
			return fmt.Sprintf("applied %d edit(s)", editsApplied)
		}
	}

	if outputText, ok := output.(string); ok && strings.TrimSpace(outputText) != "" {
		return summarizePatchText(outputText, 160)
	}
	return ""
}

func summarizePatchText(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" || limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}

func extractUnifiedDiff(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	lines := strings.Split(text, "\n")
	start := -1
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "--- ") {
			start = index
			break
		}
	}
	if start < 0 {
		return ""
	}

	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		if strings.HasPrefix(strings.TrimSpace(lines[index]), "```") {
			end = index
			break
		}
	}
	diff := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
	if !strings.Contains(diff, "+++ ") {
		return ""
	}
	return diff
}

func pathFromDiff(diff string) string {
	if strings.TrimSpace(diff) == "" {
		return ""
	}

	lines := strings.Split(diff, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "+++ ") {
			if path := normalizeDiffPath(trimmed[4:]); path != "" {
				return path
			}
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--- ") {
			if path := normalizeDiffPath(trimmed[4:]); path != "" {
				return path
			}
		}
	}
	return ""
}

func normalizeDiffPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return ""
	}
	path := strings.TrimSpace(parts[0])
	if path == "/dev/null" {
		return ""
	}
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	return strings.TrimSpace(path)
}

func patchIdentity(patch FilePatch) string {
	return firstNonEmptyString(strings.TrimSpace(patch.Path), strings.TrimSpace(patch.Diff), strings.TrimSpace(patch.Summary))
}

func metricStringSliceValue(observation types.Observation, key string) []string {
	value, ok := observation.GetMetric(key)
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			text, _ := item.(string)
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			values = append(values, text)
		}
		return values
	default:
		return nil
	}
}

func derivePatchApplyStatus(tool string, metadata map[string]interface{}, output interface{}) string {
	if !isWriteLikeToolName(tool) {
		return ""
	}
	if action := strings.TrimSpace(stringMapValue(metadata, "action")); action != "" {
		return "applied"
	}
	if replacements, ok := intMapValue(metadata, "replacements"); ok && replacements >= 0 {
		return "applied"
	}
	if editsApplied, ok := intMapValue(metadata, "edits_applied"); ok && editsApplied >= 0 {
		return "applied"
	}
	if _, ok := output.(string); ok {
		return "applied"
	}
	return "applied"
}

func (s *SubagentScheduler) prepareTasks(tasks []SubagentTask) ([]SubagentTask, error) {
	prepared := make([]SubagentTask, 0, len(tasks))
	parentPolicy := (*ToolExecutionPolicy)(nil)
	if s != nil && s.parent != nil {
		parentPolicy = s.parent.GetToolExecutionPolicy()
	}
	knownIDs := make(map[string]bool, len(tasks))

	for index, task := range tasks {
		effective := task
		if effective.ID == "" {
			effective.ID = fmt.Sprintf("subagent_%d", index+1)
		}
		if effective.ToolsWhitelist == nil {
			effective.ToolsWhitelist = DefaultToolsForRole(effective.Role)
		}
		if parentPolicy != nil {
			if parentPolicy.ReadOnly && !effective.ReadOnly {
				return nil, fmt.Errorf("read-only parent policy blocks writable subagent %q", effective.ID)
			}
			childPolicy := parentPolicy.DeriveChild(effective.ToolsWhitelist, effective.ReadOnly)
			effective.ReadOnly = childPolicy.ReadOnly
			if childPolicy.AllowlistEnabled {
				effective.ToolsWhitelist = childPolicy.AllowedToolNames()
			}
		}
		knownIDs[effective.ID] = true
		prepared = append(prepared, effective)
	}

	for _, task := range prepared {
		for _, dependency := range task.DependsOn {
			dependency = strings.TrimSpace(dependency)
			if dependency == "" {
				continue
			}
			if !knownIDs[dependency] {
				return nil, fmt.Errorf("subagent %q depends on unknown task %q", task.ID, dependency)
			}
		}
	}

	return prepared, nil
}

func (s *SubagentScheduler) childPolicy(task SubagentTask) *ToolExecutionPolicy {
	parentPolicy := (*ToolExecutionPolicy)(nil)
	if s != nil && s.parent != nil {
		parentPolicy = s.parent.GetToolExecutionPolicy()
	}
	if parentPolicy != nil {
		return parentPolicy.DeriveChild(task.ToolsWhitelist, task.ReadOnly)
	}
	return NewToolExecutionPolicy(task.ToolsWhitelist, task.ReadOnly)
}

type indexedSubagentTask struct {
	index int
	task  SubagentTask
}

func (s *SubagentScheduler) partitionTasks(tasks []SubagentTask) ([]indexedSubagentTask, []indexedSubagentTask, error) {
	readers := make([]indexedSubagentTask, 0, len(tasks))
	writers := make([]indexedSubagentTask, 0, 1)

	for index, task := range tasks {
		if task.ReadOnly {
			if containsWriteLikeTool(task.ToolsWhitelist) {
				return nil, nil, fmt.Errorf("read-only subagent %q requested write-like tools", task.ID)
			}
			readers = append(readers, indexedSubagentTask{index: index, task: task})
			continue
		}
		writers = append(writers, indexedSubagentTask{index: index, task: task})
	}

	if s.config.EnforceSingleWriter && len(writers) > 1 {
		return nil, nil, fmt.Errorf("single-writer policy violation: %d writer subagents requested", len(writers))
	}

	return readers, writers, nil
}

func containsWriteLikeTool(tools []string) bool {
	for _, tool := range tools {
		if isWriteLikeToolName(tool) {
			return true
		}
	}
	return false
}

func (s *SubagentScheduler) emitSubagentDenied(options SubagentRunOptions, taskID, policy, reason string, payload map[string]interface{}) {
	if s == nil || s.parent == nil {
		return
	}
	if payload == nil {
		payload = map[string]interface{}{}
	}
	if taskID != "" {
		payload["subagent_id"] = taskID
	}
	if policy != "" {
		payload["policy"] = policy
	}
	if options.ParentSessionID != "" {
		payload["parent_session_id"] = options.ParentSessionID
	}
	if options.ParentToolCallID != "" {
		payload["parent_tool_call_id"] = options.ParentToolCallID
	}
	payload["reason"] = reason
	payload["trace_id"] = options.TraceID
	s.parent.emitRuntimeEvent("subagent.denied", "", "", payload)
}

func classifySubagentDeniedPolicy(reason string) string {
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "single-writer"):
		return "single_writer"
	case strings.Contains(lower, "read-only parent policy"):
		return "read_only"
	case strings.Contains(lower, "write-like tools"):
		return "read_only"
	default:
		return "subagent_scheduler"
	}
}

func usageTotal(usage *types.TokenUsage) int {
	if usage == nil {
		return 0
	}
	return usage.TotalTokens
}
