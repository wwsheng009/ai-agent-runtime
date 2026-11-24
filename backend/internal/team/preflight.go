package team

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	preflightRemediationTeamCreateTasks = "Add at least one task to this Team, then run preflight again."
	preflightRemediationTeamAddMate     = "Add an available teammate with a bound runtime session."
	preflightRemediationTeamLimits      = "Reduce active work or raise the configured Team limit."
	preflightRemediationSession         = "Bind the teammate to a valid runtime session before assigning work."
	preflightRemediationAssignee        = "Assign the task to a teammate in this Team, or clear the stale assignee."
	preflightRemediationDependency      = "Repair the dependency reference so it points to an existing task in this Team."
	preflightRemediationCycle           = "Remove at least one dependency edge in the cycle."
	preflightRemediationPaths           = "Narrow overlapping path scopes or serialize the affected tasks, then retry preflight."
	preflightRemediationClaims          = "Wait for or release the conflicting active path claim, or change the declared scope."
	preflightRemediationRoute           = "Connect the existing Team task route resolver to preview the configured runtime route."
	preflightRemediationRouteFailure    = "Review the existing route resolver/configuration and retry preflight."
)

// NewPreflightService creates a read-only Team preflight service. Optional
// settings can be supplied for path canonicalization, route preview, and a
// stable clock used when evaluating leased path claims.
func NewPreflightService(store Store, options ...PreflightOptions) *PreflightService {
	service := &PreflightService{Store: store}
	if len(options) > 0 {
		service.WorkspaceRoot = options[0].WorkspaceRoot
		service.RouteResolver = options[0].RouteResolver
		service.Now = options[0].Now
	}
	return service
}

// NewTeamPreflightService is an explicit constructor alias for API adapters
// that name the domain slice by its Team product surface.
func NewTeamPreflightService(store Store, options ...PreflightOptions) *PreflightService {
	return NewPreflightService(store, options...)
}

