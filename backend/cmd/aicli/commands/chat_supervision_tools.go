package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// P2-12 方案 3：把 supervision_snapshot / ack_lifecycle / control_descendant
// 接到 CLI 宿主的 durable 控制面。
//
// scope 口径与注入式 preflight（chat_actor_host.go:2145-2161）完全一致：默认
// 以父会话为 root scope；当该会话是活动团队的 lead 时改用团队 scope。模型无法
// 指定 root scope，跨 scope 通知一律拒绝。写动作的 scope 校验、allowed_actions
// 复核与 expected_version CAS 复用 internal/supervision.LocalControlService，
// 与 /debug supervision 是同一实现。
type localSupervisionToolController struct {
	host    *localChatRuntimeHost
	session *ChatSession
	service *supervision.LocalControlService
	// results is the P0-4 read channel (batch TaskResult → mailbox completion
	// payload). The provider is decorated per call so
	// supervision_descendants(include_results=true) behaves exactly like the
	// API host; the CLI plane deliberately does not receive the host batch
	// store through hooks, so the decoration happens here.
	results supervision.ResultSource
}

// newLocalSupervisionToolController returns nil when the host has no durable
// supervision store, which keeps the tools out of Definitions() entirely.
func newLocalSupervisionToolController(host *localChatRuntimeHost, session *ChatSession) *localSupervisionToolController {
	if host == nil || host.Supervision == nil || host.Supervision.Store == nil {
		return nil
	}
	controller := &localSupervisionToolController{
		host:    host,
		session: session,
		service: supervision.NewLocalControlService(host.Supervision.Store, host.Supervision.Actions),
	}
	controller.results = runtimeserver.NewSupervisionResultSource(host.SubagentBatches, localCompletionMailboxReader(host))
	return controller
}

// localCompletionMailboxReader returns the host's AgentControl session mailbox
// read channel — the same store the completion dispatcher appends to
// (chat_actor_execution_supervisor.go) — so the P0-4 completion-payload
// fallback reads exactly what the delivery path wrote.
func localCompletionMailboxReader(host *localChatRuntimeHost) runtimeserver.CompletionMailboxReader {
	if host == nil {
		return nil
	}
	if reader, ok := host.EventStore.(runtimeserver.CompletionMailboxReader); ok && reader != nil {
		return reader
	}
	if reader, ok := host.RuntimeStore.(runtimeserver.CompletionMailboxReader); ok && reader != nil {
		return reader
	}
	return nil
}

// resultProvider decorates the current plane provider with the host's result
// source. It is built per call so a host that swaps its provider (tests, or a
// future re-wire) keeps working.
func (c *localSupervisionToolController) resultProvider() supervision.DescendantProvider {
	if c == nil || c.host == nil || c.host.Supervision == nil {
		return nil
	}
	var runs supervision.ExecutionRunStore
	if store, ok := c.host.Supervision.Store.(supervision.ExecutionRunStore); ok {
		runs = store
	}
	return runtimeserver.NewDescendantResultProvider(c.host.Supervision.Provider, c.results, runs, c.host.AgentRegistryStore)
}

// resultSource is the read channel behind read_agent_result: the decorated
// provider when available (it also resolves agent paths), otherwise the raw
// batch/mailbox source.
func (c *localSupervisionToolController) resultSource() supervision.ResultSource {
	if provider := c.resultProvider(); provider != nil {
		if source, ok := provider.(supervision.ResultSource); ok {
			return source
		}
	}
	return c.results
}

// resolution mirrors the preflight rule exactly: a team lead reads/writes the
// team scope (rows addressed at its own session), every other session reads its
// own root scope.
func (c *localSupervisionToolController) resolution(ctx context.Context, parentSessionID string) (string, string) {
	sessionID := c.callerSessionID(parentSessionID)
	rootScopeID := sessionID
	targetTeamID := ""
	if c.session == nil || c.session.ActiveTeam == nil || c.host == nil || c.host.TeamStore == nil {
		return rootScopeID, targetTeamID
	}
	teamID := strings.TrimSpace(c.session.ActiveTeam.TeamID)
	if teamID == "" {
		return rootScopeID, targetTeamID
	}
	record, err := c.host.TeamStore.GetTeam(ctx, teamID)
	if err != nil || record == nil || strings.TrimSpace(record.LeadSessionID) != sessionID {
		return rootScopeID, targetTeamID
	}
	return teamID, teamID
}

