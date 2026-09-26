package runtimeapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// P0-A 方案 A / doc 6.2：把 unified 快照读模型接到 runtime-server（HTTP）宿主的
// 模型工具面。
//
// 在此之前，HTTP 宿主只有 /supervision/snapshot 这类宿主接口能看到 descendant
// 状态矩阵，模型在自己的回合内看不到任何子 agent 的业务状态，只能靠 wait_agent /
// list_agents 逐行轮询（R4）。本控制器把同一份 BuildSnapshot + DescendantProvider
// 组合成 supervision_descendants / supervision_snapshot / ack_lifecycle /
// control_descendant 四个工具的后端，scope 口径与 preflight 注入完全一致
// （supervision_handlers.go:277-302）：普通会话是自己的会话；团队 lead 的
// durable root scope 是团队，投影子树仍是 lead 自身会话。模型无法指定 root
// scope，越权通知一律拒绝（复用 LocalControlService）。
type handlerSupervisionToolController struct {
	handler *Handler
	service *supervision.LocalControlService
}

// newHandlerSupervisionToolController returns nil when the HTTP host has no
// durable store, which keeps the four tools out of Definitions() entirely —
// same capability gate as the CLI host.
func newHandlerSupervisionToolController(h *Handler) *handlerSupervisionToolController {
	if h == nil || h.getSupervisionStore() == nil {
		return nil
	}
	return &handlerSupervisionToolController{
		handler: h,
		service: supervision.NewLocalControlService(h.getSupervisionStore(), h.getSupervisionActionService()),
	}
}

// resolution mirrors the preflight rule: a team lead acts on the team scope,
// every other session on its own root scope. The model cannot name either.
func (c *handlerSupervisionToolController) resolution(ctx context.Context, parentSessionID string) (string, string) {
	sessionID := strings.TrimSpace(parentSessionID)
	if sessionID == "" {
		return "", ""
	}
	rootScopeID := sessionID
	targetTeamID := ""
	if teamID := c.leadTeamID(ctx, sessionID); teamID != "" {
		rootScopeID = teamID
		targetTeamID = teamID
	}
	return rootScopeID, targetTeamID
}

// leadTeamID returns the id of the team this session leads, preferring an
// active team. It is the HTTP counterpart of the CLI's ActiveTeam check: a
// child task's run meta also carries a team id, so only the registered lead may
// consume the team's supervision inbox.
func (c *handlerSupervisionToolController) leadTeamID(ctx context.Context, sessionID string) string {
	store := c.handler.getTeamStore()
	if store == nil {
		return ""
	}
	teams, err := store.ListTeams(ctx, team.TeamFilter{})
	if err != nil {
		return ""
	}
	fallback := ""
	for _, record := range teams {
		if strings.TrimSpace(record.LeadSessionID) != sessionID {
			continue
		}
		teamID := strings.TrimSpace(record.ID)
		if teamID == "" {
			continue
		}
		if record.Status == team.TeamStatusActive {
			return teamID
		}
		if fallback == "" {
			fallback = teamID
		}
	}
	return fallback
}

func (c *handlerSupervisionToolController) SupervisionSnapshot(ctx context.Context, parentSessionID string, args toolbroker.SupervisionSnapshotArgs) (*supervision.Digest, error) {
	rootScopeID, targetTeamID := c.resolution(ctx, parentSessionID)
	if rootScopeID == "" {
		return nil, fmt.Errorf("supervision scope is required")
	}
	limit := args.Limit
	if limit <= 0 {
		// Host-configured default (DigestMaxItems), the same knob the preflight
		// injection reads, so a tool-driven read never returns a larger digest
		// than the host would have injected on the next turn.
		limit = c.handler.supervisionTuning().DigestMaxItems
	}
	return c.service.Snapshot(ctx, supervision.LocalSnapshotRequest{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: strings.TrimSpace(parentSessionID),
		TargetParentTeamID:    targetTeamID,
		AfterSeq:              args.AfterSeq,
		IncludeResolved:       args.IncludeResolved,
		Limit:                 limit,
		SubjectPresence:       c.handler.supervisionSubjectPresence(),
		HostCapabilities:      c.handler.supervisionHostCapabilities(),
	})
}

// SupervisionDescendants returns the scoped descendant state matrix (doc 6.2),
// read-only. This is the business 巡查 primitive (P0-A): one call lists every
// child/descendant of the caller's own scope with execution_status,
// supervision_state, heartbeat/progress ages plus the remediation hints this
// host can actually execute, so a parent inspects a whole batch instead of
// polling wait_agent / list_agents row by row.
//
// A team lead keeps its own session as the projected subtree (agent rows stay
// visible) while durable rows stay rooted at the team — the same split the
// preflight digest expresses with RootScopeID + TargetParentTeamID.
func (c *handlerSupervisionToolController) SupervisionDescendants(ctx context.Context, parentSessionID string, args toolbroker.SupervisionDescendantsArgs) (*supervision.Snapshot, error) {
	rootScopeID, targetTeamID := c.resolution(ctx, parentSessionID)
	if rootScopeID == "" {
		return nil, fmt.Errorf("supervision scope is required")
	}
	request := supervision.SnapshotRequest{
		Scope: supervision.Scope{
			RootSessionID: strings.TrimSpace(parentSessionID),
			Mode:          strings.TrimSpace(args.Mode),
		},
		AfterSeq:         args.AfterSeq,
		Health:           strings.TrimSpace(args.Health),
		IncludeTerminal:  args.IncludeTerminal,
		IncludeResults:   args.IncludeResults,
		Limit:            args.Limit,
		DefaultLimit:     c.handler.supervisionTuning().SnapshotMaxItems,
		Provider:         c.handler.getSupervisionDescendantProvider(),
		HostCapabilities: c.handler.supervisionHostCapabilities(),
	}
	if targetTeamID != "" {
		request.Scope.RootTeamID = targetTeamID
		request.RootScopeID = targetTeamID
	}
	return supervision.BuildSnapshot(ctx, c.service.Store(), request)
}

func (c *handlerSupervisionToolController) AckLifecycle(ctx context.Context, parentSessionID string, args toolbroker.AckLifecycleArgs) (*supervision.Notification, error) {
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

func (c *handlerSupervisionToolController) ControlDescendant(ctx context.Context, parentSessionID string, args toolbroker.ControlDescendantArgs) (supervision.ActionRecord, error) {
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

// ReadAgentResult returns the bounded durable result of one child session or
// batch task inside the caller's own scope (P0-4 改动 2). Scope resolution is
// identical to SupervisionDescendants (a team lead reads the team scope); the
// model cannot name a root scope. The read channel is the descendant provider
// decorated by runtimeserver (batch TaskResult → mailbox completion payload);
// a host without a result-aware provider answers no_result_recorded with an
// executable next_action instead of failing the call.
func (c *handlerSupervisionToolController) ReadAgentResult(ctx context.Context, parentSessionID string, args toolbroker.ReadAgentResultArgs) (supervision.ReadResultPayload, error) {
	rootScopeID, targetTeamID := c.resolution(ctx, parentSessionID)
	if rootScopeID == "" {
		return supervision.ReadResultPayload{}, fmt.Errorf("supervision scope is required")
	}
	source, _ := c.handler.getSupervisionDescendantProvider().(supervision.ResultSource)
	if source == nil {
		return supervision.NoResultRecordedPayload(args.SessionID, args.TaskID), nil
	}
	scope := supervision.Scope{RootSessionID: strings.TrimSpace(parentSessionID)}
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
		Offset:    args.Offset,
		Limit:     args.Limit,
		MaxChars:  args.MaxChars,
	}), nil
}