// Build returns a deterministic report for an existing legacy Team. Findings
// are sorted independently of Store row order. Store failures are returned as
// errors; validation failures are represented in the report.
func (s *PreflightService) Build(ctx context.Context, teamID string) (*PreflightReport, error) {
	if s == nil || s.Store == nil {
		return nil, fmt.Errorf("team preflight store is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	teamID = strings.TrimSpace(teamID)
	report := &PreflightReport{
		TeamID:                 teamID,
		RoutePreviewCapability: PreflightRouteCapabilityNotRequested,
	}
	findings := make([]PreflightFinding, 0)
	add := func(finding PreflightFinding) {
		finding.TeamID = firstNonEmptyString(finding.TeamID, teamID)
		findings = append(findings, finding)
	}
	if teamID == "" {
		add(PreflightFinding{
			Severity:    PreflightSeverityBlocker,
			Code:        PreflightCodeTeamNotFound,
			Message:     "A Team ID is required to build a preflight report.",
			Remediation: "Provide the ID of an existing Team.",
		})
		finalizePreflightReport(report, findings)
		return report, nil
	}

	teamRecord, err := s.Store.GetTeam(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("load Team for preflight: %w", err)
	}
	if teamRecord == nil {
		add(PreflightFinding{
			Severity:    PreflightSeverityBlocker,
			Code:        PreflightCodeTeamNotFound,
			Message:     "The requested Team does not exist.",
			Remediation: "Create or select an existing Team before running preflight.",
		})
		finalizePreflightReport(report, findings)
		return report, nil
	}
	teamCopy := *teamRecord
	report.Team = &teamCopy
	report.TeamExists = true

	teammates, err := s.Store.ListTeammates(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list Team teammates for preflight: %w", err)
	}
	tasks, err := s.Store.ListTasks(ctx, TaskFilter{TeamID: teamID})
	if err != nil {
		return nil, fmt.Errorf("list Team tasks for preflight: %w", err)
	}
	claims, err := s.Store.ListPathClaims(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list Team path claims for preflight: %w", err)
	}

	sortPreflightTeammates(teammates)
	sortPreflightTasks(tasks)
	sortPreflightClaims(claims, s.WorkspaceRoot)
	asOf := time.Now().UTC()
	if s.Now != nil {
		asOf = s.Now().UTC()
	}

	if IsTerminalTeamStatus(teamRecord.Status) {
		add(PreflightFinding{
			Severity:    PreflightSeverityBlocker,
			Code:        PreflightCodeTeamNotRunnable,
			Message:     "The Team is in a terminal state and cannot schedule more work.",
			Remediation: "Create a new Team for additional work.",
		})
	} else if teamRecord.Status == TeamStatusPaused {
		add(PreflightFinding{
			Severity:    PreflightSeverityWarning,
			Code:        PreflightCodeTeamNotRunnable,
			Message:     "The Team is paused; its current work is not expected to advance.",
			Remediation: "Resume the Team when it is ready to continue.",
		})
	}

	switch {
	case len(teammates) == 0 && len(tasks) == 0:
		add(PreflightFinding{
			Severity:    PreflightSeverityBlocker,
			Code:        PreflightCodeTeamEmpty,
			Message:     "The Team has no teammates and no tasks.",
			Remediation: preflightRemediationTeamCreateTasks,
		})
	case len(teammates) == 0:
		add(PreflightFinding{
			Severity:    PreflightSeverityBlocker,
			Code:        PreflightCodeTeamTeammatesEmpty,
			Message:     "The Team has tasks but no teammates available to run them.",
			Remediation: preflightRemediationTeamAddMate,
		})
	case len(tasks) == 0:
		add(PreflightFinding{
			Severity:    PreflightSeverityBlocker,
			Code:        PreflightCodeTeamTasksEmpty,
			Message:     "The Team has no tasks to execute.",
			Remediation: preflightRemediationTeamCreateTasks,
		})
	}

	teammatesByID, activeTeammateCount, activeTasksByAssignee := indexPreflightTeammates(teamID, teammates, tasks, add)
	checkPreflightTeammateBindings(teammates, activeTasksByAssignee, add)
	checkPreflightTeamLimits(*teamRecord, activeTeammateCount, tasks, asOf, add)
	dependencyGraph, err := checkPreflightTasks(ctx, s.Store, teamID, tasks, teammatesByID, add)
	if err != nil {
		return nil, err
	}
	checkPreflightDependencyCycles(dependencyGraph, add)
	checkPreflightPathConflicts(tasks, dependencyGraph, claims, s.WorkspaceRoot, asOf, add)

	report.RoutePreviews, report.RoutePreviewCapability = s.previewTaskRoutes(ctx, *teamRecord, tasks, teammatesByID, add)
	finalizePreflightReport(report, findings)
	return report, nil
}

// BuildReport is a descriptive alias for callers that prefer the report
// operation's full name.
func (s *PreflightService) BuildReport(ctx context.Context, teamID string) (*PreflightReport, error) {
	return s.Build(ctx, teamID)
}

func indexPreflightTeammates(
	teamID string,
	teammates []Teammate,
	tasks []Task,
	add func(PreflightFinding),
) (map[string]Teammate, int, map[string][]Task) {
	teammatesByID := make(map[string]Teammate, len(teammates))
	activeTasksByAssignee := make(map[string][]Task)
	sessionOwners := make(map[string]string, len(teammates))
	idCounts := make(map[string]int, len(teammates))
	for _, mate := range teammates {
		id := strings.TrimSpace(mate.ID)
		if id == "" {
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        PreflightCodeTeammateIDMissing,
				TeammateID:  id,
				Message:     "A teammate record has no ID.",
				Remediation: "Repair the teammate record so tasks can reference it.",
			})
			continue
		}
		idCounts[id]++
		if idCounts[id] > 1 {
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        PreflightCodeTeammateDuplicateID,
				TeammateID:  id,
				Message:     "More than one teammate record uses the same ID.",
				Remediation: "Give each teammate in the Team a unique ID.",
			})
			continue
		}
		teammatesByID[id] = mate
		if !teamIDsEqual(strings.TrimSpace(mate.TeamID), teamID) {
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        PreflightCodeTeammateTeamMismatch,
				TeammateID:  id,
				Message:     "A listed teammate belongs to a different Team.",
				Remediation: "Move the teammate into this Team or remove the inconsistent record.",
			})
		}
		sessionID := strings.TrimSpace(mate.SessionID)
		if sessionID == "" {
			continue
		}
		sessionKey := strings.ToLower(sessionID)
		if owner, exists := sessionOwners[sessionKey]; exists {
			add(PreflightFinding{
				Severity:        PreflightSeverityBlocker,
				Code:            PreflightCodeTeammateDuplicateSession,
				TeammateID:      id,
				RelatedTeammate: owner,
				Message:         "Two teammates are bound to the same runtime session.",
				Remediation:     "Bind each teammate to its own runtime session.",
			})
			continue
		}
		sessionOwners[sessionKey] = id
	}

	activeTeammateCount := 0
	for _, mate := range teammates {
		if mate.State == TeammateStateBusy || mate.State == TeammateStateBlocked {
			activeTeammateCount++
		}
	}
	for _, task := range tasks {
		assignee := taskAssigneeID(task)
		if assignee == "" || IsTerminalTaskStatus(task.Status) {
			continue
		}
		activeTasksByAssignee[assignee] = append(activeTasksByAssignee[assignee], task)
	}
	for assignee := range activeTasksByAssignee {
		sortPreflightTasks(activeTasksByAssignee[assignee])
	}
	return teammatesByID, activeTeammateCount, activeTasksByAssignee
}

