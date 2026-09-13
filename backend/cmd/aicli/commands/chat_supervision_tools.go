package commands

import (
	"context"
	"fmt"
	"strings"

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
}

// newLocalSupervisionToolController returns nil when the host has no durable
// supervision store, which keeps the tools out of Definitions() entirely.
func newLocalSupervisionToolController(host *localChatRuntimeHost, session *ChatSession) *localSupervisionToolController {
	if host == nil || host.Supervision == nil || host.Supervision.Store == nil {
		return nil
	}
	return &localSupervisionToolController{
		host:    host,
		session: session,
		service: supervision.NewLocalControlService(host.Supervision.Store, host.Supervision.Actions),
	}
}

// resolution mirrors the preflight rule exactly: a team lead reads/writes the
// team scope (rows addressed at its own session), every other session reads its
// own root scope.
func (c *localSupervisionToolController) resolution(ctx context.Context, parentSessionID string) (string, string) {
	sessionID := strings.TrimSpace(parentSessionID)
	if sessionID == "" && c.session != nil {
		sessionID = strings.TrimSpace(currentRuntimeSessionID(c.session))
	}
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
	})
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
