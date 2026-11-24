package team

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// The product snapshot intentionally accepts the fields already used by the
// lead planner/task graph contracts. Unknown fields remain available for later
// phases, but Start only projects the fields represented below into the legacy
// runtime. That keeps the compatibility boundary explicit instead of silently
// pretending that arbitrary Definition JSON is executable.
type teamRunStartConfig struct {
	Strategy      string                  `json:"strategy,omitempty"`
	LeadSessionID string                  `json:"lead_session_id,omitempty"`
	MaxTeammates  int                     `json:"max_teammates,omitempty"`
	MaxWriters    int                     `json:"max_writers,omitempty"`
	Members       []teamRunMemberSpec     `json:"members,omitempty"`
	Teammates     []teamRunMemberSpec     `json:"teammates,omitempty"`
	Tasks         []teamRunTaskSpec       `json:"tasks,omitempty"`
	Dependencies  []teamRunDependencySpec `json:"dependencies,omitempty"`
}

type teamRunMemberSpec struct {
	ID           string   `json:"id,omitempty"`
	Name         string   `json:"name,omitempty"`
	Role         string   `json:"role,omitempty"`
	Profile      string   `json:"profile,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
	State        string   `json:"state,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type teamRunTaskSpec struct {
	ID                  string   `json:"id,omitempty"`
	ParentTaskID        string   `json:"parent_task_id,omitempty"`
	Title               string   `json:"title,omitempty"`
	Goal                string   `json:"goal,omitempty"`
	Difficulty          string   `json:"difficulty,omitempty"`
	DifficultyRationale string   `json:"difficulty_rationale,omitempty"`
	TaskType            string   `json:"task_type,omitempty"`
	TaskSubject         string   `json:"task_subject,omitempty"`
	Inputs              []string `json:"inputs,omitempty"`
	ReadPaths           []string `json:"read_paths,omitempty"`
	WritePaths          []string `json:"write_paths,omitempty"`
	Deliverables        []string `json:"deliverables,omitempty"`
	Priority            int      `json:"priority,omitempty"`
	Assignee            string   `json:"assignee,omitempty"`
	Status              string   `json:"status,omitempty"`
}

