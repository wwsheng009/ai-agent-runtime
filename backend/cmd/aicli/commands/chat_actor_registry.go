package commands

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type localActorRegistry struct {
	Host *localChatRuntimeHost
}

func newLocalActorRegistry(host *localChatRuntimeHost) *localActorRegistry {
	return &localActorRegistry{Host: host}
}

func (r *localActorRegistry) SubmitPrompt(ctx context.Context, sessionID, prompt string, runMeta *team.RunMeta) (*team.SessionResult, error) {
	if r == nil || r.Host == nil || r.Host.SessionHub == nil {
		return nil, fmt.Errorf("session hub not configured")
	}
	if err := r.ensureSession(ctx, sessionID); err != nil {
		return nil, err
	}
	actor, err := r.Host.SessionHub.GetOrCreate(strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	result, err := actor.SubmitPrompt(ctx, prompt, runMeta)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("session result is nil")
	}
	return &team.SessionResult{
		Success:      result.Success,
		Output:       result.Output,
		Error:        result.Error,
		TraceID:      result.TraceID,
		Steps:        result.Steps,
		Observations: team.SessionObservationsFromRuntime(result.Observations),
	}, nil
}

func (r *localActorRegistry) TriggerTask(ctx context.Context, request team.TaskTriggerRequest) (*team.SessionResult, error) {
	store := team.Store(nil)
	if r != nil && r.Host != nil {
		store = r.Host.TeamStore
	}
	_, _ = team.AppendTaskDispatchRequested(ctx, store, request)
	_ = r.deliverAgentMailboxEvent(ctx, request.SessionID, team.BuildTaskAssignmentMailboxMessage(request))
	result, err := r.SubmitPrompt(ctx, request.SessionID, request.Prompt, request.RunMeta)
	_, _ = team.AppendTaskDispatchCompleted(ctx, store, request, result, err)
	return result, err
}

func (r *localActorRegistry) DispatchTeamMailboxMessage(ctx context.Context, message team.MailMessage) error {
	if r == nil || r.Host == nil || r.Host.TeamStore == nil {
		return nil
	}
	targets, err := r.resolveMailboxSessionTargets(ctx, message)
	if err != nil {
		return err
	}
	for _, sessionID := range targets {
		if err := r.ensureSession(ctx, sessionID); err != nil {
			return err
		}
		if err := r.deliverAgentMailboxEvent(ctx, sessionID, message); err != nil {
			return err
		}
	}
	return nil
}

func (r *localActorRegistry) EnsureTeammateSessionIDs(teamID string, specs []toolbroker.SpawnTeammateSpec) []toolbroker.SpawnTeammateSpec {
	return ensureTeammateSessionIDs(teamID, specs)
}

func (r *localActorRegistry) SyncTeamTeammateAgent(ctx context.Context, previous *team.Teammate, mate team.Teammate) error {
	if r == nil || r.Host == nil || r.Host.TeamStore == nil {
		return nil
	}
	if err := r.closeStaleTeamTeammateAgentRecord(ctx, previous, mate); err != nil {
		return err
	}
	teamID := strings.TrimSpace(mate.TeamID)
	sessionID := strings.TrimSpace(mate.SessionID)
	if teamID == "" || sessionID == "" {
		return nil
	}
	record, err := r.Host.TeamStore.GetTeam(ctx, teamID)
	if err != nil || record == nil {
		return err
	}
	return r.upsertTeamTeammateAgentRecordByProjection(ctx, mate)
}

func (r *localActorRegistry) closeStaleTeamTeammateAgentRecord(ctx context.Context, previous *team.Teammate, mate team.Teammate) error {
	store := r.localAgentRegistryStore()
	if store == nil || r == nil || r.Host == nil || r.Host.TeamStore == nil || previous == nil {
		return nil
	}
	previousRecord, ok, err := r.teamTeammateAgentRecordProjection(ctx, *previous)
	if err != nil || !ok {
		return err
	}
	currentRecord, currentOK, err := r.teamTeammateAgentRecordProjection(ctx, mate)
	if err != nil {
		return err
	}
	if currentOK && strings.EqualFold(previousRecord.RootSessionID, currentRecord.RootSessionID) && strings.EqualFold(previousRecord.AgentPath, currentRecord.AgentPath) {
		return nil
	}
	_, err = store.CloseAgentControlAgentSubtree(ctx, previousRecord.RootSessionID, previousRecord.AgentPath, time.Now().UTC())
	return err
}

func (r *localActorRegistry) upsertTeamTeammateAgentRecordByProjection(ctx context.Context, mate team.Teammate) error {
	store := r.localAgentRegistryStore()
	if store == nil {
		return nil
	}
	teammate, ok, err := r.teamTeammateAgentRecordProjection(ctx, mate)
	if err != nil || !ok {
		return err
	}
	root := agentcontrol.AgentRecord{
		AgentID:       localRootAgentID(teammate.RootSessionID),
		RootSessionID: teammate.RootSessionID,
		SessionID:     localRootSessionBinding(teammate),
		AgentPath:     "/root",
		AgentType:     agentcontrol.AgentTypeRoot,
		Status:        agentcontrol.AgentStatusActive,
	}
	if _, err := store.UpsertAgentControlAgent(ctx, root); err != nil {
		return err
	}
	if existing, exists, err := r.existingLocalAgentRecord(ctx, store, teammate); err != nil {
		return err
	} else if exists && existing.Closed() {
		return nil
	}
	_, err = store.UpsertAgentControlAgent(ctx, teammate)
	return err
}

func (r *localActorRegistry) teamTeammateAgentRecordProjection(ctx context.Context, mate team.Teammate) (agentcontrol.AgentRecord, bool, error) {
	if r == nil || r.Host == nil || r.Host.TeamStore == nil {
		return agentcontrol.AgentRecord{}, false, nil
	}
	teamID := strings.TrimSpace(mate.TeamID)
	sessionID := strings.TrimSpace(mate.SessionID)
	if teamID == "" || sessionID == "" {
		return agentcontrol.AgentRecord{}, false, nil
	}
	record, err := r.Host.TeamStore.GetTeam(ctx, teamID)
	if err != nil {
		return agentcontrol.AgentRecord{}, false, err
	}
	if record == nil {
		return agentcontrol.AgentRecord{}, false, nil
	}
	rootSessionID := firstNonEmptyChatValue(strings.TrimSpace(record.LeadSessionID), strings.TrimSpace(r.Host.baseRuntimeSessionID()))
	if rootSessionID == "" {
		rootSessionID = "team:" + teamID
	}
	if rootSessionID == "" {
		return agentcontrol.AgentRecord{}, false, nil
	}
	return agentcontrol.AgentRecord{
		AgentID:         "team:" + teamID + ":" + firstNonEmptyChatValue(strings.TrimSpace(mate.ID), sessionID),
		RootSessionID:   rootSessionID,
		ParentAgentID:   localRootAgentID(rootSessionID),
		ParentSessionID: rootSessionID,
		SessionID:       sessionID,
		AgentPath:       agentcontrol.TeamTeammatePath(teamID, mate.ID, mate.Name, sessionID),
		Depth:           1,
		AgentType:       firstNonEmptyChatValue(strings.TrimSpace(mate.Profile), agentcontrol.AgentTypeTeamTeammate),
		Nickname:        strings.TrimSpace(mate.Name),
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		TeamID:          teamID,
		TeammateID:      strings.TrimSpace(mate.ID),
		Status:          agentcontrol.AgentStatusActive,
		CreatedAt:       mate.CreatedAt,
		UpdatedAt:       mate.UpdatedAt,
	}, true, nil
}

func localRootSessionBinding(record agentcontrol.AgentRecord) string {
	rootSessionID := strings.TrimSpace(record.RootSessionID)
	if rootSessionID == "" || strings.EqualFold(rootSessionID, "team:"+strings.TrimSpace(record.TeamID)) {
		return ""
	}
	return rootSessionID
}

type localAgentForkMode int

const (
	localAgentForkNone localAgentForkMode = iota
	localAgentForkAll
	localAgentForkLastN
)

func resolveLocalAgentForkMode(args toolbroker.SpawnAgentArgs) (localAgentForkMode, int, error) {
	forkTurns := strings.ToLower(strings.TrimSpace(args.ForkTurns))
	if forkTurns != "" {
		switch forkTurns {
		case "none":
			return localAgentForkNone, 0, nil
		case "all":
			return localAgentForkAll, 0, nil
		default:
			n, err := strconv.Atoi(forkTurns)
			if err != nil || n <= 0 {
				return localAgentForkNone, 0, fmt.Errorf("fork_turns must be none, all, or a positive integer")
			}
			return localAgentForkLastN, n, nil
		}
	}
	if args.ForkContext != nil && *args.ForkContext {
		return localAgentForkAll, 0, nil
	}
	return localAgentForkNone, 0, nil
}

func (r *localActorRegistry) Spawn(ctx context.Context, parentSessionID string, args toolbroker.SpawnAgentArgs) (*toolbroker.AgentStatusResult, error) {
	if r == nil || r.Host == nil || r.Host.SessionStore == nil || r.Host.SessionHub == nil {
		return nil, fmt.Errorf("session runtime host is not configured")
	}
	sessionID := firstNonEmptyChatValue(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	parentSessionID = firstNonEmptyChatValue(strings.TrimSpace(parentSessionID), r.Host.baseRuntimeSessionID())

	var parentSession *runtimechat.Session
	if parentSessionID != "" {
		parent, err := r.Host.SessionStore.Load(ctx, parentSessionID)
		if err == nil {
			parentSession = parent
		} else if err != runtimechat.ErrSessionNotFound {
			return nil, err
		}
	}
	childDepth := localAgentChildDepth(parentSession)
	if err := r.enforceLocalAgentSpawnLimits(ctx, parentSession, parentSessionID, childDepth); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.ForkTurns) == "" && args.ForkContext == nil {
		args.ForkTurns = strings.TrimSpace(r.localAgentsConfig().DefaultForkTurns)
	}
	forkMode, forkTurns, err := resolveLocalAgentForkMode(args)
	if err != nil {
		return nil, err
	}

	if sessionID != "" {
		existing, err := r.Host.SessionStore.Load(ctx, sessionID)
		if err == nil && existing != nil {
			return nil, fmt.Errorf("session already exists: %s", sessionID)
		}
		if err != nil && err != runtimechat.ErrSessionNotFound {
			return nil, err
		}
	}

	userID := firstNonEmptyChatValue(strings.TrimSpace(r.Host.SessionUser), "agent")
	var childSession *runtimechat.Session
	if forkMode != localAgentForkNone && parentSession != nil {
		childSession = parentSession.Clone()
		if forkMode == localAgentForkLastN {
			childSession.ReplaceHistory(parentSession.GetRecentMessages(forkTurns))
		}
		if sessionID == "" {
			sessionID = runtimechat.NewSession(userID).ID
		}
		childSession.ID = sessionID
		childSession.UserID = userID
		childSession.UpdateState(runtimechat.StateActive)
	} else {
		childSession = runtimechat.NewSession(userID)
		if sessionID == "" {
			sessionID = childSession.ID
		} else {
			childSession.ID = sessionID
		}
	}
	childSession.SetContext(toolbroker.AgentSessionContextParentSessionID, parentSessionID)
	childSession.SetContext(toolbroker.AgentSessionContextRootSessionID, localAgentRootSessionID(parentSession, parentSessionID))
	childSession.SetContext(toolbroker.AgentSessionContextPath, localAgentChildPath(parentSession, sessionID))
	childSession.SetContext(toolbroker.AgentSessionContextDepth, childDepth)
	if agentType := strings.TrimSpace(args.AgentType); agentType != "" {
		childSession.SetContext(toolbroker.AgentSessionContextAgentType, agentType)
	}
	if model := strings.TrimSpace(args.Model); model != "" {
		childSession.SetContext(toolbroker.AgentSessionContextRequestedModel, model)
	}
	if err := r.Host.SessionStore.Save(ctx, childSession); err != nil {
		return nil, err
	}
	if err := r.reserveOrRegisterLocalAgentSpawn(ctx, parentSession, parentSessionID, childSession, args, childDepth); err != nil {
		_ = r.Host.SessionStore.Delete(ctx, childSession.ID)
		return nil, err
	}
	r.subscribeLocalAgentCompletion(parentSessionID, childSession)

	actor, err := r.Host.SessionHub.GetOrCreate(sessionID)
	if err != nil {
		return nil, err
	}
	queued := false
	if message := strings.TrimSpace(args.Message); message != "" {
		if err := actor.SubmitPromptAsync(ctx, message, nil); err != nil {
			return nil, err
		}
		queued = true
	}
	result, err := r.agentSnapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result.Created = true
	result.Queued = queued
	return result, nil
}