func checkPreflightTeammateBindings(
	teammates []Teammate,
	activeTasksByAssignee map[string][]Task,
	add func(PreflightFinding),
) {
	for _, mate := range teammates {
		id := strings.TrimSpace(mate.ID)
		if id == "" {
			continue
		}
		active := len(activeTasksByAssignee[id]) > 0
		if active {
			// Active assignments receive task-specific session/state findings.
			continue
		}
		sessionSeverity := PreflightSeverityWarning
		switch {
		case strings.TrimSpace(mate.SessionID) == "":
			add(PreflightFinding{
				Severity:    sessionSeverity,
				Code:        PreflightCodeTeammateSessionMissing,
				TeammateID:  id,
				Message:     "The teammate has no bound runtime session.",
				Remediation: preflightRemediationSession,
			})
		case mate.State == TeammateStateOffline:
			add(PreflightFinding{
				Severity:    sessionSeverity,
				Code:        PreflightCodeTeammateOffline,
				TeammateID:  id,
				Message:     "The teammate is offline.",
				Remediation: "Bring the teammate online before assigning it new work.",
			})
		case mate.State == TeammateStateBlocked:
			add(PreflightFinding{
				Severity:    PreflightSeverityWarning,
				Code:        PreflightCodeTeammateBlocked,
				TeammateID:  id,
				Message:     "The teammate is blocked.",
				Remediation: "Resolve the teammate's blocker before assigning it new work.",
			})
		case mate.State == TeammateStateBusy:
			add(PreflightFinding{
				Severity:    PreflightSeverityWarning,
				Code:        PreflightCodeTeammateBusy,
				TeammateID:  id,
				Message:     "The teammate is busy and is not available for new work.",
				Remediation: "Wait for the teammate to become idle before assigning it new work.",
			})
		case !knownTeammateState(mate.State):
			add(PreflightFinding{
				Severity:    PreflightSeverityWarning,
				Code:        PreflightCodeTeammateStateUnknown,
				TeammateID:  id,
				Message:     "The teammate has an unrecognized availability state.",
				Remediation: "Set the teammate state to idle, busy, blocked, or offline.",
			})
		}
	}
}

func checkPreflightTeamLimits(
	team Team,
	activeTeammateCount int,
	tasks []Task,
	asOf time.Time,
	add func(PreflightFinding),
) {
	if team.MaxTeammates > 0 {
		switch {
		case activeTeammateCount > team.MaxTeammates:
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        PreflightCodeTeamActiveTeammatesExceeded,
				Message:     "The number of active teammates exceeds the configured Team limit.",
				Remediation: preflightRemediationTeamLimits,
			})
		case activeTeammateCount == team.MaxTeammates:
			add(PreflightFinding{
				Severity:    PreflightSeverityWarning,
				Code:        PreflightCodeTeamActiveTeammatesAtLimit,
				Message:     "The Team has reached its active teammate limit.",
				Remediation: preflightRemediationTeamLimits,
			})
		}
	}

	activeWriters := 0
	for _, task := range tasks {
		if task.Status != TaskStatusRunning || len(task.WritePaths) == 0 {
			continue
		}
		if task.LeaseUntil != nil && task.LeaseUntil.Before(asOf) {
			continue
		}
		activeWriters++
	}
	if team.MaxWriters > 0 {
		switch {
		case activeWriters > team.MaxWriters:
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        PreflightCodeTeamActiveWritersExceeded,
				Message:     "The number of active writer tasks exceeds the configured Team limit.",
				Remediation: preflightRemediationTeamLimits,
			})
		case activeWriters == team.MaxWriters:
			add(PreflightFinding{
				Severity:    PreflightSeverityWarning,
				Code:        PreflightCodeTeamWriterLimitWillSerialize,
				Message:     "The Team has reached its active writer limit; additional writers will wait.",
				Remediation: "Allow an active writer to finish, or raise the configured writer limit if safe.",
			})
		}
	}
}