type teamRunDependencySpec struct {
	Task        string `json:"task,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	DependsOn   string `json:"depends_on,omitempty"`
	DependsOnID string `json:"depends_on_id,omitempty"`
}

type teamRunProjection struct {
	Team         Team
	Teammates    []Teammate
	Tasks        []Task
	Dependencies []teamRunProjectionDependency
	Findings     []PreflightFinding
}

type teamRunProjectionDependency struct {
	TaskID      string
	DependsOnID string
}

func deterministicTeamRunID(runID string) string {
	return "teamrun_" + strings.TrimSpace(runID)
}

func deterministicTeamRunProjectionID(prefix, runID, reference string, index int) string {
	seed := fmt.Sprintf("team-run-projection\x00%s\x00%s\x00%d", strings.TrimSpace(runID), strings.TrimSpace(reference), index)
	return prefix + digestTeamResource([]byte(seed))[:32]
}

func buildTeamRunProjection(run TeamRun, envelope teamRunSnapshotEnvelope, now time.Time) (*teamRunProjection, error) {
	var config teamRunStartConfig
	if err := json.Unmarshal(envelope.Config, &config); err != nil {
		return nil, fmt.Errorf("%w: decode executable Team Run config: %v", ErrTeamResourceInvalid, err)
	}

	projection := &teamRunProjection{
		Team: Team{
			ID:            deterministicTeamRunID(run.ID),
			WorkspaceID:   strings.TrimSpace(run.WorkspaceID),
			LeadSessionID: strings.TrimSpace(config.LeadSessionID),
			Status:        TeamStatusActive,
			Strategy:      strings.TrimSpace(config.Strategy),
			MaxTeammates:  config.MaxTeammates,
			MaxWriters:    config.MaxWriters,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		Teammates:    make([]Teammate, 0),
		Tasks:        make([]Task, 0),
		Dependencies: make([]teamRunProjectionDependency, 0),
		Findings:     make([]PreflightFinding, 0),
	}
	if projection.Team.Strategy == "" {
		projection.Team.Strategy = "manual"
	}
	add := func(finding PreflightFinding) {
		finding.TeamID = projection.Team.ID
		projection.Findings = append(projection.Findings, finding)
	}
	if config.MaxTeammates < 0 || config.MaxWriters < 0 {
		add(PreflightFinding{
			Severity:    PreflightSeverityBlocker,
			Code:        "team_run_invalid_limit",
			Message:     "Team limits cannot be negative.",
			Remediation: "Set max_teammates and max_writers to zero or a positive limit.",
		})
	}

	memberSpecs := make([]teamRunMemberSpec, 0, len(config.Members)+len(config.Teammates))
	memberSpecs = append(memberSpecs, config.Members...)
	memberSpecs = append(memberSpecs, config.Teammates...)
	if len(memberSpecs) == 0 {
		add(PreflightFinding{
			Severity:    PreflightSeverityBlocker,
			Code:        PreflightCodeTeamTeammatesEmpty,
			Message:     "The Team Run snapshot contains no teammates.",
			Remediation: "Add at least one teammate with a bound runtime session before starting the Run.",
		})
	}

	memberRefs := make(map[string]string, len(memberSpecs))
	memberByID := make(map[string]Teammate, len(memberSpecs))
	sessionOwners := make(map[string]string, len(memberSpecs))
	readyMembers := 0
	activeMembers := 0
	for index, spec := range memberSpecs {
		rawID := strings.TrimSpace(spec.ID)
		if rawID == "" {
			rawID = fmt.Sprintf("member-%d", index+1)
		}
		if _, exists := memberRefs[rawID]; exists {
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        PreflightCodeTeammateDuplicateID,
				TeammateID:  rawID,
				Message:     "More than one snapshot member uses the same ID.",
				Remediation: "Give each teammate in the Run snapshot a unique id.",
			})
			continue
		}
		memberID := deterministicTeamRunProjectionID("mate_", run.ID, rawID, index)
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			name = strings.TrimSpace(spec.Role)
		}
		if name == "" {
			name = rawID
		}
		profile := strings.TrimSpace(spec.Profile)
		if profile == "" {
			profile = strings.TrimSpace(spec.Role)
		}
		state := TeammateState(strings.ToLower(strings.TrimSpace(spec.State)))
		if state == "" {
			state = TeammateStateIdle
		}
		if !knownTeammateState(state) {
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        PreflightCodeTeammateStateUnknown,
				TeammateID:  rawID,
				Message:     "The snapshot member has an unrecognized availability state.",
				Remediation: "Set state to idle, busy, blocked, or offline.",
			})
		}
		sessionID := strings.TrimSpace(spec.SessionID)
		if sessionID != "" {
			sessionKey := strings.ToLower(sessionID)
			if owner, exists := sessionOwners[sessionKey]; exists {
				add(PreflightFinding{
					Severity:        PreflightSeverityBlocker,
					Code:            PreflightCodeTeammateDuplicateSession,
					TeammateID:      rawID,
					RelatedTeammate: owner,
					Message:         "Two snapshot teammates are bound to the same runtime session.",
					Remediation:     "Bind each teammate to its own runtime session.",
				})
			} else {
				sessionOwners[sessionKey] = rawID
			}
		}
		mate := Teammate{
			ID:           memberID,
			TeamID:       projection.Team.ID,
			Name:         name,
			Profile:      profile,
			SessionID:    sessionID,
			State:        state,
			Capabilities: append([]string(nil), spec.Capabilities...),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		projection.Teammates = append(projection.Teammates, mate)
		memberRefs[rawID] = memberID
		memberRefs[memberID] = memberID
		memberByID[memberID] = mate
		if sessionID != "" && state == TeammateStateIdle {
			readyMembers++
		} else if state == TeammateStateBusy || state == TeammateStateBlocked {
			activeMembers++
		} else if sessionID == "" {
			add(PreflightFinding{
				Severity:    PreflightSeverityWarning,
				Code:        PreflightCodeTeammateSessionMissing,
				TeammateID:  rawID,
				Message:     "The snapshot teammate has no bound runtime session.",
				Remediation: "Bind the teammate to a valid runtime session before assigning work.",
			})
		}
	}
	hasReadyCapacity := readyMembers > 0 &&
		(projection.Team.MaxTeammates <= 0 || activeMembers < projection.Team.MaxTeammates)
	if len(projection.Teammates) > 0 && !hasReadyCapacity {
		add(PreflightFinding{
			Severity:    PreflightSeverityBlocker,
			Code:        "team_run_no_ready_teammate",
			Message:     "The Team Run has no idle teammate with available concurrency to receive work.",
			Remediation: "Bind an idle teammate with a runtime session and free an active teammate slot before starting the Run.",
		})
	}

	if len(config.Tasks) == 0 {
		add(PreflightFinding{
			Severity:    PreflightSeverityBlocker,
			Code:        PreflightCodeTeamTasksEmpty,
			Message:     "The Team Run snapshot contains no tasks.",
			Remediation: "Add at least one task before starting the Run.",
		})
	}

	taskRefs := make(map[string]string, len(config.Tasks))
	rawTaskIDs := make(map[string]struct{}, len(config.Tasks))
	for index, spec := range config.Tasks {
		rawID := strings.TrimSpace(spec.ID)
		if rawID == "" {
			rawID = fmt.Sprintf("task-%d", index+1)
		}
		if _, exists := rawTaskIDs[rawID]; exists {
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        PreflightCodeTaskDuplicateID,
				TaskID:      rawID,
				Message:     "More than one snapshot task uses the same ID.",
				Remediation: "Give each task in the Run snapshot a unique id.",
			})
			continue
		}
		rawTaskIDs[rawID] = struct{}{}
		taskID := deterministicTeamRunProjectionID("task_", run.ID, rawID, index)
		title := strings.TrimSpace(spec.Title)
		goal := strings.TrimSpace(spec.Goal)
		if title == "" {
			title = goal
		}
		if goal == "" {
			goal = title
		}
		if title == "" || goal == "" {
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        "task_goal_missing",
				TaskID:      rawID,
				Message:     "A snapshot task must have a title or goal.",
				Remediation: "Provide title and/or goal for every task.",
			})
		}
		difficulty, validDifficulty := NormalizeTaskDifficulty(spec.Difficulty)
		if !validDifficulty {
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        "task_difficulty_invalid",
				TaskID:      rawID,
				Message:     "The snapshot task has an unsupported difficulty.",
				Remediation: "Use easy, normal, hard, or expert.",
			})
		}
		assignee := strings.TrimSpace(spec.Assignee)
		var assigneeID *string
		if assignee != "" {
			mapped, exists := memberRefs[assignee]
			if !exists {
				add(PreflightFinding{
					Severity:    PreflightSeverityBlocker,
					Code:        PreflightCodeTaskAssigneeUnknown,
					TaskID:      rawID,
					TeammateID:  assignee,
					Message:     "The snapshot task assignee does not identify a teammate.",
					Remediation: "Assign the task to a member id in this Run snapshot.",
				})
			} else {
				assigneeID = &mapped
				mate := memberByID[mapped]
				if strings.TrimSpace(mate.SessionID) == "" {
					add(PreflightFinding{
						Severity:    PreflightSeverityBlocker,
						Code:        PreflightCodeTaskAssigneeSessionMissing,
						TaskID:      rawID,
						TeammateID:  assignee,
						Message:     "The assigned snapshot teammate has no runtime session.",
						Remediation: "Bind the assigned teammate to a valid runtime session.",
					})
				} else if mate.State == TeammateStateOffline {
					add(PreflightFinding{
						Severity:    PreflightSeverityBlocker,
						Code:        PreflightCodeTaskAssigneeOffline,
						TaskID:      rawID,
						TeammateID:  assignee,
						Message:     "The assigned snapshot teammate is offline.",
						Remediation: "Bring the teammate online or assign another teammate.",
					})
				} else if mate.State == TeammateStateBlocked {
					add(PreflightFinding{
						Severity:    PreflightSeverityBlocker,
						Code:        PreflightCodeTaskAssigneeBlocked,
						TaskID:      rawID,
						TeammateID:  assignee,
						Message:     "The assigned snapshot teammate is blocked and cannot receive this task.",
						Remediation: "Unblock the teammate or assign another teammate.",
					})
				} else if mate.State == TeammateStateBusy {
					add(PreflightFinding{
						Severity:    PreflightSeverityBlocker,
						Code:        PreflightCodeTaskAssigneeBusy,
						TaskID:      rawID,
						TeammateID:  assignee,
						Message:     "The assigned snapshot teammate is already busy and cannot receive this task.",
						Remediation: "Wait for the teammate to become idle or assign another teammate.",
					})
				}
			}
		}
		status := TaskStatusPending
		if rawStatus := strings.ToLower(strings.TrimSpace(spec.Status)); rawStatus != "" && rawStatus != string(TaskStatusPending) {
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        "task_status_invalid_for_start",
				TaskID:      rawID,
				Message:     "A new Team Run task must start in pending state.",
				Remediation: "Remove status or set it to pending in the Run snapshot.",
			})
		}
		task := Task{
			ID:                  taskID,
			TeamID:              projection.Team.ID,
			Title:               title,
			Goal:                goal,
			Difficulty:          difficulty,
			DifficultyRationale: strings.TrimSpace(spec.DifficultyRationale),
			TaskType:            strings.TrimSpace(spec.TaskType),
			TaskSubject:         strings.TrimSpace(spec.TaskSubject),
			Inputs:              append([]string(nil), spec.Inputs...),
			ReadPaths:           append([]string(nil), spec.ReadPaths...),
			WritePaths:          append([]string(nil), spec.WritePaths...),
			Deliverables:        append([]string(nil), spec.Deliverables...),
			Status:              status,
			Priority:            spec.Priority,
			Assignee:            assigneeID,
			Version:             1,
			Attempt:             0,
			CreatedAt:           now,
			UpdatedAt:           now,
		}
		projection.Tasks = append(projection.Tasks, task)
		taskRefs[rawID] = taskID
		taskRefs[taskID] = taskID
	}

	for index, spec := range config.Tasks {
		rawID := strings.TrimSpace(spec.ID)
		if rawID == "" {
			rawID = fmt.Sprintf("task-%d", index+1)
		}
		task := projectionTaskByRawID(projection.Tasks, taskRefs[rawID])
		if task == nil {
			continue
		}
		if parent := strings.TrimSpace(spec.ParentTaskID); parent != "" {
			mapped, exists := taskRefs[parent]
			if !exists {
				add(PreflightFinding{
					Severity:    PreflightSeverityBlocker,
					Code:        PreflightCodeDependencyTaskMissing,
					TaskID:      rawID,
					DependsOnID: parent,
					Message:     "The snapshot parent task does not exist.",
					Remediation: "Reference an existing task in the Run snapshot.",
				})
			} else {
				parentID := mapped
				task.ParentTaskID = &parentID
			}
		}
	}

	graph := make(map[string][]string, len(projection.Tasks))
	for _, task := range projection.Tasks {
		graph[task.ID] = nil
	}
	for _, spec := range config.Dependencies {
		rawTask := firstNonEmptyString(spec.TaskID, spec.Task)
		rawDependency := firstNonEmptyString(spec.DependsOnID, spec.DependsOn)
		taskID, taskExists := taskRefs[strings.TrimSpace(rawTask)]
		dependencyID, dependencyExists := taskRefs[strings.TrimSpace(rawDependency)]
		if !taskExists || !dependencyExists {
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        PreflightCodeDependencyTaskMissing,
				TaskID:      rawTask,
				DependsOnID: rawDependency,
				Message:     "A snapshot dependency references a task that does not exist.",
				Remediation: "Reference existing task ids in the Run snapshot.",
			})
			continue
		}
		if taskID == dependencyID {
			add(PreflightFinding{
				Severity:    PreflightSeverityBlocker,
				Code:        PreflightCodeDependencySelf,
				TaskID:      taskID,
				DependsOnID: dependencyID,
				Message:     "A task cannot depend on itself.",
				Remediation: "Remove the self-dependency.",
			})
			continue
		}
		graph[taskID] = append(graph[taskID], dependencyID)
		projection.Dependencies = append(projection.Dependencies, teamRunProjectionDependency{TaskID: taskID, DependsOnID: dependencyID})
	}
	for taskID := range graph {
		graph[taskID] = uniqueTrimmedSorted(graph[taskID])
	}
	checkPreflightDependencyCycles(graph, add)
	checkPreflightPathConflicts(projection.Tasks, graph, nil, "", now, add)

	sort.SliceStable(projection.Findings, func(i, j int) bool {
		left, right := projection.Findings[i], projection.Findings[j]
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		if left.TaskID != right.TaskID {
			return left.TaskID < right.TaskID
		}
		return left.TeammateID < right.TeammateID
	})
	return projection, nil
}

func projectionTaskByRawID(tasks []Task, id string) *Task {
	for index := range tasks {
		if tasks[index].ID == id {
			return &tasks[index]
		}
	}
	return nil
}

func (s *SQLiteStore) StartTeamRun(ctx context.Context, request TeamRunStartRequest) (*TeamRun, error) {
	ownerID, err := validateTeamResourceOwner(request.OwnerID)
	if err != nil {
		return nil, err
	}
	workspaceID, err := validateTeamResourceWorkspace(request.WorkspaceID)
	if err != nil {
		return nil, err
	}
	runID := strings.TrimSpace(request.RunID)
	if runID == "" {
		return nil, fmt.Errorf("%w: run_id is required", ErrTeamResourceInvalid)
	}
	if request.ExpectedVersion <= 0 {
		return nil, fmt.Errorf("%w: expected_version must be positive", ErrTeamResourceInvalid)
	}
	idempotencyKey := strings.TrimSpace(request.IdempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > maxTeamResourceKeyLength {
		return nil, fmt.Errorf("%w: idempotency_key is required and must be at most %d bytes", ErrTeamResourceInvalid, maxTeamResourceKeyLength)
	}
	requestID := strings.TrimSpace(request.RequestID)
	if len(requestID) > maxTeamResourceKeyLength {
		return nil, fmt.Errorf("%w: request_id must be at most %d bytes", ErrTeamResourceInvalid, maxTeamResourceKeyLength)
	}
	if s == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return nil, err
	}

	startKeyHash := digestTeamResource([]byte("start_team_run\x00" + ownerID + "\x00" + runID + "\x00" + idempotencyKey))
	requestBody := struct {
		Action          string `json:"action"`
		OwnerID         string `json:"owner_id"`
		WorkspaceID     string `json:"workspace_id"`
		RunID           string `json:"run_id"`
		ExpectedVersion int64  `json:"expected_version"`
	}{
		Action:          "start_team_run",
		OwnerID:         ownerID,
		WorkspaceID:     workspaceID,
		RunID:           runID,
		ExpectedVersion: request.ExpectedVersion,
	}
	requestBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("encode Team Run Start request digest: %w", err)
	}
	requestDigest := digestTeamResource(requestBytes)

	var started *TeamRun
	wakeEvents := make([]agentcontrol.WakeEvent, 0, 8)
	err = s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		current, err := loadTeamRunTx(ctx, tx, ownerID, runID)
		if err != nil {
			return err
		}
		if current == nil || current.WorkspaceID != workspaceID {
			return ErrTeamRunNotFound
		}

		// Idempotency is checked before the optimistic version so a client that
		// timed out after commit can safely replay with the original request.
		if current.StartIdempotencyKeyHash != "" {
			if current.StartIdempotencyKeyHash != startKeyHash {
				return ErrTeamRunInvalidTransition
			}
			if current.StartRequestDigest != requestDigest {
				return ErrTeamRunStartIdempotencyConflict
			}
			started = current
			return nil
		}
		if current.Status != TeamRunStatusDraft || current.Phase != TeamRunPhaseDraft || current.LegacyTeamID != "" {
			return ErrTeamRunInvalidTransition
		}
		if current.Version != request.ExpectedVersion {
			return fmt.Errorf("%w: expected %d, current %d", ErrTeamRunVersionConflict, request.ExpectedVersion, current.Version)
		}

		var envelope teamRunSnapshotEnvelope
		if err := json.Unmarshal(current.Snapshot, &envelope); err != nil {
			return fmt.Errorf("%w: decode snapshot for Start: %v", ErrTeamRunSnapshotCorrupt, err)
		}
		projection, err := buildTeamRunProjection(*current, envelope, time.Now().UTC())
		if err != nil {
			return err
		}
		if blockers := teamRunProjectionBlockers(projection.Findings); len(blockers) > 0 {
			return &TeamRunPreflightBlockedError{Findings: blockers}
		}

		var existingWorkspace string
		existingTeamErr := tx.QueryRowContext(ctx, `SELECT workspace_id FROM teams WHERE id = ?`, projection.Team.ID).Scan(&existingWorkspace)
		switch existingTeamErr {
		case nil:
			if strings.TrimSpace(existingWorkspace) != "" && strings.TrimSpace(existingWorkspace) != projection.Team.WorkspaceID {
				return ErrTeamRunProjectionConflict
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE teams
				SET workspace_id = ?, lead_session_id = ?, status = ?, strategy = ?, max_teammates = ?, max_writers = ?, updated_at = ?
				WHERE id = ?
			`, projection.Team.WorkspaceID, projection.Team.LeadSessionID, string(TeamStatusActive), projection.Team.Strategy,
				projection.Team.MaxTeammates, projection.Team.MaxWriters, formatTime(projection.Team.UpdatedAt), projection.Team.ID); err != nil {
				return fmt.Errorf("activate existing legacy Team projection: %w", err)
			}
		case sql.ErrNoRows:
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO teams (
					id, workspace_id, lead_session_id, status, strategy, max_teammates, max_writers, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, projection.Team.ID, projection.Team.WorkspaceID, projection.Team.LeadSessionID, string(TeamStatusActive),
				projection.Team.Strategy, projection.Team.MaxTeammates, projection.Team.MaxWriters,
				formatTime(projection.Team.CreatedAt), formatTime(projection.Team.UpdatedAt)); err != nil {
				return fmt.Errorf("insert legacy Team projection: %w", err)
			}
		default:
			return fmt.Errorf("check legacy Team projection: %w", existingTeamErr)
		}

		matesByID := make(map[string]Teammate, len(projection.Teammates))
		for _, mate := range projection.Teammates {
			if err := insertTeamRunTeammateTx(ctx, tx, mate); err != nil {
				return err
			}
			matesByID[mate.ID] = mate
		}
		for _, task := range projection.Tasks {
			var mate *Teammate
			if assignee := taskAssigneeID(task); assignee != "" {
				candidate, exists := matesByID[assignee]
				if exists {
					mate = &candidate
				}
			}
			record := AgentControlTaskRecord(task, mate)
			if err := s.insertAgentControlTaskRecordTx(ctx, tx, task, record, true); err != nil {
				return err
			}
		}
		for _, dependency := range projection.Dependencies {
			dependencyID := deterministicTeamRunProjectionID("dep_", current.ID, dependency.TaskID+"\x00"+dependency.DependsOnID, 0)
			result, err := tx.ExecContext(ctx, `
				INSERT INTO agent_control_task_dependencies (
					workflow, dependency_id, team_id, task_id, depends_on_id, created_at
				) VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT(workflow, task_id, depends_on_id) DO NOTHING
			`, agentcontrol.WorkflowSpawnTeam, dependencyID, projection.Team.ID, dependency.TaskID, dependency.DependsOnID, formatTime(projection.Team.UpdatedAt))
			if err != nil {
				return fmt.Errorf("insert Team Run dependency projection: %w", err)
			}
			affected, _ := result.RowsAffected()
			if affected == 0 {
				continue
			}
			seq, wake, err := appendTeamRunEventTx(ctx, tx, TeamEvent{
				Type:   TaskDependencyCreatedEvent,
				TeamID: projection.Team.ID,
				Payload: map[string]interface{}{
					"dependency_id": dependencyID,
					"task_id":       dependency.TaskID,
					"depends_on_id": dependency.DependsOnID,
					"team_id":       projection.Team.ID,
				},
				Timestamp: projection.Team.UpdatedAt,
			})
			if err != nil {
				return err
			}
			_ = seq
			wakeEvents = append(wakeEvents, wake)
		}

		startSeq, wake, err := appendTeamRunEventTx(ctx, tx, TeamEvent{
			Type:   "team.run.started",
			TeamID: projection.Team.ID,
			Payload: map[string]interface{}{
				"run_id":           current.ID,
				"legacy_team_id":   projection.Team.ID,
				"previous_version": current.Version,
				"version":          current.Version + 1,
				"status":           "active",
				"phase":            "running",
			},
			Timestamp: projection.Team.UpdatedAt,
		})
		if err != nil {
			return err
		}
		wakeEvents = append(wakeEvents, wake)

		result, err := tx.ExecContext(ctx, `
			UPDATE team_runs
			SET legacy_team_id = ?, status = ?, phase = ?, compatibility_mode = ?, version = version + 1,
				event_seq = ?, summary_state = ?, start_idempotency_key_hash = ?, start_request_digest = ?,
				started_at = ?
			WHERE id = ? AND owner_id = ? AND workspace_id = ? AND version = ?
			`, projection.Team.ID, "active", "running", TeamRunCompatibilityLegacyTeamView, startSeq,
			TeamRunSummaryPending, startKeyHash, requestDigest, formatTime(projection.Team.UpdatedAt),
			current.ID, ownerID, workspaceID, request.ExpectedVersion)
		if err != nil {
			return fmt.Errorf("update Team Run Start state: %w", err)
		}
		affected, _ := result.RowsAffected()
		if affected != 1 {
			return fmt.Errorf("%w: expected %d", ErrTeamRunVersionConflict, request.ExpectedVersion)
		}
		started, err = loadTeamRunTx(ctx, tx, ownerID, current.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, wake := range wakeEvents {
		s.notifyAgentControlWakeWatchers(wake)
	}
	return started, nil
}