func (r *localActorRegistry) subscribeLocalAgentCompletion(parentSessionID string, childSession *runtimechat.Session) {
	if r == nil || r.Host == nil || r.Host.EventBus == nil || r.Host.EventStore == nil || childSession == nil {
		return
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	childSessionID := strings.TrimSpace(childSession.ID)
	if parentSessionID == "" || childSessionID == "" {
		return
	}
	childPath := localAgentSessionPath(childSession)
	childDepth := localAgentSessionDepth(childSession)
	childType := ""
	if value, ok := childSession.GetContext(toolbroker.AgentSessionContextAgentType); ok {
		if text, ok := value.(string); ok {
			childType = strings.TrimSpace(text)
		}
	}

	var unsubscribe func()
	handler := func(event runtimeevents.Event) {
		if !strings.EqualFold(strings.TrimSpace(event.SessionID), childSessionID) {
			return
		}
		eventType := strings.TrimSpace(event.Type)
		if eventType != runtimechat.EventSessionEnd && eventType != runtimechat.EventSessionInterrupted {
			return
		}
		if unsubscribe != nil {
			unsubscribe()
		}
		payload := map[string]interface{}{
			"agent_id":          childSessionID,
			"session_id":        childSessionID,
			"parent_session_id": parentSessionID,
			"path":              childPath,
			"source_event_type": eventType,
			"status":            localAgentCompletionStatus(event),
		}
		if childDepth > 0 {
			payload["depth"] = childDepth
		}
		if childType != "" {
			payload["agent_type"] = childType
			payload["role"] = childType
		}
		copyLocalAgentCompletionPayload(payload, event.Payload)
		completionMessage, mailboxErr := r.deliverSubagentCompletionMailbox(context.Background(), parentSessionID, childSessionID, childPath, childType, eventType, payload)
		payload = toolbroker.AnnotateSubagentCompletionDisplayMirror(payload, completionMessage, mailboxErr)
		mirrored := runtimeevents.Event{
			Type:      "subagent.completed",
			TraceID:   strings.TrimSpace(event.TraceID),
			AgentName: "agent-controller",
			SessionID: parentSessionID,
			Payload:   payload,
			Timestamp: event.Timestamp,
		}
		if mirrored.Timestamp.IsZero() {
			mirrored.Timestamp = time.Now().UTC()
		}
		if seq, err := r.Host.EventStore.AppendEvent(context.Background(), mirrored); err == nil {
			if mirrored.Payload == nil {
				mirrored.Payload = map[string]interface{}{}
			}
			mirrored.Payload["seq"] = seq
		}
		r.Host.EventBus.Publish(mirrored)
	}
	unsubscribe = r.Host.EventBus.SubscribeCancelable("", handler)
}

func (r *localActorRegistry) deliverSubagentCompletionMailbox(ctx context.Context, parentSessionID, childSessionID, childPath, childType, sourceEventType string, payload map[string]interface{}) (team.MailMessage, error) {
	if r == nil || r.Host == nil {
		return team.MailMessage{}, nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	childSessionID = strings.TrimSpace(childSessionID)
	if parentSessionID == "" || childSessionID == "" {
		return team.MailMessage{}, nil
	}
	message := toolbroker.BuildSubagentCompletionMailboxMessage(parentSessionID, childSessionID, childPath, childType, sourceEventType, payload)
	message.ID = "subagent_completed_" + sanitizeLocalAgentPathSegment(childSessionID)
	err := runtimechat.DeliverMailboxEventFirst(ctx, r.Host.EventStore, r.Host.EventBus, r.deliverMailboxToActor, parentSessionID, message)
	return message, err
}

func (r *localActorRegistry) localAgentRegistryStore() agentcontrol.AgentRegistryStore {
	if r == nil || r.Host == nil {
		return nil
	}
	return r.Host.AgentRegistryStore
}

func (r *localActorRegistry) reserveOrRegisterLocalAgentSpawn(ctx context.Context, parentSession *runtimechat.Session, parentSessionID string, childSession *runtimechat.Session, args toolbroker.SpawnAgentArgs, childDepth int) error {
	store := r.localAgentRegistryStore()
	if store == nil || childSession == nil {
		return nil
	}
	childSessionID := strings.TrimSpace(childSession.ID)
	if childSessionID == "" {
		return fmt.Errorf("child session id is required")
	}
	rootRecord := localRootAgentRecord(parentSession, parentSessionID)
	childRecord := localChildAgentRecord(parentSession, parentSessionID, childSession, args, childDepth)
	if err := r.closeStaleLocalAgentSessionBinding(ctx, store, rootRecord); err != nil {
		return err
	}
	if err := r.closeStaleLocalAgentSessionBinding(ctx, store, childRecord); err != nil {
		return err
	}
	if reserver, ok := store.(agentcontrol.AgentSpawnReservationStore); ok && reserver != nil {
		_, err := reserver.ReserveAgentControlAgentSpawn(ctx, rootRecord, childRecord, r.localAgentsConfig().MaxThreads)
		return err
	}
	if _, err := store.UpsertAgentControlAgent(ctx, rootRecord); err != nil {
		return err
	}
	_, err := store.UpsertAgentControlAgent(ctx, childRecord)
	return err
}

func localRootAgentRecord(parentSession *runtimechat.Session, parentSessionID string) agentcontrol.AgentRecord {
	parentSessionID = strings.TrimSpace(parentSessionID)
	rootSessionID := localAgentRootSessionID(parentSession, parentSessionID)
	if rootSessionID == "" {
		rootSessionID = parentSessionID
	}
	return agentcontrol.AgentRecord{
		AgentID:       localRootAgentID(rootSessionID),
		RootSessionID: rootSessionID,
		SessionID:     rootSessionID,
		AgentPath:     "/root",
		AgentType:     agentcontrol.AgentTypeRoot,
		Status:        agentcontrol.AgentStatusActive,
	}
}

func localChildAgentRecord(parentSession *runtimechat.Session, parentSessionID string, childSession *runtimechat.Session, args toolbroker.SpawnAgentArgs, childDepth int) agentcontrol.AgentRecord {
	childSessionID := ""
	if childSession != nil {
		childSessionID = strings.TrimSpace(childSession.ID)
	}
	rootSessionID := localAgentRootSessionID(parentSession, strings.TrimSpace(parentSessionID))
	agentType := firstNonEmptyChatValue(strings.TrimSpace(args.AgentType), agentcontrol.AgentTypeChild)
	return agentcontrol.AgentRecord{
		AgentID:         childSessionID,
		RootSessionID:   rootSessionID,
		ParentAgentID:   localAgentIDForSession(parentSession, strings.TrimSpace(parentSessionID)),
		ParentSessionID: strings.TrimSpace(parentSessionID),
		SessionID:       childSessionID,
		AgentPath:       localAgentChildPath(parentSession, childSessionID),
		Depth:           childDepth,
		AgentType:       agentType,
		Workflow:        agentcontrol.WorkflowSpawnAgent,
		Status:          agentcontrol.AgentStatusActive,
	}
}

func localAgentIDForSession(session *runtimechat.Session, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if session == nil || !isLocalAgentSession(session) {
		return localRootAgentID(localAgentRootSessionID(session, sessionID))
	}
	return sessionID
}

func localRootAgentID(rootSessionID string) string {
	rootSessionID = strings.TrimSpace(rootSessionID)
	if rootSessionID == "" {
		return "root"
	}
	return "root:" + rootSessionID
}

func firstNonZeroChatInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func (r *localActorRegistry) listLocalAgentsFromRegistry(ctx context.Context, parentSessionID string, args toolbroker.ListAgentsArgs, store agentcontrol.AgentRegistryStore) (*toolbroker.AgentListResult, error) {
	if store == nil {
		return nil, nil
	}
	rootSessionID, parentPath, err := r.localAgentRegistryRootAndPath(ctx, parentSessionID)
	if err != nil {
		return nil, err
	}
	if rootSessionID == "" {
		return nil, nil
	}
	pathPrefix := strings.TrimSpace(args.PathPrefix)
	if pathPrefix == "" && parentPath != "" && parentPath != "/root" {
		pathPrefix = parentPath
	}
	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: rootSessionID,
		PathPrefix:    pathPrefix,
		IncludeClosed: args.IncludeClosed,
	})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	agents := make([]toolbroker.AgentStatusResult, 0, len(records))
	for _, record := range records {
		record = record.Normalize()
		if record.AgentPath == "/root" || strings.EqualFold(record.AgentType, agentcontrol.AgentTypeRoot) {
			continue
		}
		status, err := r.localAgentStatusFromRecord(ctx, record)
		if err != nil {
			return nil, err
		}
		agents = append(agents, status)
	}
	sort.SliceStable(agents, func(i, j int) bool {
		left := firstNonEmptyChatValue(agents[i].Path, agents[i].SessionID, agents[i].ID)
		right := firstNonEmptyChatValue(agents[j].Path, agents[j].SessionID, agents[j].ID)
		return left < right
	})
	return &toolbroker.AgentListResult{Agents: agents, Count: len(agents)}, nil
}

func (r *localActorRegistry) localAgentRegistryRootAndPath(ctx context.Context, parentSessionID string) (string, string, error) {
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return "", "", nil
	}
	if strings.HasPrefix(parentSessionID, "/") {
		if store := r.localAgentRegistryStore(); store != nil {
			records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
				AgentPath:     parentSessionID,
				IncludeClosed: true,
				Limit:         1,
			})
			if err != nil {
				return "", "", err
			}
			if len(records) > 0 {
				return records[0].RootSessionID, records[0].AgentPath, nil
			}
		}
		return "", parentSessionID, nil
	}
	parent, err := r.Host.SessionStore.Load(ctx, parentSessionID)
	if err != nil {
		if err == runtimechat.ErrSessionNotFound {
			return parentSessionID, "", nil
		}
		return "", "", err
	}
	return localAgentRootSessionID(parent, parentSessionID), localAgentSessionPath(parent), nil
}