// callerSessionID is the identity the tool entry acts as: the tool call's own
// parent session, falling back to the rendered session when the broker passes
// an empty id.
func (c *localSupervisionToolController) callerSessionID(parentSessionID string) string {
	sessionID := strings.TrimSpace(parentSessionID)
	if sessionID == "" && c.session != nil {
		sessionID = strings.TrimSpace(currentRuntimeSessionID(c.session))
	}
	return sessionID
}

func (c *localSupervisionToolController) SupervisionSnapshot(ctx context.Context, parentSessionID string, args toolbroker.SupervisionSnapshotArgs) (*supervision.Digest, error) {
	rootScopeID, targetTeamID := c.resolution(ctx, parentSessionID)
	if rootScopeID == "" {
		return nil, fmt.Errorf("supervision scope is required")
	}
	limit := args.Limit
	if limit <= 0 {
		limit = c.host.supervisionConfig.WithDefaults().DigestMaxItems
	}
	return c.service.Snapshot(ctx, supervision.LocalSnapshotRequest{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: strings.TrimSpace(parentSessionID),
		TargetParentTeamID:    targetTeamID,
		AfterSeq:              args.AfterSeq,
		IncludeResolved:       args.IncludeResolved,
		Limit:                 limit,
		SubjectPresence:       localSupervisionSubjectPresence(c.host),
		HostCapabilities:      localSupervisionHostCapabilities(c.host),
	})
}

// SupervisionDescendants returns the scoped descendant state matrix (doc 6.2),
// read-only. This is the business "巡查" primitive (P0-A): one call lists every
// child/descendant of the caller's own scope with execution_status,
// supervision_state, heartbeat/progress ages plus the remediation hints this
// host can actually execute, so a parent inspects a whole batch instead of
// polling wait_agent / list_agents row by row.
//
// Scope 口径与 SupervisionSnapshot 完全相同（模型不能指定 root scope）：
//   - 普通会话：投影子树与 durable root scope 都是自身会话；
//   - team lead：投影子树是自身会话 + 团队边，durable root scope 是团队，
//     与 preflight digest 的 RootScopeID / TargetParentTeamID 口径一致。
func (c *localSupervisionToolController) SupervisionDescendants(ctx context.Context, parentSessionID string, args toolbroker.SupervisionDescendantsArgs) (*supervision.Snapshot, error) {
	rootScopeID, targetTeamID := c.resolution(ctx, parentSessionID)
	if rootScopeID == "" {
		return nil, fmt.Errorf("supervision scope is required")
	}
	request := supervision.SnapshotRequest{
		Scope: supervision.Scope{
			RootSessionID: c.callerSessionID(parentSessionID),
			Mode:          strings.TrimSpace(args.Mode),
		},
		AfterSeq:         args.AfterSeq,
		Health:           strings.TrimSpace(args.Health),
		IncludeTerminal:  args.IncludeTerminal,
		IncludeResults:   args.IncludeResults,
		Limit:            args.Limit,
		DefaultLimit:     c.host.supervisionConfig.WithDefaults().SnapshotMaxItems,
		HostCapabilities: localSupervisionHostCapabilities(c.host),
	}
	if targetTeamID != "" {
		request.Scope.RootTeamID = targetTeamID
		// Durable rows stay rooted at the team while the projected subtree is
		// the lead's own session: the same split the digest uses.
		request.RootScopeID = targetTeamID
	}
	if c.host != nil && c.host.Supervision != nil {
		if provider := c.resultProvider(); provider != nil {
			request.Provider = provider
		} else {
			request.Provider = c.host.Supervision.Provider
		}
	}
	return supervision.BuildSnapshot(ctx, c.service.Store(), request)
}

