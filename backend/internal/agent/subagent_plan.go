package agent

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
)

// BuildSubagentTasksFromPlan 将 planner 结果映射为建议的子代理任务图。
func BuildSubagentTasksFromPlan(plan *Plan) []SubagentTask {
	if plan == nil || len(plan.Steps) == 0 {
		return nil
	}

	tasks := make([]SubagentTask, 0, len(plan.Steps)+1)
	writerIDs := make([]string, 0, 1)
	verifierDependsOnWriter := false

	for _, step := range plan.Steps {
		role := inferSubagentRole(step)
		difficulty, difficultyRationale := difficultyMetadataForPlanStep(step)
		task := SubagentTask{
			ID:                  firstNonEmptyString(step.ID, role),
			Role:                role,
			Goal:                subagentGoalForStep(plan.Goal, step),
			Difficulty:          difficulty,
			DifficultyRationale: difficultyRationale,
			ToolsWhitelist:      stepToolWhitelist(step),
			DependsOn:           append([]string(nil), step.DependsOn...),
			ReadOnly:            role != "writer",
		}
		tasks = append(tasks, task)
		if role == "writer" {
			writerIDs = append(writerIDs, task.ID)
		}
		if role == "verifier" && dependsOnAny(task.DependsOn, writerIDs) {
			verifierDependsOnWriter = true
		}
	}

	if len(writerIDs) > 0 && !verifierDependsOnWriter {
		verifierDifficulty := verifierDifficultyForWriters(tasks, writerIDs)
		tasks = append(tasks, SubagentTask{
			ID:                  "verifier_auto",
			Role:                "verifier",
			Goal:                fmt.Sprintf("Verify the writer changes for goal: %s", strings.TrimSpace(plan.Goal)),
			Difficulty:          verifierDifficulty,
			DifficultyRationale: "Verifier difficulty derived from planner writer task difficulty.",
			ToolsWhitelist:      DefaultToolsForRole("verifier"),
			DependsOn:           append([]string(nil), writerIDs...),
			ReadOnly:            true,
		})
	}

	upgradeVerifierDifficultyForHighRiskWriters(tasks)
	return tasks
}

func inferSubagentRole(step PlanStep) string {
	lowerTool := strings.ToLower(strings.TrimSpace(step.Tool))
	lowerDesc := strings.ToLower(strings.TrimSpace(step.Description))

	switch {
	case isWriteLikeToolName(lowerTool) || strings.Contains(lowerDesc, "write") || strings.Contains(lowerDesc, "patch") || strings.Contains(lowerDesc, "modify"):
		return "writer"
	case strings.Contains(lowerTool, "test"),
		strings.Contains(lowerTool, "lint"),
		strings.Contains(lowerTool, "build"),
		strings.Contains(lowerTool, "verify"),
		strings.Contains(lowerDesc, "test"),
		strings.Contains(lowerDesc, "verify"),
		strings.Contains(lowerDesc, "validate"),
		strings.Contains(lowerDesc, "regression"):
		return "verifier"
	default:
		return "researcher"
	}
}

func subagentGoalForStep(planGoal string, step PlanStep) string {
	if strings.TrimSpace(step.Description) != "" {
		if strings.TrimSpace(planGoal) != "" {
			return fmt.Sprintf("%s Goal context: %s", strings.TrimSpace(step.Description), strings.TrimSpace(planGoal))
		}
		return strings.TrimSpace(step.Description)
	}
	if strings.TrimSpace(step.Tool) != "" {
		if strings.TrimSpace(planGoal) != "" {
			return fmt.Sprintf("Use %s to advance the goal: %s", step.Tool, strings.TrimSpace(planGoal))
		}
		return fmt.Sprintf("Use %s to complete the assigned work.", step.Tool)
	}
	return strings.TrimSpace(planGoal)
}

func stepToolWhitelist(step PlanStep) []string {
	if strings.TrimSpace(step.Tool) == "" {
		return nil
	}
	return []string{strings.TrimSpace(step.Tool)}
}

func difficultyMetadataForPlanStep(step PlanStep) (string, string) {
	difficulty, ok := modelrouting.NormalizeDifficulty(step.Difficulty)
	if !ok {
		difficulty = modelrouting.DifficultyNormal
	}
	rationale := strings.TrimSpace(step.DifficultyRationale)
	if rationale == "" {
		rationale = "Default difficulty assigned for planner-generated subtask."
	}
	return difficulty, rationale
}

func verifierDifficultyForWriters(tasks []SubagentTask, writerIDs []string) string {
	writerSet := make(map[string]bool, len(writerIDs))
	for _, writerID := range writerIDs {
		writerSet[strings.TrimSpace(writerID)] = true
	}
	for _, task := range tasks {
		if !writerSet[strings.TrimSpace(task.ID)] {
			continue
		}
		difficulty, ok := modelrouting.NormalizeDifficulty(task.Difficulty)
		if !ok {
			continue
		}
		if difficulty == modelrouting.DifficultyHard || difficulty == modelrouting.DifficultyExpert {
			return modelrouting.DifficultyHard
		}
	}
	return modelrouting.DifficultyNormal
}