func (r *localActorRegistry) localAgentStatusFromRecord(ctx context.Context, record agentcontrol.AgentRecord) (toolbroker.AgentStatusResult, error) {
	sessionID := strings.TrimSpace(record.SessionID)
	var result *toolbroker.AgentStatusResult
	if sessionID != "" {
		snapshot, err := r.agentSnapshot(ctx, sessionID)
		if err != nil {
			return toolbroker.AgentStatusResult{}, err
		}
		result = snapshot
	}
	if result == nil {
		result = &toolbroker.AgentStatusResult{
			ID:        firstNonEmptyChatValue(record.AgentID, record.SessionID),
			SessionID: sessionID,
			Status:    "missing",
		}
	}
	result.ID = firstNonEmptyChatValue(strings.TrimSpace(result.ID), record.AgentID, sessionID)
	result.SessionID = firstNonEmptyChatValue(strings.TrimSpace(result.SessionID), sessionID)
	result.ParentSessionID = firstNonEmptyChatValue(strings.TrimSpace(result.ParentSessionID), record.ParentSessionID)
	result.Path = firstNonEmptyChatValue(strings.TrimSpace(result.Path), record.AgentPath)
	result.Depth = firstNonZeroChatInt(result.Depth, record.Depth)
	result.AgentType = firstNonEmptyChatValue(strings.TrimSpace(result.AgentType), record.AgentType)
	result.TeamID = firstNonEmptyChatValue(strings.TrimSpace(result.TeamID), record.TeamID)
	result.TeammateID = firstNonEmptyChatValue(strings.TrimSpace(result.TeammateID), record.TeammateID)
	if record.Closed() {
		result.Status = string(runtimechat.SessionStopped)
		if result.SessionState == "" {
			result.SessionState = string(runtimechat.StateClosed)
		}
	}
	return *result, nil
}

func (r *localActorRegistry) materializeLocalAgentRegistry(ctx context.Context) error {
	store := r.localAgentRegistryStore()
	if store == nil {
		return nil
	}
	records, err := r.projectLocalAgentRecords(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		record = record.Normalize()
		if record.AgentID == "" || record.RootSessionID == "" || record.AgentPath == "" {
			continue
		}
		existing, exists, err := r.existingLocalAgentRecord(ctx, store, record)
		if err != nil {
			return err
		}
		if exists && existing.Closed() && !record.Closed() {
			continue
		}
		if err := r.closeStaleLocalAgentSessionBinding(ctx, store, record); err != nil {
			return err
		}
		if _, err := store.UpsertAgentControlAgent(ctx, record); err != nil {
			return err
		}
	}
	return nil
}

func (r *localActorRegistry) closeStaleLocalAgentSessionBinding(ctx context.Context, store agentcontrol.AgentRegistryStore, record agentcontrol.AgentRecord) error {
	if store == nil {
		return nil
	}
	record = record.Normalize()
	if record.SessionID == "" {
		return nil
	}
	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		SessionID: record.SessionID,
	})
	if err != nil {
		return err
	}
	for _, existing := range records {
		existing = existing.Normalize()
		if sameLocalAgentBinding(existing, record) {
			continue
		}
		if existing.RootSessionID == "" || existing.AgentPath == "" {
			continue
		}
		if _, err := store.CloseAgentControlAgentSubtree(ctx, existing.RootSessionID, existing.AgentPath, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func sameLocalAgentBinding(left, right agentcontrol.AgentRecord) bool {
	left = left.Normalize()
	right = right.Normalize()
	if left.AgentID != "" && right.AgentID != "" && strings.EqualFold(left.AgentID, right.AgentID) {
		return true
	}
	return left.RootSessionID != "" &&
		right.RootSessionID != "" &&
		left.AgentPath != "" &&
		right.AgentPath != "" &&
		strings.EqualFold(left.RootSessionID, right.RootSessionID) &&
		strings.EqualFold(left.AgentPath, right.AgentPath)
}

func (r *localActorRegistry) existingLocalAgentRecord(ctx context.Context, store agentcontrol.AgentRegistryStore, record agentcontrol.AgentRecord) (agentcontrol.AgentRecord, bool, error) {
	if store == nil {
		return agentcontrol.AgentRecord{}, false, nil
	}
	if agentID := strings.TrimSpace(record.AgentID); agentID != "" {
		records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
			AgentID:       agentID,
			IncludeClosed: true,
			Limit:         1,
		})
		if err != nil {
			return agentcontrol.AgentRecord{}, false, err
		}
		if len(records) > 0 {
			return records[0].Normalize(), true, nil
		}
	}
	if rootSessionID, agentPath := strings.TrimSpace(record.RootSessionID), strings.TrimSpace(record.AgentPath); rootSessionID != "" && agentPath != "" {
		records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
			RootSessionID: rootSessionID,
			AgentPath:     agentPath,
			IncludeClosed: true,
			Limit:         1,
		})
		if err != nil {
			return agentcontrol.AgentRecord{}, false, err
		}
		if len(records) > 0 {
			return records[0].Normalize(), true, nil
		}
	}
	return agentcontrol.AgentRecord{}, false, nil
}

func (r *localActorRegistry) projectLocalAgentRecords(ctx context.Context) ([]agentcontrol.AgentRecord, error) {
	records := make([]agentcontrol.AgentRecord, 0)
	sessionRecords, err := r.projectLocalSessionAgentRecords(ctx)
	if err != nil {
		return nil, err
	}
	records = append(records, sessionRecords...)
	teamRecords, err := r.projectLocalTeamAgentRecords(ctx)
	if err != nil {
		return nil, err
	}
	records = append(records, teamRecords...)
	return dedupeLocalAgentRecords(records), nil
}

func (r *localActorRegistry) projectLocalSessionAgentRecords(ctx context.Context) ([]agentcontrol.AgentRecord, error) {
	sessions, err := r.listLocalAgentSessions(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]agentcontrol.AgentRecord, 0, len(sessions)+1)
	roots := map[string]agentcontrol.AgentRecord{}
	for _, session := range sessions {
		if session == nil {
			continue
		}
		if changed, applyErr := r.applyTeamTeammateAgentContext(ctx, session); applyErr != nil {
			return nil, applyErr
		} else if changed && r.Host != nil && r.Host.SessionStore != nil {
			if err := r.Host.SessionStore.Update(ctx, session); err != nil {
				return nil, err
			}
		}
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" {
			continue
		}
		rootID := localAgentRootSessionID(session, sessionID)
		if rootID == "" {
			continue
		}
		if _, exists := roots[rootID]; !exists {
			roots[rootID] = agentcontrol.AgentRecord{
				AgentID:       localRootAgentID(rootID),
				RootSessionID: rootID,
				SessionID:     rootID,
				AgentPath:     "/root",
				AgentType:     agentcontrol.AgentTypeRoot,
				Status:        agentcontrol.AgentStatusActive,
			}
		}
		if !isLocalAgentSession(session) {
			continue
		}
		parentSessionID := ""
		if value, ok := session.GetContext(toolbroker.AgentSessionContextParentSessionID); ok {
			if text, ok := value.(string); ok {
				parentSessionID = strings.TrimSpace(text)
			}
		}
		agentID := sessionID
		record := agentcontrol.AgentRecord{
			AgentID:         agentID,
			RootSessionID:   rootID,
			ParentAgentID:   localRootAgentID(rootID),
			ParentSessionID: parentSessionID,
			SessionID:       sessionID,
			AgentPath:       localAgentSessionPath(session),
			Depth:           localAgentSessionDepth(session),
			AgentType:       agentcontrol.AgentTypeChild,
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			Status:          agentcontrol.AgentStatusActive,
		}
		if parentSessionID != "" && !strings.EqualFold(parentSessionID, rootID) {
			record.ParentAgentID = parentSessionID
		}
		if agentType := agentcontrol.ContextString(session, toolbroker.AgentSessionContextAgentType); agentType != "" {
			record.AgentType = agentType
		}
		if teamID := agentcontrol.ContextString(session, toolbroker.AgentSessionContextTeamID); teamID != "" {
			record.TeamID = teamID
			record.Workflow = agentcontrol.WorkflowSpawnTeam
		}
		if teammateID := agentcontrol.ContextString(session, toolbroker.AgentSessionContextTeammateID); teammateID != "" {
			record.TeammateID = teammateID
			record.Workflow = agentcontrol.WorkflowSpawnTeam
		}
		if record.TeamID != "" {
			record.AgentID = "team:" + record.TeamID + ":" + firstNonEmptyChatValue(record.TeammateID, sessionID)
			record.ParentAgentID = localRootAgentID(rootID)
		}
		if isClosedLocalAgentSession(session) {
			record.Status = agentcontrol.AgentStatusClosed
			closedAt := time.Now().UTC()
			record.ClosedAt = &closedAt
		}
		records = append(records, record)
	}
	for _, root := range roots {
		records = append(records, root)
	}
	return records, nil
}

func (r *localActorRegistry) projectLocalTeamAgentRecords(ctx context.Context) ([]agentcontrol.AgentRecord, error) {
	if r == nil || r.Host == nil || r.Host.TeamStore == nil {
		return nil, nil
	}
	teams, err := r.Host.TeamStore.ListTeams(ctx, team.TeamFilter{})
	if err != nil {
		return nil, err
	}
	records := make([]agentcontrol.AgentRecord, 0)
	for _, teamRecord := range teams {
		teamID := strings.TrimSpace(teamRecord.ID)
		if teamID == "" {
			continue
		}
		rootSessionID := firstNonEmptyChatValue(strings.TrimSpace(teamRecord.LeadSessionID), strings.TrimSpace(r.Host.baseRuntimeSessionID()))
		rootSessionIDIsSynthetic := false
		if rootSessionID == "" {
			rootSessionID = "team:" + teamID
			rootSessionIDIsSynthetic = true
		}
		rootSessionBinding := rootSessionID
		if rootSessionIDIsSynthetic {
			rootSessionBinding = ""
		}
		records = append(records, agentcontrol.AgentRecord{
			AgentID:       localRootAgentID(rootSessionID),
			RootSessionID: rootSessionID,
			SessionID:     rootSessionBinding,
			AgentPath:     "/root",
			AgentType:     agentcontrol.AgentTypeRoot,
			Status:        agentcontrol.AgentStatusActive,
		})
		teammates, err := r.Host.TeamStore.ListTeammates(ctx, teamID)
		if err != nil {
			return nil, err
		}
		for _, mate := range teammates {
			sessionID := strings.TrimSpace(mate.SessionID)
			if sessionID == "" {
				continue
			}
			path := agentcontrol.TeamTeammatePath(teamID, mate.ID, mate.Name, sessionID)
			agentID := "team:" + teamID + ":" + firstNonEmptyChatValue(strings.TrimSpace(mate.ID), sessionID)
			records = append(records, agentcontrol.AgentRecord{
				AgentID:         agentID,
				RootSessionID:   rootSessionID,
				ParentAgentID:   localRootAgentID(rootSessionID),
				ParentSessionID: rootSessionID,
				SessionID:       sessionID,
				AgentPath:       path,
				Depth:           1,
				AgentType:       firstNonEmptyChatValue(strings.TrimSpace(mate.Profile), agentcontrol.AgentTypeTeamTeammate),
				Nickname:        strings.TrimSpace(mate.Name),
				Workflow:        agentcontrol.WorkflowSpawnTeam,
				TeamID:          teamID,
				TeammateID:      strings.TrimSpace(mate.ID),
				Status:          agentcontrol.AgentStatusActive,
			})
		}
	}
	return records, nil
}