// ReadAgentResult returns the bounded durable result of one child session or
// batch task inside the caller's own scope (P0-4 改动 2). Scope resolution is
// identical to SupervisionDescendants (a team lead reads the team scope); the
// model cannot name a root scope. Missing records are reported as
// no_result_recorded with an executable next_action, never as a tool failure.
func (c *localSupervisionToolController) ReadAgentResult(ctx context.Context, parentSessionID string, args toolbroker.ReadAgentResultArgs) (supervision.ReadResultPayload, error) {
	rootScopeID, targetTeamID := c.resolution(ctx, parentSessionID)
	if rootScopeID == "" {
		return supervision.ReadResultPayload{}, fmt.Errorf("supervision scope is required")
	}
	source := c.resultSource()
	if source == nil {
		return supervision.NoResultRecordedPayload(args.SessionID, args.TaskID), nil
	}
	scope := supervision.Scope{RootSessionID: c.callerSessionID(parentSessionID)}
	if targetTeamID != "" {
		scope.RootTeamID = targetTeamID
	}
	record, found, err := source.LoadAgentResult(ctx, scope, args.SessionID, args.TaskID)
	if err != nil {
		return supervision.ReadResultPayload{}, err
	}
	if !found {
		return supervision.NoResultRecordedPayload(args.SessionID, args.TaskID), nil
	}
	return supervision.BuildReadResultPayload(record, supervision.ReadResultArgs{
		SessionID: args.SessionID,
		TaskID:    args.TaskID,
		Sections:  args.Sections,
		MaxChars:  args.MaxChars,
	}), nil
}

func (c *localSupervisionToolController) AckLifecycle(ctx context.Context, parentSessionID string, args toolbroker.AckLifecycleArgs) (*supervision.Notification, error) {
	rootScopeID, _ := c.resolution(ctx, parentSessionID)
	if rootScopeID == "" {
		return nil, fmt.Errorf("supervision scope is required")
	}
	request := supervision.LifecycleDecisionRequest{
		NotificationID:     args.NotificationID,
		Scopes:             []string{rootScopeID},
		ExpectedVersion:    args.ExpectedVersion,
		HasExpectedVersion: args.HasExpectedVersion,
		Note:               args.Note,
		Reason:             args.Reason,
		Until:              args.Until,
	}
	switch args.Decision {
	case "acknowledge":
		return c.service.Acknowledge(ctx, request)
	case "defer":
		return c.service.Defer(ctx, request)
	case "resolve":
		request.Resolution = supervision.ResolutionState(args.Resolution)
		return c.service.Resolve(ctx, request)
	default:
		return nil, fmt.Errorf("unsupported decision %q (want acknowledge|defer|resolve)", args.Decision)
	}
}

func (c *localSupervisionToolController) ControlDescendant(ctx context.Context, parentSessionID string, args toolbroker.ControlDescendantArgs) (supervision.ActionRecord, error) {
	rootScopeID, _ := c.resolution(ctx, parentSessionID)
	if rootScopeID == "" {
		return supervision.ActionRecord{}, fmt.Errorf("supervision scope is required")
	}
	cascade := supervision.CascadeNone
	if strings.EqualFold(args.Cascade, string(supervision.CascadeDescendants)) {
		cascade = supervision.CascadeDescendants
	}
	return c.service.Control(ctx, supervision.ControlRequest{
		NotificationID:     args.NotificationID,
		Scopes:             []string{rootScopeID},
		RequestedByID:      strings.TrimSpace(parentSessionID),
		Action:             supervision.ActionKind(args.Action),
		Reason:             args.Reason,
		CascadeMode:        cascade,
		ExpectedVersion:    args.ExpectedVersion,
		HasExpectedVersion: args.HasExpectedVersion,
	})
}