func checkPreflightTasks(
	ctx context.Context,
	store Store,
	teamID string,
	tasks []Task,
	teammatesByID map[string]Teammate,
	add func(PreflightFinding),
) (map[string][]string, error) {
	tasksByID := make(map[string]Task, len(tasks))
	taskIDCounts := make(map[string]int, len(tasks))
	for _, task := range tasks {
		id := strings.TrimSpace(task.ID)
		if id == "" {
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        PreflightCodeTaskIDMissing,
				TaskID:      id,
				Message:     "A task record has no ID.",
				Remediation: "Repair the task record so dependencies and assignments can reference it.",
			})
			continue
		}
		taskIDCounts[id]++
		if taskIDCounts[id] > 1 {
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        PreflightCodeTaskDuplicateID,
				TaskID:      id,
				Message:     "More than one task record uses the same ID.",
				Remediation: "Give each task in the Team a unique ID.",
			})
			continue
		}
		tasksByID[id] = task
		if !teamIDsEqual(strings.TrimSpace(task.TeamID), teamID) {
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        PreflightCodeTaskTeamMismatch,
				TaskID:      id,
				Message:     "A listed task belongs to a different Team.",
				Remediation: "Move the task into this Team or remove the inconsistent record.",
			})
		}
	}

	graph := make(map[string][]string, len(tasksByID))
	knownDependencies := make(map[string]*Task, len(tasksByID))
	for id, task := range tasksByID {
		taskCopy := task
		knownDependencies[id] = &taskCopy
	}
	for _, task := range tasks {
		taskID := strings.TrimSpace(task.ID)
		if taskID == "" {
			continue
		}
		if _, alreadyProcessed := graph[taskID]; alreadyProcessed {
			continue
		}
		// An empty dependency list is still a processed node; retain it in the
		// graph so duplicate task IDs are handled deterministically.
		graph[taskID] = nil
		assignee := taskAssigneeID(task)
		if IsTerminalTaskStatus(task.Status) {
			// Historical assignments on terminal tasks no longer affect routing.
		} else if assignee == "" {
			add(PreflightFinding{
				Severity:    PreflightSeverityInfo,
				Code:        PreflightCodeTaskUnassigned,
				TaskID:      taskID,
				Message:     "The task has no explicit assignee and may be assigned by the existing scheduler.",
				Remediation: "Assign a teammate explicitly, or leave this task unassigned for scheduler selection.",
			})
		} else if _, exists := teammatesByID[assignee]; !exists {
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        PreflightCodeTaskAssigneeUnknown,
				TaskID:      taskID,
				Message:     "The task assignee does not identify a teammate in this Team.",
				Remediation: preflightRemediationAssignee,
			})
		} else if !IsTerminalTaskStatus(task.Status) {
			mate := teammatesByID[assignee]
			sessionID := strings.TrimSpace(mate.SessionID)
			switch {
			case sessionID == "":
				add(PreflightFinding{
					Severity:    PreflightSeverityBlocker,
					Code:        PreflightCodeTaskAssigneeSessionMissing,
					TeammateID:  assignee,
					TaskID:      taskID,
					Message:     "The assigned teammate has no bound runtime session.",
					Remediation: preflightRemediationSession,
				})
			case mate.State == TeammateStateOffline:
				add(PreflightFinding{
					Severity:    PreflightSeverityBlocker,
					Code:        PreflightCodeTaskAssigneeOffline,
					TeammateID:  assignee,
					TaskID:      taskID,
					Message:     "The assigned teammate is offline.",
					Remediation: "Bring the teammate online or assign the task to an available teammate.",
				})
			case mate.State == TeammateStateBlocked && (task.Status == TaskStatusPending || task.Status == TaskStatusReady || task.Status == TaskStatusReclaimPending):
				add(PreflightFinding{
					Severity:    PreflightSeverityBlocker,
					Code:        PreflightCodeTaskAssigneeBlocked,
					TeammateID:  assignee,
					TaskID:      taskID,
					Message:     "The task is assigned to a blocked teammate that cannot take new work.",
					Remediation: "Unblock the teammate or assign the task to an available teammate.",
				})
			case mate.State == TeammateStateBusy && (task.Status == TaskStatusPending || task.Status == TaskStatusReady || task.Status == TaskStatusReclaimPending):
				add(PreflightFinding{
					Severity:    PreflightSeverityBlocker,
					Code:        PreflightCodeTaskAssigneeBusy,
					TeammateID:  assignee,
					TaskID:      taskID,
					Message:     "The task is assigned to a busy teammate that cannot take new work.",
					Remediation: "Wait for the teammate to become idle or assign the task to an available teammate.",
				})
			case !knownTeammateState(mate.State):
				add(PreflightFinding{
					Severity:    PreflightSeverityWarning,
					Code:        PreflightCodeTeammateStateUnknown,
					TeammateID:  assignee,
					TaskID:      taskID,
					Message:     "The assigned teammate has an unrecognized availability state.",
					Remediation: "Set the teammate state to idle, busy, blocked, or offline.",
				})
			}
		}

		dependencies, err := store.ListTaskDependencies(ctx, taskID)
		if err != nil {
			return nil, fmt.Errorf("list dependencies for Team task %q: %w", taskID, err)
		}
		dependencies = uniqueTrimmedSorted(dependencies)
		for _, dependencyID := range dependencies {
			if dependencyID == taskID {
				add(PreflightFinding{
					Severity:    PreflightSeverityBlocker,
					Code:        PreflightCodeDependencySelf,
					TaskID:      taskID,
					DependsOnID: dependencyID,
					Message:     "A task cannot depend on itself.",
					Remediation: preflightRemediationCycle,
				})
				continue
			}
			dependency := knownDependencies[dependencyID]
			if dependency == nil {
				loaded, err := store.GetTask(ctx, dependencyID)
				if err != nil {
					return nil, fmt.Errorf("load dependency task %q for Team task %q: %w", dependencyID, taskID, err)
				}
				if loaded == nil {
					add(PreflightFinding{
						Severity:    PreflightSeverityBlocker,
						Code:        PreflightCodeDependencyTaskMissing,
						TaskID:      taskID,
						DependsOnID: dependencyID,
						Message:     "The dependency references a task that does not exist.",
						Remediation: preflightRemediationDependency,
					})
					continue
				}
				loadedCopy := *loaded
				dependency = &loadedCopy
				knownDependencies[dependencyID] = dependency
			}
			if !teamIDsEqual(strings.TrimSpace(dependency.TeamID), teamID) {
				add(PreflightFinding{
					Severity:    PreflightSeverityBlocker,
					Code:        PreflightCodeDependencyCrossTeam,
					TaskID:      taskID,
					DependsOnID: dependencyID,
					Message:     "A task dependency crosses the Team boundary.",
					Remediation: preflightRemediationDependency,
				})
				continue
			}
			if _, exists := tasksByID[dependencyID]; exists {
				graph[taskID] = append(graph[taskID], dependencyID)
			}
		}
	}
	for taskID := range graph {
		graph[taskID] = uniqueTrimmedSorted(graph[taskID])
	}

	return graph, nil
}