func dedupeLocalAgentRecords(records []agentcontrol.AgentRecord) []agentcontrol.AgentRecord {
	if len(records) == 0 {
		return nil
	}
	byID := make(map[string]agentcontrol.AgentRecord, len(records))
	ordered := make([]string, 0, len(records))
	for _, record := range records {
		record = record.Normalize()
		if record.AgentID == "" {
			continue
		}
		key := record.AgentID
		if _, exists := byID[key]; !exists {
			ordered = append(ordered, key)
		}
		if existing, exists := byID[key]; exists {
			if existing.Workflow == agentcontrol.WorkflowSpawnTeam && record.Workflow != agentcontrol.WorkflowSpawnTeam {
				continue
			}
			if existing.Closed() && !record.Closed() {
				continue
			}
		}
		byID[key] = record
	}
	out := make([]agentcontrol.AgentRecord, 0, len(ordered))
	for _, key := range ordered {
		out = append(out, byID[key])
	}
	return out
}

func (r *localActorRegistry) List(ctx context.Context, parentSessionID string, args toolbroker.ListAgentsArgs) (*toolbroker.AgentListResult, error) {
	baseSessionID := ""
	if r != nil && r.Host != nil {
		baseSessionID = r.Host.baseRuntimeSessionID()
	}
	parentSessionID = firstNonEmptyChatValue(strings.TrimSpace(args.ParentSessionID), strings.TrimSpace(parentSessionID), baseSessionID)
	if store := r.localAgentRegistryStore(); store != nil {
		if err := r.materializeLocalAgentRegistry(ctx); err != nil {
			return nil, err
		}
		result, err := r.listLocalAgentsFromRegistry(ctx, parentSessionID, args, store)
		if err != nil {
			return nil, err
		}
		if result != nil {
			return result, nil
		}
	}
	sessions, err := r.listLocalAgentSessions(ctx)
	if err != nil {
		return nil, err
	}
	for _, session := range sessions {
		changed, applyErr := r.applyTeamTeammateAgentContext(ctx, session)
		if applyErr != nil {
			return nil, applyErr
		}
		if changed {
			if err := r.Host.SessionStore.Update(ctx, session); err != nil {
				return nil, err
			}
		}
	}
	byID := make(map[string]*runtimechat.Session, len(sessions))
	for _, session := range sessions {
		if session != nil && strings.TrimSpace(session.ID) != "" {
			byID[strings.TrimSpace(session.ID)] = session
		}
	}

	rootSessionID := parentSessionID
	if parent := byID[parentSessionID]; parent != nil {
		rootSessionID = localAgentRootSessionID(parent, parentSessionID)
	}
	pathPrefix := strings.TrimSpace(args.PathPrefix)
	agents := make([]toolbroker.AgentStatusResult, 0)
	for _, session := range sessions {
		if session == nil || !isLocalAgentSession(session) {
			continue
		}
		if !args.IncludeClosed && isClosedLocalAgentSession(session) {
			continue
		}
		if rootSessionID != "" && localAgentRootSessionID(session, "") != rootSessionID && !localAgentHasAncestor(session, parentSessionID, byID) {
			continue
		}
		if parentSessionID != "" && rootSessionID == "" && !localAgentHasAncestor(session, parentSessionID, byID) {
			continue
		}
		path := localAgentSessionPath(session)
		if pathPrefix != "" && !strings.HasPrefix(path, pathPrefix) {
			continue
		}
		snapshot, err := r.agentSnapshot(ctx, strings.TrimSpace(session.ID))
		if err != nil {
			return nil, err
		}
		if snapshot != nil {
			agents = append(agents, *snapshot)
		}
	}
	sort.SliceStable(agents, func(i, j int) bool {
		left := firstNonEmptyChatValue(agents[i].Path, agents[i].SessionID, agents[i].ID)
		right := firstNonEmptyChatValue(agents[j].Path, agents[j].SessionID, agents[j].ID)
		return left < right
	})
	return &toolbroker.AgentListResult{Agents: agents, Count: len(agents)}, nil
}

func (r *localActorRegistry) SendMessage(ctx context.Context, fromSessionID string, args toolbroker.AgentMessageArgs) (*toolbroker.AgentMessageResult, error) {
	return r.deliverAgentMessage(ctx, fromSessionID, args, false)
}

func (r *localActorRegistry) FollowupTask(ctx context.Context, fromSessionID string, args toolbroker.AgentMessageArgs) (*toolbroker.AgentMessageResult, error) {
	return r.deliverAgentMessage(ctx, fromSessionID, args, true)
}

func (r *localActorRegistry) deliverAgentMessage(ctx context.Context, fromSessionID string, args toolbroker.AgentMessageArgs, trigger bool) (*toolbroker.AgentMessageResult, error) {
	if r == nil || r.Host == nil {
		return nil, fmt.Errorf("runtime host not configured")
	}
	sessionID := firstNonEmptyChatValue(strings.TrimSpace(args.Target), strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("target is required")
	}
	resolvedSessionID, err := r.resolveLocalAgentTargetSessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sessionID = resolvedSessionID
	message := strings.TrimSpace(args.Message)
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}
	if err := r.ensureSession(ctx, sessionID); err != nil {
		return nil, err
	}
	delivered := false
	triggered := false
	if trigger && !r.localAgentSessionBusy(ctx, sessionID) {
		if actor := r.localAgentActor(ctx, sessionID); actor != nil && !localAgentActorBusy(actor.State()) {
			if err := actor.SubmitPromptAsync(ctx, message, nil); err != nil {
				return nil, err
			}
			triggered = true
		}
	}
	if !triggered {
		mail := toolbroker.BuildAgentMailboxMessage(fromSessionID, sessionID, message, trigger)
		if err := r.deliverAgentMailboxEvent(ctx, sessionID, mail); err != nil {
			return nil, err
		}
		delivered = true
	}
	status, err := r.agentSnapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if status != nil && triggered {
		status.Queued = true
	}
	return &toolbroker.AgentMessageResult{
		TargetSessionID: sessionID,
		Delivered:       delivered || triggered,
		Triggered:       triggered,
		Status:          status,
	}, nil
}

func (r *localActorRegistry) localAgentSessionBusy(ctx context.Context, sessionID string) bool {
	if r == nil || r.Host == nil {
		return false
	}
	if r.Host.SessionHub != nil {
		if actor, ok := r.Host.SessionHub.Get(strings.TrimSpace(sessionID)); ok && actor != nil && localAgentActorBusy(actor.State()) {
			return true
		}
	}
	if r.Host.RuntimeStore != nil {
		state, err := r.Host.RuntimeStore.LoadState(ctx, strings.TrimSpace(sessionID))
		if err == nil && localAgentActorBusy(state) {
			return true
		}
	}
	return false
}

func (r *localActorRegistry) localAgentActor(ctx context.Context, sessionID string) *runtimechat.SessionActor {
	if r == nil || r.Host == nil || r.Host.SessionHub == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if actor, ok := r.Host.SessionHub.Get(sessionID); ok && actor != nil {
		return actor
	}
	actor, err := r.Host.SessionHub.GetOrCreate(sessionID)
	if err != nil {
		return nil
	}
	return actor
}

func (r *localActorRegistry) deliverAgentMailboxEvent(ctx context.Context, sessionID string, mail team.MailMessage) error {
	if r == nil || r.Host == nil {
		return fmt.Errorf("runtime host not configured")
	}
	return runtimechat.DeliverMailboxEventFirst(ctx, r.Host.EventStore, r.Host.EventBus, r.deliverMailboxToActor, sessionID, mail)
}

func (r *localActorRegistry) deliverMailboxToActor(ctx context.Context, sessionID string, mail team.MailMessage) error {
	actor := r.localAgentActor(ctx, sessionID)
	if actor == nil {
		return fmt.Errorf("session hub not configured")
	}
	return actor.DeliverMailboxMessage(ctx, mail)
}

func (r *localActorRegistry) SendInput(ctx context.Context, args toolbroker.SendAgentInputArgs) (*toolbroker.AgentStatusResult, error) {
	if r == nil || r.Host == nil || r.Host.SessionHub == nil {
		return nil, fmt.Errorf("session hub not configured")
	}
	sessionID := firstNonEmptyChatValue(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	resolvedSessionID, err := r.resolveLocalAgentTargetSessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sessionID = resolvedSessionID
	message := strings.TrimSpace(args.Message)
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}
	if err := r.ensureSession(ctx, sessionID); err != nil {
		return nil, err
	}
	actor, err := r.Host.SessionHub.GetOrCreate(sessionID)
	if err != nil {
		return nil, err
	}
	if state := actor.State(); state != nil {
		busy := state.Status == runtimechat.SessionRunning || state.Status == runtimechat.SessionRewinding || state.Status == runtimechat.SessionWaitingApproval || state.Status == runtimechat.SessionWaitingInput
		if busy {
			interrupt := args.Interrupt != nil && *args.Interrupt
			if !interrupt {
				return nil, fmt.Errorf("session is busy (%s)", state.Status)
			}
			if err := actor.Interrupt(ctx); err != nil {
				return nil, err
			}
		}
	}
	if err := actor.SubmitPromptAsync(ctx, message, nil); err != nil {
		return nil, err
	}
	result, err := r.agentSnapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result.Queued = true
	return result, nil
}