// PreflightTeamRun evaluates the immutable Run snapshot without mutating the
// Run or creating its legacy Team projection. Start repeats the same
// validation inside its write transaction; this method is an explanatory read
// surface, not a client-controlled readiness override.
func (s *SQLiteStore) PreflightTeamRun(ctx context.Context, request TeamRunPreflightRequest) (*TeamRunPreflightReport, error) {
	ownerID, err := validateTeamResourceOwner(request.OwnerID)
	if err != nil {
		return nil, err
	}
	workspaceID, err := validateTeamResourceWorkspace(request.WorkspaceID)
	if err != nil {
		return nil, err
	}
	runID := strings.TrimSpace(request.RunID)
	if runID == "" {
		return nil, fmt.Errorf("%w: run_id is required", ErrTeamResourceInvalid)
	}
	if s == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	if skip, err := s.ensureForRead(); err != nil {
		return nil, err
	} else if skip {
		return nil, ErrTeamRunNotFound
	}

	run, err := loadTeamRun(ctx, s.db, ownerID, runID)
	if err != nil {
		return nil, err
	}
	if run == nil || run.WorkspaceID != workspaceID {
		return nil, ErrTeamRunNotFound
	}

	report := &TeamRunPreflightReport{
		RunID:                  run.ID,
		LegacyTeamID:           run.LegacyTeamID,
		Version:                run.Version,
		Status:                 run.Status,
		Phase:                  run.Phase,
		SnapshotSchema:         run.SnapshotSchema,
		RoutePreviewCapability: PreflightRouteCapabilityDisabled,
	}
	add := func(finding PreflightFinding) {
		if finding.TeamID == "" && run.LegacyTeamID != "" {
			finding.TeamID = run.LegacyTeamID
		}
		report.Findings = append(report.Findings, finding)
	}

	if run.Status != TeamRunStatusDraft ||
		run.Phase != TeamRunPhaseDraft ||
		strings.TrimSpace(run.LegacyTeamID) != "" {
		add(PreflightFinding{
			Severity:    PreflightSeverityBlocker,
			Code:        "team_run_not_draft",
			Message:     "Only a draft Team Run can be preflighted for Start.",
			Remediation: "Create a new Team Run draft for another execution.",
		})
		finalizeTeamRunPreflightReport(report)
		return report, nil
	}

	var envelope teamRunSnapshotEnvelope
	if err := json.Unmarshal(run.Snapshot, &envelope); err != nil {
		add(PreflightFinding{
			Severity:    PreflightSeverityBlocker,
			Code:        "team_run_snapshot_invalid",
			Message:     "The Team Run snapshot cannot be decoded for Start.",
			Remediation: "Create a new Run from a valid Definition/configuration.",
		})
		finalizeTeamRunPreflightReport(report)
		return report, nil
	}
	projection, err := buildTeamRunProjection(*run, envelope, time.Now().UTC())
	if err != nil {
		add(PreflightFinding{
			Severity:    PreflightSeverityBlocker,
			Code:        "team_run_snapshot_invalid",
			Message:     "The Team Run snapshot cannot be projected into the Runtime.",
			Remediation: "Repair the Definition/configuration and create a new Run draft.",
		})
		finalizeTeamRunPreflightReport(report)
		return report, nil
	}
	for _, finding := range projection.Findings {
		add(finding)
	}
	finalizeTeamRunPreflightReport(report)
	return report, nil
}