func checkPreflightDependencyCycles(graph map[string][]string, add func(PreflightFinding)) {
	state := make(map[string]uint8, len(graph))
	stack := make([]string, 0, len(graph))
	stackIndex := make(map[string]int, len(graph))
	cycleMembers := make(map[string]struct{})

	var visit func(string)
	visit = func(taskID string) {
		state[taskID] = 1
		stackIndex[taskID] = len(stack)
		stack = append(stack, taskID)
		for _, dependencyID := range graph[taskID] {
			switch state[dependencyID] {
			case 0:
				visit(dependencyID)
			case 1:
				start := stackIndex[dependencyID]
				for _, member := range stack[start:] {
					cycleMembers[member] = struct{}{}
				}
			}
		}
		stack = stack[:len(stack)-1]
		delete(stackIndex, taskID)
		state[taskID] = 2
	}

	taskIDs := make([]string, 0, len(graph))
	for taskID := range graph {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)
	for _, taskID := range taskIDs {
		if state[taskID] == 0 {
			visit(taskID)
		}
	}
	members := make([]string, 0, len(cycleMembers))
	for taskID := range cycleMembers {
		members = append(members, taskID)
	}
	sort.Strings(members)
	for _, taskID := range members {
		add(PreflightFinding{
			Severity:    PreflightSeverityBlocker,
			Code:        PreflightCodeDependencyCycle,
			TaskID:      taskID,
			Message:     "The Team task dependency graph contains a cycle.",
			Remediation: preflightRemediationCycle,
		})
	}
}