func (r *localActorRegistry) Wait(ctx context.Context, args toolbroker.WaitAgentArgs) (*toolbroker.AgentWaitResult, error) {
	if args.MailboxOnly {
		return r.waitForLocalAgentMailbox(ctx, args)
	}
	sessionIDs := normalizeLocalAgentWaitIDs(args)
	if len(sessionIDs) == 0 {
		return nil, fmt.Errorf("id is required")
	}
	for index, sessionID := range sessionIDs {
		resolvedSessionID, err := r.resolveLocalAgentTargetSessionID(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		sessionIDs[index] = resolvedSessionID
	}
	timeout := time.Duration(args.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		defaultWaitMs := r.localAgentsConfig().DefaultWaitTimeoutMs
		if defaultWaitMs <= 0 {
			defaultWaitMs = int((30 * time.Second).Milliseconds())
		}
		timeout = time.Duration(defaultWaitMs) * time.Millisecond
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	wakeCh, unsubscribe := r.subscribeLocalAgentWaitEvents(waitCtx, sessionIDs)
	defer unsubscribe()
	for {
		snapshots := make([]toolbroker.AgentStatusResult, 0, len(sessionIDs))
		var matched *toolbroker.AgentStatusResult
		readyCount := 0
		for _, sessionID := range sessionIDs {
			result, err := r.agentSnapshot(waitCtx, sessionID)
			if err != nil {
				return nil, err
			}
			if result == nil {
				continue
			}
			snapshots = append(snapshots, *result)
			if isLocalAgentWaitReady(result.Status) {
				readyCount++
				if matched == nil {
					cloned := *result
					matched = &cloned
				}
			}
		}
		waitResult := &toolbroker.AgentWaitResult{
			Agents:       snapshots,
			ReadyCount:   readyCount,
			PendingCount: len(snapshots) - readyCount,
		}
		if matched != nil {
			waitResult.Agent = matched
			waitResult.MatchedID = matched.ID
			waitResult.MatchedSessionID = matched.SessionID
			return waitResult, nil
		}
		select {
		case <-waitCtx.Done():
			waitResult.TimedOut = true
			return waitResult, nil
		case <-wakeCh:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (r *localActorRegistry) waitForLocalAgentMailbox(ctx context.Context, args toolbroker.WaitAgentArgs) (*toolbroker.AgentWaitResult, error) {
	if r == nil || r.Host == nil || r.Host.EventStore == nil {
		return nil, fmt.Errorf("event store not configured")
	}
	sessionID := firstNonEmptyChatValue(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID), r.Host.baseRuntimeSessionID())
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	timeout := time.Duration(args.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		defaultWaitMs := r.localAgentsConfig().DefaultWaitTimeoutMs
		if defaultWaitMs <= 0 {
			defaultWaitMs = int((30 * time.Second).Milliseconds())
		}
		timeout = time.Duration(defaultWaitMs) * time.Millisecond
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	wakeCh, unsubscribe := r.subscribeLocalAgentMailboxEvents(waitCtx, sessionID)
	defer unsubscribe()
	for {
		if events, ok, hasMailboxRows, err := r.listLocalAgentMailboxEvents(waitCtx, sessionID, args.AfterSeq, 64); err != nil {
			return nil, err
		} else if ok {
			if result := buildLocalAgentMailboxWaitResult(sessionID, events); result != nil {
				return result, nil
			}
			if hasMailboxRows {
				select {
				case <-waitCtx.Done():
					return &toolbroker.AgentWaitResult{TimedOut: true}, nil
				case <-wakeCh:
				case <-time.After(500 * time.Millisecond):
				}
				continue
			}
		}
		events, err := r.Host.EventStore.ListEvents(waitCtx, sessionID, args.AfterSeq, 64)
		if err != nil {
			return nil, err
		}
		if result := buildLocalAgentMailboxWaitResult(sessionID, events); result != nil {
			return result, nil
		}
		select {
		case <-waitCtx.Done():
			return &toolbroker.AgentWaitResult{TimedOut: true}, nil
		case <-wakeCh:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (r *localActorRegistry) subscribeLocalAgentWaitEvents(ctx context.Context, sessionIDs []string) (<-chan struct{}, func()) {
	if r == nil || r.Host == nil || len(sessionIDs) == 0 {
		return nil, func() {}
	}
	targets := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
			targets[sessionID] = struct{}{}
		}
	}
	if len(targets) == 0 {
		return nil, func() {}
	}
	wakeCh := make(chan struct{}, 1)
	wake := func() {
		select {
		case wakeCh <- struct{}{}:
		default:
		}
	}
	var unsubscribeStores []func()
	if watcher, ok := r.Host.EventStore.(runtimechat.EventWatcherStore); ok && watcher != nil {
		for sessionID := range targets {
			eventCh, unwatch := watcher.WatchEvents(ctx, sessionID)
			unsubscribeStores = append(unsubscribeStores, unwatch)
			go func(target string, ch <-chan runtimeevents.Event) {
				for {
					select {
					case <-ctx.Done():
						return
					case event := <-ch:
						if !strings.EqualFold(strings.TrimSpace(event.SessionID), target) {
							continue
						}
						if !isLocalAgentWaitWakeEvent(event.Type) {
							continue
						}
						wake()
					}
				}
			}(sessionID, eventCh)
		}
	}
	if r.Host.EventBus == nil {
		return wakeCh, func() {
			for _, unwatch := range unsubscribeStores {
				if unwatch != nil {
					unwatch()
				}
			}
		}
	}
	handler := func(event runtimeevents.Event) {
		if _, ok := targets[strings.TrimSpace(event.SessionID)]; !ok {
			return
		}
		if !isLocalAgentWaitWakeEvent(event.Type) {
			return
		}
		wake()
	}
	unsubscribeBus := r.Host.EventBus.SubscribeCancelable("", handler)
	return wakeCh, func() {
		for _, unwatch := range unsubscribeStores {
			if unwatch != nil {
				unwatch()
			}
		}
		if unsubscribeBus != nil {
			unsubscribeBus()
		}
	}
}

func (r *localActorRegistry) ReadEvents(ctx context.Context, args toolbroker.ReadAgentEventsArgs) (*toolbroker.AgentEventsResult, error) {
	if r == nil || r.Host == nil || r.Host.EventStore == nil {
		return nil, fmt.Errorf("event store not configured")
	}
	sessionID := firstNonEmptyChatValue(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		sessionID = r.Host.baseRuntimeSessionID()
	}
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	if !args.MailboxOnly {
		resolvedSessionID, err := r.resolveLocalAgentTargetSessionID(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		sessionID = resolvedSessionID
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 20
	}
	waitMs := args.WaitMs
	if waitMs < 0 {
		waitMs = 0
	}
	readCtx := ctx
	cancel := func() {}
	if waitMs > 0 {
		readCtx, cancel = context.WithTimeout(ctx, time.Duration(waitMs)*time.Millisecond)
	}
	defer cancel()
	wakeCh, unsubscribe := r.subscribeLocalAgentReadEvents(readCtx, sessionID)
	if args.MailboxOnly {
		unsubscribe()
		wakeCh, unsubscribe = r.subscribeLocalAgentMailboxEvents(readCtx, sessionID)
	}
	defer unsubscribe()
	for {
		if args.MailboxOnly {
			if events, ok, hasMailboxRows, err := r.listLocalAgentMailboxEvents(readCtx, sessionID, args.AfterSeq, limit); err != nil {
				return nil, err
			} else if ok {
				if len(events) > 0 || (hasMailboxRows && waitMs == 0) {
					return buildLocalAgentEventsResult(sessionID, events), nil
				}
				if hasMailboxRows {
					select {
					case <-readCtx.Done():
						result := buildLocalAgentEventsResult(sessionID, nil)
						result.TimedOut = true
						return result, nil
					case <-wakeCh:
					case <-time.After(500 * time.Millisecond):
					}
					continue
				}
			}
		}
		events, err := r.Host.EventStore.ListEvents(readCtx, sessionID, args.AfterSeq, limit)
		if err != nil {
			return nil, err
		}
		if args.MailboxOnly {
			events = filterLocalAgentMailboxWaitEvents(events)
		}
		if len(events) > 0 || waitMs == 0 {
			return buildLocalAgentEventsResult(sessionID, events), nil
		}
		select {
		case <-readCtx.Done():
			result := buildLocalAgentEventsResult(sessionID, nil)
			result.TimedOut = true
			return result, nil
		case <-wakeCh:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func filterLocalAgentMailboxWaitEvents(events []runtimeevents.Event) []runtimeevents.Event {
	filtered := make([]runtimeevents.Event, 0, len(events))
	for _, event := range events {
		if !isLocalAgentMailboxWaitEvent(event) {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

func (r *localActorRegistry) listLocalAgentMailboxEvents(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]runtimeevents.Event, bool, bool, error) {
	if r == nil || r.Host == nil || r.Host.EventStore == nil {
		return nil, false, false, nil
	}
	messages, ok, hasMailboxRows, err := runtimechat.ListMailboxAgentControlFirst(ctx, r.Host.EventStore, sessionID, afterSeq, limit)
	if err != nil {
		return nil, ok, false, err
	}
	if !ok {
		return nil, false, false, nil
	}
	return localAgentMailboxMessagesToEvents(sessionID, messages), true, hasMailboxRows, nil
}

func localAgentMailboxMessagesToEvents(sessionID string, messages []team.MailMessage) []runtimeevents.Event {
	events := make([]runtimeevents.Event, 0, len(messages))
	for _, message := range messages {
		event := runtimechat.NewMailboxReceivedEvent(sessionID, message)
		if event.Payload == nil {
			event.Payload = map[string]interface{}{}
		}
		presentationSeq := message.Seq
		if message.SessionMailboxSeq > 0 {
			presentationSeq = message.SessionMailboxSeq
		}
		event.Payload["seq"] = presentationSeq
		if message.ControlSeq > 0 {
			event.Payload["control_seq"] = message.ControlSeq
		}
		if message.SessionMailboxSeq > 0 {
			event.Payload["session_mailbox_seq"] = message.SessionMailboxSeq
			event.Payload["mailbox_seq"] = message.SessionMailboxSeq
		} else {
			event.Payload["mailbox_seq"] = message.Seq
		}
		events = append(events, event)
	}
	return events
}

func (r *localActorRegistry) subscribeLocalAgentMailboxEvents(ctx context.Context, sessionID string) (<-chan struct{}, func()) {
	if r == nil || r.Host == nil {
		return nil, func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, func() {}
	}
	wakeCh := make(chan struct{}, 1)
	wake := func() {
		select {
		case wakeCh <- struct{}{}:
		default:
		}
	}
	unsubscribes := make([]func(), 0, 2)
	if mailCh, unwatch, ok := runtimechat.WatchMailboxAgentControlFirst(ctx, r.Host.EventStore, sessionID); ok {
		unsubscribes = append(unsubscribes, unwatch)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case message, open := <-mailCh:
					if !open {
						return
					}
					if strings.TrimSpace(message.ToAgent) != "" || message.Seq > 0 {
						wake()
					}
				}
			}
		}()
	}
	if watcher, ok := r.Host.EventStore.(runtimechat.EventWatcherStore); ok && watcher != nil {
		eventCh, unwatch := watcher.WatchEvents(ctx, sessionID)
		unsubscribes = append(unsubscribes, unwatch)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case event := <-eventCh:
					if strings.EqualFold(strings.TrimSpace(event.SessionID), sessionID) && isLocalAgentMailboxWaitEvent(event) {
						wake()
					}
				}
			}
		}()
	}
	if len(unsubscribes) > 0 {
		return wakeCh, func() {
			for _, unsubscribe := range unsubscribes {
				if unsubscribe != nil {
					unsubscribe()
				}
			}
		}
	}
	return r.subscribeLocalAgentReadEvents(ctx, sessionID)
}

func (r *localActorRegistry) subscribeLocalAgentReadEvents(ctx context.Context, sessionID string) (<-chan struct{}, func()) {
	if r == nil || r.Host == nil {
		return nil, func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, func() {}
	}
	wakeCh := make(chan struct{}, 1)
	wake := func() {
		select {
		case wakeCh <- struct{}{}:
		default:
		}
	}
	var unsubscribeStore func()
	if watcher, ok := r.Host.EventStore.(runtimechat.EventWatcherStore); ok && watcher != nil {
		eventCh, unwatch := watcher.WatchEvents(ctx, sessionID)
		unsubscribeStore = unwatch
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case event := <-eventCh:
					if strings.EqualFold(strings.TrimSpace(event.SessionID), sessionID) {
						wake()
					}
				}
			}
		}()
	}
	if r.Host.EventBus == nil {
		return wakeCh, func() {
			if unsubscribeStore != nil {
				unsubscribeStore()
			}
		}
	}
	handler := func(event runtimeevents.Event) {
		if !strings.EqualFold(strings.TrimSpace(event.SessionID), sessionID) {
			return
		}
		wake()
	}
	unsubscribeBus := r.Host.EventBus.SubscribeCancelable("", handler)
	return wakeCh, func() {
		if unsubscribeStore != nil {
			unsubscribeStore()
		}
		if unsubscribeBus != nil {
			unsubscribeBus()
		}
	}
}

func (r *localActorRegistry) Close(ctx context.Context, sessionID string) (*toolbroker.AgentStatusResult, error) {
	target := strings.TrimSpace(sessionID)
	if target == "" {
		return nil, fmt.Errorf("id is required")
	}
	targetSessionID, closeIDs, err := r.resolveLocalAgentCloseTargets(ctx, target)
	if err != nil {
		return nil, err
	}
	if len(closeIDs) == 0 {
		closeIDs = []string{target}
		targetSessionID = target
	}
	closedIDs := make([]string, 0, len(closeIDs))
	for _, closeID := range closeIDs {
		closeID = strings.TrimSpace(closeID)
		if closeID == "" {
			continue
		}
		if r != nil && r.Host != nil && r.Host.SessionHub != nil {
			r.Host.SessionHub.Stop(closeID)
		}
		if r != nil && r.Host != nil && r.Host.SessionStore != nil {
			if session, loadErr := r.Host.SessionStore.Load(ctx, closeID); loadErr == nil && session != nil {
				session.UpdateState(runtimechat.StateClosed)
				_ = r.Host.SessionStore.Update(ctx, session)
			}
		}
		closedIDs = append(closedIDs, closeID)
	}
	result, err := r.agentSnapshot(ctx, targetSessionID)
	if err != nil {
		return nil, err
	}
	result.Status = string(runtimechat.SessionStopped)
	result.ClosedCount = len(closedIDs)
	result.ClosedSessionIDs = closedIDs
	if store := r.localAgentRegistryStore(); store != nil {
		rootSessionID, targetPath, err := r.closeTargetLocalRegistryRootAndPath(ctx, target, targetSessionID)
		if err != nil {
			return nil, err
		}
		if rootSessionID != "" && targetPath != "" {
			if _, err := store.CloseAgentControlAgentSubtree(ctx, rootSessionID, targetPath, time.Now().UTC()); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func (r *localActorRegistry) resolveLocalAgentCloseTargets(ctx context.Context, target string) (string, []string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", nil, fmt.Errorf("id is required")
	}
	if targetSessionID, closeIDs, ok, err := r.resolveLocalAgentCloseTargetsFromRegistry(ctx, target); err != nil || ok {
		return targetSessionID, closeIDs, err
	}
	sessions, err := r.listLocalAgentSessions(ctx)
	if err != nil {
		return "", nil, err
	}
	for _, session := range sessions {
		changed, applyErr := r.applyTeamTeammateAgentContext(ctx, session)
		if applyErr != nil {
			return "", nil, applyErr
		}
		if changed {
			if err := r.Host.SessionStore.Update(ctx, session); err != nil {
				return "", nil, err
			}
		}
	}
	byID := make(map[string]*runtimechat.Session, len(sessions))
	for _, session := range sessions {
		if session != nil && strings.TrimSpace(session.ID) != "" {
			byID[strings.TrimSpace(session.ID)] = session
		}
	}
	targetSessionID := target
	targetPath := ""
	for _, session := range sessions {
		if session == nil {
			continue
		}
		sessionID := strings.TrimSpace(session.ID)
		path := localAgentSessionPath(session)
		if strings.EqualFold(target, path) {
			targetSessionID = sessionID
			targetPath = path
			break
		}
	}
	targetSession := byID[targetSessionID]
	if targetPath == "" && targetSession != nil {
		targetPath = localAgentSessionPath(targetSession)
	}
	if targetSession == nil && !strings.HasPrefix(target, "/") {
		return target, []string{target}, nil
	}
	closeIDs := make([]string, 0, len(sessions))
	seen := make(map[string]struct{}, len(sessions))
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		closeIDs = append(closeIDs, value)
	}
	add(targetSessionID)
	for _, session := range sessions {
		if session == nil || !isLocalAgentSession(session) {
			continue
		}
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" || strings.EqualFold(sessionID, targetSessionID) {
			continue
		}
		if targetPath != "" && strings.HasPrefix(localAgentSessionPath(session), strings.TrimRight(targetPath, "/")+"/") {
			add(sessionID)
			continue
		}
		if targetSessionID != "" && localAgentHasAncestor(session, targetSessionID, byID) {
			add(sessionID)
		}
	}
	sort.SliceStable(closeIDs, func(i, j int) bool {
		if strings.EqualFold(closeIDs[i], targetSessionID) {
			return true
		}
		if strings.EqualFold(closeIDs[j], targetSessionID) {
			return false
		}
		return closeIDs[i] < closeIDs[j]
	})
	return targetSessionID, closeIDs, nil
}

func (r *localActorRegistry) resolveLocalAgentCloseTargetsFromRegistry(ctx context.Context, target string) (string, []string, bool, error) {
	if r == nil || r.Host == nil {
		return "", nil, false, nil
	}
	if err := r.materializeLocalAgentRegistry(ctx); err != nil {
		return "", nil, false, err
	}
	record, ok, err := r.resolveLocalAgentRecord(ctx, target, true)
	if err != nil || !ok {
		return "", nil, ok, err
	}
	store := r.localAgentRegistryStore()
	if store == nil {
		return "", nil, false, nil
	}
	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: record.RootSessionID,
		PathPrefix:    record.AgentPath,
		IncludeClosed: true,
	})
	if err != nil {
		return "", nil, true, err
	}
	if len(records) == 0 {
		records = []agentcontrol.AgentRecord{record}
	}
	sort.SliceStable(records, func(i, j int) bool {
		if strings.EqualFold(records[i].AgentPath, record.AgentPath) {
			return true
		}
		if strings.EqualFold(records[j].AgentPath, record.AgentPath) {
			return false
		}
		return records[i].AgentPath < records[j].AgentPath
	})
	closeIDs := make([]string, 0, len(records))
	seen := map[string]struct{}{}
	targetPath := strings.TrimRight(strings.TrimSpace(record.AgentPath), "/")
	for _, item := range records {
		itemPath := strings.TrimSpace(item.AgentPath)
		if targetPath != "" && itemPath != targetPath && !strings.HasPrefix(itemPath, targetPath+"/") {
			continue
		}
		sessionID := strings.TrimSpace(item.SessionID)
		if sessionID == "" {
			continue
		}
		key := strings.ToLower(sessionID)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		closeIDs = append(closeIDs, sessionID)
	}
	targetSessionID := firstNonEmptyChatValue(record.SessionID, record.AgentID, target)
	if len(closeIDs) == 0 && targetSessionID != "" {
		closeIDs = append(closeIDs, targetSessionID)
	}
	return targetSessionID, closeIDs, true, nil
}

func (r *localActorRegistry) closeTargetLocalRegistryRootAndPath(ctx context.Context, target, targetSessionID string) (string, string, error) {
	record, ok, err := r.resolveLocalAgentRecord(ctx, firstNonEmptyChatValue(target, targetSessionID), true)
	if err != nil || !ok {
		return "", "", err
	}
	return record.RootSessionID, record.AgentPath, nil
}

func (r *localActorRegistry) resolveLocalAgentRecord(ctx context.Context, target string, includeClosed bool) (agentcontrol.AgentRecord, bool, error) {
	store := r.localAgentRegistryStore()
	if store == nil {
		return agentcontrol.AgentRecord{}, false, nil
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return agentcontrol.AgentRecord{}, false, nil
	}
	filter := agentcontrol.AgentFilter{IncludeClosed: includeClosed, Limit: 1}
	if strings.HasPrefix(target, "/") {
		filter.AgentPath = target
	} else {
		filter.SessionID = target
	}
	records, err := store.ListAgentControlAgents(ctx, filter)
	if err != nil {
		return agentcontrol.AgentRecord{}, false, err
	}
	if len(records) == 0 && !strings.HasPrefix(target, "/") {
		records, err = store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
			AgentID:       target,
			IncludeClosed: includeClosed,
			Limit:         1,
		})
		if err != nil {
			return agentcontrol.AgentRecord{}, false, err
		}
	}
	if len(records) == 0 {
		return agentcontrol.AgentRecord{}, false, nil
	}
	return records[0].Normalize(), true, nil
}