func finalizeTeamRunPreflightReport(report *TeamRunPreflightReport) {
	if report == nil {
		return
	}
	sort.SliceStable(report.Findings, func(i, j int) bool {
		return preflightFindingLess(report.Findings[i], report.Findings[j])
	})
	unique := report.Findings[:0]
	seen := make(map[string]struct{}, len(report.Findings))
	for _, finding := range report.Findings {
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
	report.Ready = report.Status == TeamRunStatusDraft &&
		report.Phase == TeamRunPhaseDraft &&
		report.BlockerCount == 0
}

func teamRunProjectionBlockers(findings []PreflightFinding) []PreflightFinding {
	blockers := make([]PreflightFinding, 0)
	for _, finding := range findings {
		if finding.Severity == PreflightSeverityBlocker {
			blockers = append(blockers, finding)
		}
	}
	return blockers
}

func insertTeamRunTeammateTx(ctx context.Context, tx *sql.Tx, mate Teammate) error {
	capabilitiesJSON, err := encodeStringSlice(mate.Capabilities)
	if err != nil {
		return err
	}
	if mate.CreatedAt.IsZero() {
		mate.CreatedAt = time.Now().UTC()
	}
	if mate.UpdatedAt.IsZero() {
		mate.UpdatedAt = mate.CreatedAt
	}
	if mate.State == "" {
		mate.State = TeammateStateIdle
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO teammates (
			id, team_id, name, profile, session_id, state, last_heartbeat, capabilities_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			team_id = excluded.team_id,
			name = excluded.name,
			profile = excluded.profile,
			session_id = excluded.session_id,
			state = excluded.state,
			last_heartbeat = excluded.last_heartbeat,
			capabilities_json = excluded.capabilities_json,
			updated_at = excluded.updated_at
	`, mate.ID, mate.TeamID, mate.Name, mate.Profile, mate.SessionID, string(mate.State),
		formatTime(mate.LastHeartbeat), capabilitiesJSON, formatTime(mate.CreatedAt), formatTime(mate.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert Team Run teammate projection: %w", err)
	}
	return nil
}

func appendTeamRunEventTx(ctx context.Context, tx *sql.Tx, event TeamEvent) (int64, agentcontrol.WakeEvent, error) {
	teamID := strings.TrimSpace(event.TeamID)
	eventType := strings.TrimSpace(event.Type)
	if teamID == "" || eventType == "" {
		return 0, agentcontrol.WakeEvent{}, fmt.Errorf("Team Run event team id and type are required")
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	payloadJSON, err := encodeMetadata(event.Payload)
	if err != nil {
		return 0, agentcontrol.WakeEvent{}, err
	}
	createdAt := formatTime(event.Timestamp)
	var seq int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0) + 1 FROM team_events WHERE team_id = ?
	`, teamID).Scan(&seq); err != nil {
		return 0, agentcontrol.WakeEvent{}, fmt.Errorf("next Team Run event seq: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_events (team_id, seq, type, payload_json, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, teamID, seq, eventType, payloadJSON, createdAt); err != nil {
		return 0, agentcontrol.WakeEvent{}, fmt.Errorf("insert Team Run event: %w", err)
	}
	if strings.HasPrefix(eventType, "task.") {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO agent_control_task_graph_events (
				workflow, team_id, team_seq, type, payload_json, created_at
			) VALUES (?, ?, ?, ?, ?, ?)
		`, agentcontrol.WorkflowSpawnTeam, teamID, seq, eventType, payloadJSON, createdAt); err != nil {
			return 0, agentcontrol.WakeEvent{}, fmt.Errorf("insert Team Run graph event: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_control_wake_events (
			workflow, kind, team_id, team_seq, event_kind, status, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, agentcontrol.WorkflowSpawnTeam, agentcontrol.WakeKindTeam, teamID, seq, eventType, "", createdAt)
	if err != nil {
		return 0, agentcontrol.WakeEvent{}, fmt.Errorf("insert Team Run wake event: %w", err)
	}
	wakeSeq, err := result.LastInsertId()
	if err != nil {
		return 0, agentcontrol.WakeEvent{}, fmt.Errorf("Team Run wake event id: %w", err)
	}
	return seq, agentcontrol.WakeEvent{
		Seq:       wakeSeq,
		Workflow:  agentcontrol.WorkflowSpawnTeam,
		Kind:      agentcontrol.WakeKindTeam,
		TeamID:    teamID,
		TeamSeq:   seq,
		EventKind: eventType,
		CreatedAt: event.Timestamp,
	}.Normalize(), nil
}

var _ TeamRunStarter = (*SQLiteStore)(nil)
var _ TeamRunPreflighter = (*SQLiteStore)(nil)