func checkPreflightPathConflicts(
	tasks []Task,
	graph map[string][]string,
	claims []PathClaim,
	root string,
	asOf time.Time,
	add func(PreflightFinding),
) {
	activeTasks := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		if !IsTerminalTaskStatus(task.Status) {
			activeTasks = append(activeTasks, task)
		}
	}

	for i := 0; i < len(activeTasks); i++ {
		left := activeTasks[i]
		for j := i + 1; j < len(activeTasks); j++ {
			right := activeTasks[j]
			if (preflightTaskDependsOn(graph, left.ID, right.ID) || preflightTaskDependsOn(graph, right.ID, left.ID)) &&
				!(left.Status == TaskStatusRunning && right.Status == TaskStatusRunning) {
				// Existing dependency edges serialize these tasks; their declared
				// path overlap is not a concurrent write conflict.
				continue
			}
			for _, leftWrite := range normalizePaths(root, left.WritePaths) {
				for _, rightPath := range append(normalizePaths(root, right.ReadPaths), normalizePaths(root, right.WritePaths)...) {
					if pathsOverlap(leftWrite, rightPath) {
						add(PreflightFinding{
							Severity:      PreflightSeverityBlocker,
							Code:          PreflightCodePathWriteConflict,
							TaskID:        strings.TrimSpace(left.ID),
							RelatedTaskID: strings.TrimSpace(right.ID),
							Path:          leftWrite,
							RelatedPath:   rightPath,
							Message:       "Two non-terminal tasks have overlapping write scopes.",
							Remediation:   preflightRemediationPaths,
						})
					}
				}
			}
			for _, rightWrite := range normalizePaths(root, right.WritePaths) {
				for _, leftRead := range normalizePaths(root, left.ReadPaths) {
					if pathsOverlap(rightWrite, leftRead) {
						add(PreflightFinding{
							Severity:      PreflightSeverityBlocker,
							Code:          PreflightCodePathWriteConflict,
							TaskID:        strings.TrimSpace(right.ID),
							RelatedTaskID: strings.TrimSpace(left.ID),
							Path:          rightWrite,
							RelatedPath:   leftRead,
							Message:       "Two non-terminal tasks have overlapping write scopes.",
							Remediation:   preflightRemediationPaths,
						})
					}
				}
			}
		}
	}

	for _, task := range activeTasks {
		taskID := strings.TrimSpace(task.ID)
		requestedReads := normalizePaths(root, task.ReadPaths)
		requestedWrites := normalizePaths(root, task.WritePaths)
		for _, claim := range claims {
			if (!claim.LeaseUntil.IsZero() && claim.LeaseUntil.Before(asOf)) || strings.TrimSpace(claim.TaskID) == taskID {
				continue
			}
			if task.Status != TaskStatusRunning && preflightTaskDependsOn(graph, taskID, strings.TrimSpace(claim.TaskID)) {
				continue
			}
			existingPath := normalizePath(root, claim.Path)
			if existingPath == "" {
				continue
			}
			conflict := ""
			for _, requested := range requestedReads {
				if claim.Mode == PathClaimWrite && pathsOverlap(requested, existingPath) {
					conflict = requested
					break
				}
			}
			if conflict == "" {
				for _, requested := range requestedWrites {
					if pathsOverlap(requested, existingPath) {
						conflict = requested
						break
					}
				}
			}
			if conflict == "" {
				continue
			}
			add(PreflightFinding{
				Severity:      PreflightSeverityBlocker,
				Code:          PreflightCodePathClaimConflict,
				TaskID:        taskID,
				RelatedTaskID: strings.TrimSpace(claim.TaskID),
				Path:          conflict,
				RelatedPath:   existingPath,
				ExistingOwner: strings.TrimSpace(claim.OwnerAgentID),
				ExistingMode:  claim.Mode,
				Message:       "The task's declared path scope overlaps an active path claim.",
				Remediation:   preflightRemediationClaims,
			})
		}
	}
}

func preflightTaskDependsOn(graph map[string][]string, taskID, dependsOnID string) bool {
	taskID = strings.TrimSpace(taskID)
	dependsOnID = strings.TrimSpace(dependsOnID)
	if taskID == "" || dependsOnID == "" || taskID == dependsOnID {
		return false
	}
	visited := make(map[string]struct{})
	pending := append([]string(nil), graph[taskID]...)
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		if current == dependsOnID {
			return true
		}
		if _, seen := visited[current]; seen {
			continue
		}
		visited[current] = struct{}{}
		pending = append(pending, graph[current]...)
	}
	return false
}