func (r *localActorRegistry) resolveLocalAgentTargetSessionID(ctx context.Context, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return target, nil
	}
	if r.localAgentRegistryStore() != nil {
		if err := r.materializeLocalAgentRegistry(ctx); err != nil {
			return "", err
		}
		if record, ok, err := r.resolveLocalAgentRecord(ctx, target, false); err != nil || ok {
			if err != nil {
				return "", err
			}
			if sessionID := strings.TrimSpace(record.SessionID); sessionID != "" {
				return sessionID, nil
			}
		}
	}
	if !strings.HasPrefix(target, "/") {
		return target, nil
	}
	sessions, err := r.listLocalAgentSessions(ctx)
	if err != nil {
		return "", err
	}
	for _, session := range sessions {
		changed, applyErr := r.applyTeamTeammateAgentContext(ctx, session)
		if applyErr != nil {
			return "", applyErr
		}
		if changed {
			if err := r.Host.SessionStore.Update(ctx, session); err != nil {
				return "", err
			}
		}
	}
	for _, session := range sessions {
		if session == nil {
			continue
		}
		if strings.EqualFold(target, localAgentSessionPath(session)) {
			if sessionID := strings.TrimSpace(session.ID); sessionID != "" {
				return sessionID, nil
			}
		}
	}
	return "", fmt.Errorf("unknown agent path: %s", target)
}

func (r *localActorRegistry) Resume(ctx context.Context, sessionID string) (*toolbroker.AgentStatusResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	resolvedSessionID, err := r.resolveLocalAgentTargetSessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sessionID = resolvedSessionID
	if err := r.ensureSession(ctx, sessionID); err != nil {
		return nil, err
	}
	if _, err := r.Host.SessionHub.GetOrCreate(sessionID); err != nil {
		return nil, err
	}
	return r.agentSnapshot(ctx, sessionID)
}