func upgradeVerifierDifficultyForHighRiskWriters(tasks []SubagentTask) {
	writerDifficulty := make(map[string]string, len(tasks))
	for _, task := range tasks {
		if isHighRiskWriterTask(task) {
			writerDifficulty[strings.TrimSpace(task.ID)] = task.Difficulty
		}
	}
	if len(writerDifficulty) == 0 {
		return
	}
	for index := range tasks {
		task := tasks[index]
		if !strings.EqualFold(strings.TrimSpace(task.Role), "verifier") || !task.ReadOnly {
			continue
		}
		if !dependsOnAny(task.DependsOn, mapKeys(writerDifficulty)) {
			continue
		}
		if subagentDifficultyRank(task.Difficulty) >= subagentDifficultyRank(modelrouting.DifficultyHard) {
			continue
		}
		tasks[index].Difficulty = modelrouting.DifficultyHard
		if strings.TrimSpace(tasks[index].DifficultyRationale) == "" ||
			strings.Contains(tasks[index].DifficultyRationale, "Default difficulty assigned") {
			tasks[index].DifficultyRationale = "Verifier difficulty derived from hard or expert writer dependency."
		}
	}
}

func mapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func dependsOnAny(dependsOn []string, targets []string) bool {
	if len(dependsOn) == 0 || len(targets) == 0 {
		return false
	}
	targetSet := make(map[string]bool, len(targets))
	for _, target := range targets {
		targetSet[strings.TrimSpace(target)] = true
	}
	for _, dep := range dependsOn {
		if targetSet[strings.TrimSpace(dep)] {
			return true
		}
	}
	return false
}

// ValidatePlannedSubagentExecution 对自动执行的子任务图做保守治理校验。
func ValidatePlannedSubagentExecution(tasks []SubagentTask, policy *ToolExecutionPolicy, allowWrite bool) error {
	if len(tasks) == 0 {
		return fmt.Errorf("no planned subagents available for execution")
	}

	taskMap := make(map[string]SubagentTask, len(tasks))
	writerIDs := make([]string, 0, 1)
	for _, task := range tasks {
		if strings.TrimSpace(task.ID) == "" {
			return fmt.Errorf("planned subagent is missing id")
		}
		taskMap[task.ID] = task
		if !task.ReadOnly {
			if task.Role != "writer" {
				return fmt.Errorf("planned writable subagent %q must use writer role", task.ID)
			}
			writerIDs = append(writerIDs, task.ID)
		}
		if task.ReadOnly && containsWriteLikeTool(task.ToolsWhitelist) {
			return fmt.Errorf("read-only planned subagent %q requested write-like tools", task.ID)
		}
		for _, dependency := range task.DependsOn {
			dependency = strings.TrimSpace(dependency)
			if dependency == "" {
				continue
			}
			if dependency == task.ID {
				return fmt.Errorf("planned subagent %q cannot depend on itself", task.ID)
			}
			if _, ok := taskMap[dependency]; !ok {
				return fmt.Errorf("planned subagent %q depends on unknown task %q", task.ID, dependency)
			}
		}
	}

	if hasSubagentDependencyCycle(taskMap) {
		return fmt.Errorf("planned subagent execution graph has cyclic dependencies")
	}

	if len(writerIDs) > 1 {
		return fmt.Errorf("planned subagent execution allows only one writer")
	}
	if len(writerIDs) > 0 && !allowWrite {
		return fmt.Errorf("planned writer subagent execution requires explicit write approval")
	}
	if policy != nil && policy.ReadOnly && len(writerIDs) > 0 {
		return fmt.Errorf("read-only policy blocks planned writer subagent execution")
	}
	if len(writerIDs) > 0 {
		hasVerifier := false
		for _, task := range tasks {
			if task.Role == "verifier" && dependsOnAny(task.DependsOn, writerIDs) {
				hasVerifier = true
				break
			}
		}
		if !hasVerifier {
			return fmt.Errorf("planned writer subagent execution requires a verifier dependency")
		}
	}
	for _, writerID := range writerIDs {
		writer := taskMap[writerID]
		if !isHighRiskWriterTask(writer) {
			continue
		}
		verifierFound := false
		for _, task := range tasks {
			if task.Role != "verifier" || !task.ReadOnly || !dependsOnAny(task.DependsOn, []string{writerID}) {
				continue
			}
			verifierFound = true
			if subagentDifficultyRank(task.Difficulty) < subagentDifficultyRank(modelrouting.DifficultyHard) {
				return fmt.Errorf("hard or expert writer subagent %q requires a hard-or-higher verifier", writerID)
			}
		}
		if !verifierFound {
			return fmt.Errorf("hard or expert writer subagent %q requires a read-only verifier dependency", writerID)
		}
	}

	return nil
}

func isHighRiskWriterTask(task SubagentTask) bool {
	if task.ReadOnly || !strings.EqualFold(strings.TrimSpace(task.Role), "writer") {
		return false
	}
	difficulty, ok := modelrouting.NormalizeDifficulty(task.Difficulty)
	return ok && (difficulty == modelrouting.DifficultyHard || difficulty == modelrouting.DifficultyExpert)
}

func subagentDifficultyRank(raw string) int {
	difficulty, ok := modelrouting.NormalizeDifficulty(raw)
	if !ok {
		difficulty = modelrouting.DifficultyNormal
	}
	switch difficulty {
	case modelrouting.DifficultyEasy:
		return 1
	case modelrouting.DifficultyNormal:
		return 2
	case modelrouting.DifficultyHard:
		return 3
	case modelrouting.DifficultyExpert:
		return 4
	default:
		return 2
	}
}

func hasSubagentDependencyCycle(taskMap map[string]SubagentTask) bool {
	visiting := make(map[string]bool, len(taskMap))
	visited := make(map[string]bool, len(taskMap))

	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		task := taskMap[id]
		for _, dependency := range task.DependsOn {
			if visit(strings.TrimSpace(dependency)) {
				return true
			}
		}
		visiting[id] = false
		visited[id] = true
		return false
	}

	for id := range taskMap {
		if visit(id) {
			return true
		}
	}
	return false
}