func (s *PreflightService) previewTaskRoutes(
	ctx context.Context,
	team Team,
	tasks []Task,
	teammatesByID map[string]Teammate,
	add func(PreflightFinding),
) ([]PreflightRoutePreview, string) {
	previews := make([]PreflightRoutePreview, 0, len(tasks))
	if s.RouteResolver == nil {
		for _, task := range tasks {
			previews = append(previews, PreflightRoutePreview{
				TaskID:     strings.TrimSpace(task.ID),
				TeammateID: taskAssigneeID(task),
				Capability: PreflightRouteCapabilityUnavailable,
			})
		}
		add(PreflightFinding{
			Severity:    PreflightSeverityInfo,
			Code:        PreflightCodeRoutePreviewUnavailable,
			Message:     "Route preview is unavailable because no Team task route resolver was injected.",
			Remediation: preflightRemediationRoute,
		})
		return previews, PreflightRouteCapabilityUnavailable
	}

	attempted := 0
	resolved := 0
	disabled := 0
	failed := 0
	for _, task := range tasks {
		taskID := strings.TrimSpace(task.ID)
		assignee := taskAssigneeID(task)
		mate, found := teammatesByID[assignee]
		if assignee == "" || !found || strings.TrimSpace(mate.SessionID) == "" || IsTerminalTaskStatus(task.Status) {
			previews = append(previews, PreflightRoutePreview{
				TaskID:     taskID,
				TeammateID: assignee,
				Capability: PreflightRouteCapabilityNotRequested,
			})
			continue
		}

		attempted++
		attempt := task.RetryCount + 1
		if attempt <= 0 {
			attempt = 1
		}
		sessionID := strings.TrimSpace(mate.SessionID)
		resolution, err := s.RouteResolver.ResolveTaskRoute(ctx, TaskRouteRequest{
			Team:      team,
			Teammate:  mate,
			Task:      cloneTaskForPreflight(task),
			Attempt:   attempt,
			SessionID: sessionID,
		})
		preview := PreflightRoutePreview{
			TaskID:     taskID,
			TeammateID: assignee,
			SessionID:  sessionID,
			Capability: PreflightRouteCapabilityResolved,
		}
		if resolution != nil {
			preview.Disabled = resolution.Disabled
			preview.Strict = resolution.Strict
			preview.Route = resolution.Route.Clone()
			if resolution.Disabled {
				preview.Capability = PreflightRouteCapabilityDisabled
				disabled++
				add(PreflightFinding{
					Severity:    PreflightSeverityInfo,
					Code:        PreflightCodeRoutePreviewDisabled,
					TaskID:      taskID,
					TeammateID:  assignee,
					Message:     "The existing route resolver reports routing disabled for this task.",
					Remediation: "No route change is required; execution will retain the resolver's existing disabled-routing behavior.",
				})
			} else if resolution.Route == nil && err == nil {
				preview.Capability = PreflightRouteCapabilityUnavailable
				failed++
				add(PreflightFinding{
					Severity:    PreflightSeverityInfo,
					Code:        PreflightCodeRoutePreviewUnavailable,
					TaskID:      taskID,
					TeammateID:  assignee,
					Message:     "The route resolver did not provide a route preview for this task.",
					Remediation: preflightRemediationRoute,
				})
			}
		} else if err == nil {
			preview.Capability = PreflightRouteCapabilityUnavailable
			failed++
			add(PreflightFinding{
				Severity:    PreflightSeverityInfo,
				Code:        PreflightCodeRoutePreviewUnavailable,
				TaskID:      taskID,
				TeammateID:  assignee,
				Message:     "The route resolver did not provide a route preview for this task.",
				Remediation: preflightRemediationRoute,
			})
		}
		if err != nil {
			preview.Error = truncateLine(sanitizeRouteAuditText(err.Error()), 240)
			preview.Capability = PreflightRouteCapabilityFailed
			failed++
			severity := PreflightSeverityWarning
			if resolution != nil && resolution.Strict {
				severity = PreflightSeverityBlocker
			}
			add(PreflightFinding{
				Severity:    severity,
				Code:        PreflightCodeRoutePreviewError,
				TaskID:      taskID,
				TeammateID:  assignee,
				Message:     "The existing route resolver failed to preview this task.",
				Remediation: preflightRemediationRouteFailure,
			})
		} else if preview.Capability == PreflightRouteCapabilityResolved {
			resolved++
		}
		previews = append(previews, preview)
	}

	if attempted == 0 {
		return previews, PreflightRouteCapabilityNotRequested
	}
	if failed > 0 && resolved == 0 && disabled == 0 {
		return previews, PreflightRouteCapabilityFailed
	}
	if failed > 0 || (resolved > 0 && disabled > 0) {
		return previews, PreflightRouteCapabilityPartial
	}
	if disabled == attempted {
		return previews, PreflightRouteCapabilityDisabled
	}
	return previews, PreflightRouteCapabilityResolved
}

func cloneTaskForPreflight(task Task) Task {
	clone := task
	if task.ParentTaskID != nil {
		value := *task.ParentTaskID
		clone.ParentTaskID = &value
	}
	if task.Assignee != nil {
		value := *task.Assignee
		clone.Assignee = &value
	}
	if task.LeaseUntil != nil {
		value := *task.LeaseUntil
		clone.LeaseUntil = &value
	}
	if task.ResultRef != nil {
		value := *task.ResultRef
		clone.ResultRef = &value
	}
	clone.Inputs = append([]string(nil), task.Inputs...)
	clone.ReadPaths = append([]string(nil), task.ReadPaths...)
	clone.WritePaths = append([]string(nil), task.WritePaths...)
	clone.Deliverables = append([]string(nil), task.Deliverables...)
	return clone
}