func (r *localActorRegistry) agentSnapshot(ctx context.Context, sessionID string) (*toolbroker.AgentStatusResult, error) {
	result := &toolbroker.AgentStatusResult{
		ID:        sessionID,
		SessionID: sessionID,
		Status:    "missing",
	}
	if r == nil || r.Host == nil || r.Host.SessionStore == nil {
		return result, nil
	}
	session, err := r.Host.SessionStore.Load(ctx, sessionID)
	if err != nil {
		if err == runtimechat.ErrSessionNotFound {
			return result, nil
		}
		return nil, err
	}
	if session != nil {
		result.Exists = true
		result.MessageCount = len(session.GetMessages())
		result.SessionState = string(session.State)
		if value, ok := session.GetContext(toolbroker.AgentSessionContextParentSessionID); ok {
			if text, ok := value.(string); ok {
				result.ParentSessionID = strings.TrimSpace(text)
			}
		}
		result.Path = localAgentSessionPath(session)
		result.Depth = localAgentSessionDepth(session)
		if value, ok := session.GetContext(toolbroker.AgentSessionContextAgentType); ok {
			if text, ok := value.(string); ok {
				result.AgentType = strings.TrimSpace(text)
			}
		}
		if err := r.enrichAgentTeamProjection(ctx, session, result); err != nil {
			return nil, err
		}
		messages := session.GetMessages()
		for i := len(messages) - 1; i >= 0; i-- {
			if result.LastMessageRole == "" {
				result.LastMessageRole = strings.TrimSpace(messages[i].Role)
				result.LastMessagePreview = truncateLocalAgentStatusPreview(messages[i].Content)
			}
			if messages[i].Role == "assistant" {
				result.Output = strings.TrimSpace(messages[i].Content)
				break
			}
		}
		result.Status = string(runtimechat.SessionIdle)
	}
	if r.Host.SessionHub != nil {
		if actor, ok := r.Host.SessionHub.Get(sessionID); ok && actor != nil {
			state := actor.State()
			if state != nil {
				result.Status = string(state.Status)
				result.PendingApproval = state.PendingApproval != nil
				result.PendingQuestion = state.PendingQuestion != nil
				result.CurrentTurnID = strings.TrimSpace(state.CurrentTurnID)
				if state.PendingTool != nil {
					result.PendingToolName = strings.TrimSpace(state.PendingTool.ToolName)
					result.PendingToolCallID = strings.TrimSpace(state.PendingTool.ToolCallID)
				}
			}
		}
	}
	return result, nil
}

func (r *localActorRegistry) enrichAgentTeamProjection(ctx context.Context, session *runtimechat.Session, result *toolbroker.AgentStatusResult) error {
	if session == nil || result == nil {
		return nil
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextTeamID); ok {
		if text, ok := value.(string); ok {
			result.TeamID = strings.TrimSpace(text)
		}
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextTeammateID); ok {
		if text, ok := value.(string); ok {
			result.TeammateID = strings.TrimSpace(text)
		}
	}
	if r == nil || r.Host == nil || r.Host.TeamStore == nil {
		return nil
	}
	if result.TeamID == "" || result.TeammateID == "" {
		record, teammate, err := team.FindTeammateBySession(ctx, r.Host.TeamStore, session.ID)
		if err != nil {
			return err
		}
		if record != nil {
			result.TeamID = strings.TrimSpace(record.ID)
		}
		if teammate != nil {
			result.TeammateID = strings.TrimSpace(teammate.ID)
		}
	}
	task, err := team.ActiveAgentControlTaskRecordForAssignee(ctx, r.Host.TeamStore, result.TeamID, result.TeammateID)
	if err != nil {
		return err
	}
	if task != nil {
		result.CurrentTaskID = strings.TrimSpace(task.ID)
		result.CurrentTaskStatus = strings.TrimSpace(task.Status)
	}
	return nil
}

func normalizeLocalAgentWaitIDs(args toolbroker.WaitAgentArgs) []string {
	seen := make(map[string]struct{})
	ordered := make([]string, 0, 1+len(args.IDs)+len(args.SessionIDs))
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		ordered = append(ordered, value)
	}
	add(args.ID)
	add(args.SessionID)
	for _, value := range args.IDs {
		add(value)
	}
	for _, value := range args.SessionIDs {
		add(value)
	}
	return ordered
}

func (r *localActorRegistry) enforceLocalAgentSpawnLimits(ctx context.Context, parentSession *runtimechat.Session, parentSessionID string, childDepth int) error {
	limits := r.localAgentsConfig()
	if limits.MaxDepth > 0 && childDepth > limits.MaxDepth {
		return fmt.Errorf("agent spawn depth limit reached: max_depth=%d requested_depth=%d", limits.MaxDepth, childDepth)
	}
	if limits.MaxThreads <= 0 {
		return nil
	}
	rootSessionID := localAgentRootSessionID(parentSession, parentSessionID)
	if store := r.localAgentRegistryStore(); store != nil {
		if err := r.materializeLocalAgentRegistry(ctx); err != nil {
			return err
		}
		records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
			RootSessionID: rootSessionID,
		})
		if err != nil {
			return err
		}
		count := 0
		for _, record := range records {
			if record.AgentPath == "/root" || strings.EqualFold(record.AgentType, agentcontrol.AgentTypeRoot) {
				continue
			}
			count++
		}
		if count >= limits.MaxThreads {
			return fmt.Errorf("agent spawn thread limit reached: max_threads=%d active_children=%d", limits.MaxThreads, count)
		}
		return nil
	}
	count, err := r.countLocalAgentTree(ctx, rootSessionID)
	if err != nil {
		return err
	}
	if count >= limits.MaxThreads {
		return fmt.Errorf("agent spawn thread limit reached: max_threads=%d active_children=%d", limits.MaxThreads, count)
	}
	return nil
}

func (r *localActorRegistry) localAgentsConfig() runtimecfg.AgentsConfig {
	defaults := runtimecfg.DefaultRuntimeConfig().Agents
	if r == nil || r.Host == nil {
		return defaults
	}
	runtimeConfig := r.Host.RuntimeConfig
	if runtimeConfig == nil && r.Host.Bootstrap != nil {
		runtimeConfig = r.Host.Bootstrap.Config()
	}
	if runtimeConfig == nil {
		return defaults
	}
	cfg := runtimeConfig.Agents
	if cfg.MaxThreads == 0 && cfg.MaxDepth == 0 && cfg.DefaultWaitTimeoutMs == 0 && strings.TrimSpace(cfg.DefaultForkTurns) == "" {
		return defaults
	}
	return cfg
}

func (r *localActorRegistry) countLocalAgentTree(ctx context.Context, rootSessionID string) (int, error) {
	sessions, err := r.listLocalAgentSessions(ctx)
	if err != nil {
		return 0, err
	}
	byID := make(map[string]*runtimechat.Session, len(sessions))
	for _, session := range sessions {
		if session != nil && strings.TrimSpace(session.ID) != "" {
			byID[strings.TrimSpace(session.ID)] = session
		}
	}
	count := 0
	for _, session := range sessions {
		if session == nil || !isLocalAgentSession(session) || isClosedLocalAgentSession(session) {
			continue
		}
		if rootSessionID == "" || localAgentRootSessionID(session, "") == rootSessionID || localAgentHasAncestor(session, rootSessionID, byID) {
			count++
		}
	}
	return count, nil
}

func (r *localActorRegistry) listLocalAgentSessions(ctx context.Context) ([]*runtimechat.Session, error) {
	if r == nil || r.Host == nil || r.Host.SessionStore == nil {
		return nil, nil
	}
	userID := strings.TrimSpace(r.Host.SessionUser)
	if userID == "" && r.Host.BaseSession != nil {
		userID = strings.TrimSpace(r.Host.BaseSession.SessionUserID)
	}
	if userID == "" {
		userID = "agent"
	}
	return r.Host.SessionStore.List(ctx, userID)
}

func localAgentChildDepth(parent *runtimechat.Session) int {
	return agentcontrol.ChildDepth(parent)
}

func localAgentSessionDepth(session *runtimechat.Session) int {
	return agentcontrol.SessionDepth(session)
}

func localAgentRootSessionID(session *runtimechat.Session, fallback string) string {
	sessionID := ""
	if session != nil {
		sessionID = session.ID
	}
	return agentcontrol.RootSessionID(session, sessionID, fallback)
}

func localAgentChildPath(parent *runtimechat.Session, sessionID string) string {
	parentID := ""
	if parent != nil {
		parentID = parent.ID
	}
	return agentcontrol.ChildPath(parent, parentID, sessionID, parent != nil && isLocalAgentSession(parent))
}

func localAgentSessionPath(session *runtimechat.Session) string {
	sessionID := ""
	if session != nil {
		sessionID = session.ID
	}
	return agentcontrol.SessionPath(session, sessionID, isLocalAgentSession(session))
}

func (r *localActorRegistry) applyTeamTeammateAgentContext(ctx context.Context, session *runtimechat.Session) (bool, error) {
	if r == nil || r.Host == nil || r.Host.TeamStore == nil || session == nil {
		return false, nil
	}
	sessionID := strings.TrimSpace(session.ID)
	if sessionID == "" {
		return false, nil
	}
	teams, err := r.Host.TeamStore.ListTeams(ctx, team.TeamFilter{})
	if err != nil {
		return false, err
	}
	for _, record := range teams {
		teamID := strings.TrimSpace(record.ID)
		if teamID == "" {
			continue
		}
		teammates, err := r.Host.TeamStore.ListTeammates(ctx, teamID)
		if err != nil {
			return false, err
		}
		for _, mate := range teammates {
			if !strings.EqualFold(strings.TrimSpace(mate.SessionID), sessionID) {
				continue
			}
			leadSessionID := strings.TrimSpace(record.LeadSessionID)
			if leadSessionID == "" {
				leadSessionID = strings.TrimSpace(r.Host.baseRuntimeSessionID())
			}
			changed := false
			if leadSessionID != "" {
				changed = setLocalAgentSessionContextIfChanged(session, toolbroker.AgentSessionContextParentSessionID, leadSessionID) || changed
				changed = setLocalAgentSessionContextIfChanged(session, toolbroker.AgentSessionContextRootSessionID, leadSessionID) || changed
			}
			changed = setLocalAgentSessionContextIfChanged(session, toolbroker.AgentSessionContextTeamID, teamID) || changed
			changed = setLocalAgentSessionContextIfChanged(session, toolbroker.AgentSessionContextTeammateID, mate.ID) || changed
			path := agentcontrol.TeamTeammatePath(teamID, mate.ID, mate.Name, sessionID)
			changed = setLocalAgentSessionContextIfChanged(session, toolbroker.AgentSessionContextPath, path) || changed
			changed = setLocalAgentSessionContextIfChanged(session, toolbroker.AgentSessionContextDepth, 1) || changed
			if profile := strings.TrimSpace(mate.Profile); profile != "" {
				changed = setLocalAgentSessionContextIfChanged(session, toolbroker.AgentSessionContextAgentType, profile) || changed
			}
			if err := r.upsertTeamTeammateAgentRecord(ctx, record, mate, session, leadSessionID, path); err != nil {
				return false, err
			}
			return changed, nil
		}
	}
	return false, nil
}

func (r *localActorRegistry) upsertTeamTeammateAgentRecord(ctx context.Context, record team.Team, mate team.Teammate, session *runtimechat.Session, leadSessionID, path string) error {
	store := r.localAgentRegistryStore()
	if store == nil || session == nil {
		return nil
	}
	sessionID := strings.TrimSpace(session.ID)
	teamID := strings.TrimSpace(record.ID)
	rootSessionID := firstNonEmptyChatValue(strings.TrimSpace(leadSessionID), strings.TrimSpace(record.LeadSessionID))
	rootSessionIDIsSynthetic := false
	if rootSessionID == "" {
		rootSessionID = "team:" + teamID
		rootSessionIDIsSynthetic = true
	}
	if rootSessionID == "" {
		return nil
	}
	rootSessionBinding := rootSessionID
	if rootSessionIDIsSynthetic {
		rootSessionBinding = ""
	}
	if _, err := store.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:       localRootAgentID(rootSessionID),
		RootSessionID: rootSessionID,
		SessionID:     rootSessionBinding,
		AgentPath:     "/root",
		AgentType:     agentcontrol.AgentTypeRoot,
		Status:        agentcontrol.AgentStatusActive,
	}); err != nil {
		return err
	}
	agentID := "team:" + teamID + ":" + firstNonEmptyChatValue(strings.TrimSpace(mate.ID), sessionID)
	_, err := store.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:         agentID,
		RootSessionID:   rootSessionID,
		ParentAgentID:   localRootAgentID(rootSessionID),
		ParentSessionID: rootSessionID,
		SessionID:       sessionID,
		AgentPath:       path,
		Depth:           1,
		AgentType:       firstNonEmptyChatValue(strings.TrimSpace(mate.Profile), agentcontrol.AgentTypeTeamTeammate),
		Nickname:        strings.TrimSpace(mate.Name),
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		TeamID:          teamID,
		TeammateID:      strings.TrimSpace(mate.ID),
		Status:          agentcontrol.AgentStatusActive,
	})
	return err
}