func finalizePreflightReport(report *PreflightReport, findings []PreflightFinding) {
	sort.Slice(findings, func(i, j int) bool {
		return preflightFindingLess(findings[i], findings[j])
	})
	unique := findings[:0]
	seen := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		key := preflightFindingKey(finding)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, finding)
	}
	report.Findings = append([]PreflightFinding(nil), unique...)
	report.Blockers = make([]PreflightFinding, 0)
	report.Warnings = make([]PreflightFinding, 0)
	report.Infos = make([]PreflightFinding, 0)
	for _, finding := range report.Findings {
		switch finding.Severity {
		case PreflightSeverityBlocker:
			report.Blockers = append(report.Blockers, finding)
		case PreflightSeverityWarning:
			report.Warnings = append(report.Warnings, finding)
		case PreflightSeverityInfo:
			report.Infos = append(report.Infos, finding)
		}
	}
	report.BlockerCount = len(report.Blockers)
	report.WarningCount = len(report.Warnings)
	report.InfoCount = len(report.Infos)
	report.Ready = report.TeamExists && report.BlockerCount == 0
	sort.Slice(report.RoutePreviews, func(i, j int) bool {
		if report.RoutePreviews[i].TaskID != report.RoutePreviews[j].TaskID {
			return report.RoutePreviews[i].TaskID < report.RoutePreviews[j].TaskID
		}
		return report.RoutePreviews[i].TeammateID < report.RoutePreviews[j].TeammateID
	})
}

func preflightFindingLess(a, b PreflightFinding) bool {
	if a.Severity != b.Severity {
		return preflightSeverityRank(a.Severity) < preflightSeverityRank(b.Severity)
	}
	if a.Code != b.Code {
		return a.Code < b.Code
	}
	for _, pair := range [][2]string{
		{a.TeamID, b.TeamID},
		{a.TeammateID, b.TeammateID},
		{a.RelatedTeammate, b.RelatedTeammate},
		{a.TaskID, b.TaskID},
		{a.RelatedTaskID, b.RelatedTaskID},
		{a.DependsOnID, b.DependsOnID},
		{a.Path, b.Path},
		{a.RelatedPath, b.RelatedPath},
		{string(a.ExistingMode), string(b.ExistingMode)},
		{a.ExistingOwner, b.ExistingOwner},
		{a.Remediation, b.Remediation},
	} {
		if pair[0] != pair[1] {
			return pair[0] < pair[1]
		}
	}
	return a.Message < b.Message
}

func preflightFindingKey(finding PreflightFinding) string {
	return strings.Join([]string{
		string(finding.Severity),
		finding.Code,
		finding.TeamID,
		finding.TeammateID,
		finding.RelatedTeammate,
		finding.TaskID,
		finding.RelatedTaskID,
		finding.DependsOnID,
		finding.Path,
		finding.RelatedPath,
		string(finding.ExistingMode),
		finding.ExistingOwner,
		finding.Message,
		finding.Remediation,
	}, "\x00")
}

func preflightSeverityRank(severity PreflightSeverity) int {
	switch severity {
	case PreflightSeverityBlocker:
		return 0
	case PreflightSeverityWarning:
		return 1
	case PreflightSeverityInfo:
		return 2
	default:
		return 3
	}
}

func sortPreflightTeammates(teammates []Teammate) {
	sort.Slice(teammates, func(i, j int) bool {
		if teammates[i].ID != teammates[j].ID {
			return teammates[i].ID < teammates[j].ID
		}
		if teammates[i].SessionID != teammates[j].SessionID {
			return teammates[i].SessionID < teammates[j].SessionID
		}
		return teammates[i].State < teammates[j].State
	})
}

func sortPreflightTasks(tasks []Task) {
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].ID != tasks[j].ID {
			return tasks[i].ID < tasks[j].ID
		}
		if tasks[i].Status != tasks[j].Status {
			return tasks[i].Status < tasks[j].Status
		}
		if taskAssigneeID(tasks[i]) != taskAssigneeID(tasks[j]) {
			return taskAssigneeID(tasks[i]) < taskAssigneeID(tasks[j])
		}
		return tasks[i].Title < tasks[j].Title
	})
}

func sortPreflightClaims(claims []PathClaim, root string) {
	sort.Slice(claims, func(i, j int) bool {
		if claims[i].TaskID != claims[j].TaskID {
			return claims[i].TaskID < claims[j].TaskID
		}
		leftPath := normalizePath(root, claims[i].Path)
		rightPath := normalizePath(root, claims[j].Path)
		if leftPath != rightPath {
			return leftPath < rightPath
		}
		if claims[i].Mode != claims[j].Mode {
			return claims[i].Mode < claims[j].Mode
		}
		if claims[i].OwnerAgentID != claims[j].OwnerAgentID {
			return claims[i].OwnerAgentID < claims[j].OwnerAgentID
		}
		return claims[i].LeaseUntil.Before(claims[j].LeaseUntil)
	})
}

func uniqueTrimmedSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func knownTeammateState(state TeammateState) bool {
	switch TeammateState(strings.ToLower(strings.TrimSpace(string(state)))) {
	case "", TeammateStateIdle, TeammateStateBusy, TeammateStateBlocked, TeammateStateOffline:
		return true
	default:
		return false
	}
}

func teamIDsEqual(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}