func setLocalAgentSessionContextIfChanged(session *runtimechat.Session, key string, value interface{}) bool {
	return agentcontrol.SetContextIfChanged(session, key, value)
}

func sanitizeLocalAgentPathSegment(value string) string {
	return agentcontrol.SanitizePathSegment(value)
}

func isLocalAgentSession(session *runtimechat.Session) bool {
	if session == nil {
		return false
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextParentSessionID); ok {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			return true
		}
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextPath); ok {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}

func isClosedLocalAgentSession(session *runtimechat.Session) bool {
	if session == nil {
		return false
	}
	return session.State == runtimechat.StateClosed || session.State == runtimechat.StateArchived
}

func localAgentHasAncestor(session *runtimechat.Session, ancestorID string, byID map[string]*runtimechat.Session) bool {
	ancestorID = strings.TrimSpace(ancestorID)
	if session == nil || ancestorID == "" {
		return false
	}
	seen := map[string]struct{}{}
	current := session
	for current != nil {
		parentID := ""
		if value, ok := current.GetContext(toolbroker.AgentSessionContextParentSessionID); ok {
			if text, ok := value.(string); ok {
				parentID = strings.TrimSpace(text)
			}
		}
		if parentID == "" {
			return false
		}
		if strings.EqualFold(parentID, ancestorID) {
			return true
		}
		if _, ok := seen[parentID]; ok {
			return false
		}
		seen[parentID] = struct{}{}
		current = byID[parentID]
	}
	return false
}

func isLocalAgentWaitReady(status string) bool {
	switch strings.TrimSpace(status) {
	case string(runtimechat.SessionIdle), string(runtimechat.SessionWaitingApproval), string(runtimechat.SessionWaitingInput), string(runtimechat.SessionStopped), "missing":
		return true
	default:
		return false
	}
}

func isLocalAgentWaitWakeEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case runtimechat.EventSessionEnd,
		runtimechat.EventSessionInterrupted,
		runtimechat.EventAssistantMessage,
		runtimechat.EventApprovalRequested,
		runtimechat.EventQuestionAsked,
		runtimechat.EventMailboxReceived:
		return true
	default:
		return false
	}
}

func localAgentCompletionStatus(event runtimeevents.Event) string {
	if strings.TrimSpace(event.Type) == runtimechat.EventSessionInterrupted {
		return string(runtimechat.SessionStopped)
	}
	if event.Payload != nil {
		if success, ok := event.Payload["success"].(bool); ok && !success {
			return "failed"
		}
		if text, ok := event.Payload["status"].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return string(runtimechat.SessionIdle)
}

func copyLocalAgentCompletionPayload(target map[string]interface{}, payload map[string]interface{}) {
	if len(target) == 0 || len(payload) == 0 {
		return
	}
	for _, key := range []string{"success", "error", "duration", "steps", "trace_id", "turn_id"} {
		if value, ok := payload[key]; ok {
			target[key] = value
		}
	}
	if value, ok := payload["seq"]; ok {
		target["source_event_seq"] = value
	}
}

func localAgentActorBusy(state *runtimechat.RuntimeState) bool {
	if state == nil {
		return false
	}
	switch state.Status {
	case runtimechat.SessionRunning, runtimechat.SessionRewinding, runtimechat.SessionWaitingApproval, runtimechat.SessionWaitingInput:
		return true
	default:
		return false
	}
}

func truncateLocalAgentStatusPreview(content string) string {
	content = strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	if len(content) <= 160 {
		return content
	}
	return content[:157] + "..."
}

func buildLocalAgentEventsResult(sessionID string, events []runtimeevents.Event) *toolbroker.AgentEventsResult {
	result := &toolbroker.AgentEventsResult{
		SessionID: strings.TrimSpace(sessionID),
		Count:     len(events),
	}
	if len(events) == 0 {
		return result
	}
	items := make([]toolbroker.AgentEventItem, 0, len(events))
	var latestSeq int64
	for _, event := range events {
		item := toolbroker.AgentEventItem{
			Type:      event.Type,
			TraceID:   event.TraceID,
			SessionID: event.SessionID,
			ToolName:  event.ToolName,
			AgentName: event.AgentName,
			Timestamp: event.Timestamp,
			Payload:   cloneRuntimeEventPayload(event.Payload),
		}
		if seq := localAgentEventSeq(event); seq > 0 {
			item.Seq = seq
			if seq > latestSeq {
				latestSeq = seq
			}
		}
		items = append(items, item)
	}
	result.Events = items
	result.LatestSeq = latestSeq
	return result
}

func buildLocalAgentMailboxWaitResult(sessionID string, events []runtimeevents.Event) *toolbroker.AgentWaitResult {
	filtered := filterLocalAgentMailboxWaitEvents(events)
	if len(filtered) == 0 {
		return nil
	}
	agentEvents := buildLocalAgentEventsResult(sessionID, filtered)
	result := &toolbroker.AgentWaitResult{
		Events:    agentEvents.Events,
		LatestSeq: agentEvents.LatestSeq,
	}
	if len(agentEvents.Events) > 0 {
		event := agentEvents.Events[0]
		result.Event = &event
		result.MatchedSessionID = event.SessionID
		result.ReadyCount = 1
	}
	return result
}

func isLocalAgentMailboxWaitEvent(event runtimeevents.Event) bool {
	switch strings.TrimSpace(event.Type) {
	case runtimechat.EventMailboxReceived,
		"subagent.completed",
		"team.completed",
		"team.summary":
		return true
	default:
		return false
	}
}

func localAgentEventSeq(event runtimeevents.Event) int64 {
	if event.Payload == nil {
		return 0
	}
	switch value := event.Payload["seq"].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}

func cloneRuntimeEventPayload(payload map[string]interface{}) map[string]interface{} {
	if len(payload) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

func (r *localActorRegistry) ensureSession(ctx context.Context, sessionID string) error {
	if r == nil || r.Host == nil || r.Host.SessionStore == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	existing, err := r.Host.SessionStore.Load(ctx, sessionID)
	if err == nil && existing != nil {
		if changed, applyErr := r.applyTeamTeammateAgentContext(ctx, existing); applyErr != nil {
			return applyErr
		} else if changed {
			return r.Host.SessionStore.Update(ctx, existing)
		}
		return nil
	}
	if err != nil && err != runtimechat.ErrSessionNotFound {
		return err
	}

	runtimeSession := runtimechat.NewSession(strings.TrimSpace(r.Host.SessionUser))
	runtimeSession.ID = sessionID
	if r.Host.BaseSession != nil {
		if prompt := strings.TrimSpace(r.Host.BaseSession.SystemPromptText); prompt != "" {
			runtimeSession.AddMessage(*runtimetypes.NewSystemMessage(prompt))
		}
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.ProviderName, strings.TrimSpace(r.Host.BaseSession.ProviderName), chatRuntimeContextProviderName)
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.ProviderProtocol, strings.TrimSpace(r.Host.BaseSession.Provider.GetProtocol()), chatRuntimeContextProtocol)
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.Model, strings.TrimSpace(r.Host.BaseSession.Model), chatRuntimeContextModel)
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.ReasoningEffort, strings.TrimSpace(r.Host.BaseSession.ReasoningEffort), chatRuntimeContextReasoningEffort)
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.Stream, r.Host.BaseSession.Stream, chatRuntimeContextStream)
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.DisableTools, r.Host.BaseSession.DisableTools, chatRuntimeContextDisableTools)
	}
	if _, err := r.applyTeamTeammateAgentContext(ctx, runtimeSession); err != nil {
		return err
	}
	return r.Host.SessionStore.Save(ctx, runtimeSession)
}

func (r *localActorRegistry) resolveMailboxSessionTargets(ctx context.Context, message team.MailMessage) ([]string, error) {
	teamID := strings.TrimSpace(message.TeamID)
	if teamID == "" {
		return nil, nil
	}
	record, err := r.Host.TeamStore.GetTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	targets := make(map[string]struct{})
	addTarget := func(sessionID string) {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			return
		}
		targets[sessionID] = struct{}{}
	}

	switch toAgent := strings.TrimSpace(message.ToAgent); toAgent {
	case "", "*":
		if record != nil {
			addTarget(record.LeadSessionID)
		}
		teammates, err := r.Host.TeamStore.ListTeammates(ctx, teamID)
		if err != nil {
			return nil, err
		}
		for _, mate := range teammates {
			addTarget(mate.SessionID)
		}
	case "lead":
		if record != nil {
			addTarget(record.LeadSessionID)
		}
	default:
		mate, err := r.Host.TeamStore.GetTeammate(ctx, toAgent)
		if err != nil {
			return nil, err
		}
		if mate != nil && strings.TrimSpace(mate.TeamID) == teamID {
			addTarget(mate.SessionID)
		}
	}

	resolved := make([]string, 0, len(targets))
	for sessionID := range targets {
		resolved = append(resolved, sessionID)
	}
	sort.Strings(resolved)
	return resolved, nil
}

func ensureTeammateSessionIDs(teamID string, specs []toolbroker.SpawnTeammateSpec) []toolbroker.SpawnTeammateSpec {
	if len(specs) == 0 {
		return nil
	}
	resolved := make([]toolbroker.SpawnTeammateSpec, len(specs))
	copy(resolved, specs)
	seen := make(map[string]int, len(specs))
	for i := range resolved {
		if strings.EqualFold(strings.TrimSpace(resolved[i].SessionID), "current") {
			resolved[i].SessionID = ""
		}
		if strings.TrimSpace(resolved[i].SessionID) != "" {
			continue
		}
		base := normalizeLocalSessionSegment(firstNonEmptyChatValue(resolved[i].ID, resolved[i].Name))
		if base == "" {
			base = fmt.Sprintf("mate_%02d", i+1)
		}
		candidate := fmt.Sprintf("%s__%s", strings.TrimSpace(teamID), base)
		seen[candidate]++
		if seen[candidate] > 1 {
			candidate = fmt.Sprintf("%s_%d", candidate, seen[candidate])
		}
		resolved[i].SessionID = candidate
	}
	return resolved
}

var localSessionSegmentPattern = regexp.MustCompile(`[^a-z0-9]+`)

func normalizeLocalSessionSegment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	value = localSessionSegmentPattern.ReplaceAllString(value, "_")
	return strings.Trim(value, "_")
}
