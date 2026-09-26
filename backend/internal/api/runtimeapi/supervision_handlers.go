package runtimeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// Supervision store wiring follows the team store pattern: the runtime server
// (or tests) calls SetSupervisionStore/... once configured; every handler
// degrades to 503 until then.

// SetSupervisionStore sets the durable supervision control-plane store.
func (h *Handler) SetSupervisionStore(store supervision.Store) {
	if h == nil {
		return
	}
	h.supervisionStoreMu.Lock()
	h.supervisionStore = store
	h.supervisionStoreMu.Unlock()
}

// SetSupervisionActionService sets the durable action service.
func (h *Handler) SetSupervisionActionService(service *supervision.ActionService) {
	if h == nil {
		return
	}
	h.supervisionStoreMu.Lock()
	h.supervisionActions = service
	h.supervisionStoreMu.Unlock()
}

// SetSupervisionConfig sets the control-plane tuning knobs (digest/snapshot
// budgets) this host applies when the caller does not pass an explicit limit.
// Zero values keep supervision defaults, so an unwired host behaves exactly
// like one configured with the package defaults (plan §9 待决项收口).
//
// P2-D：它同时是 opt-in 周期巡查的唯一启动点——ProgressCheckInterval > 0 时
// 拉起巡检循环，为 0（默认）时不注册 ticker、不新增 goroutine；重复调用先停后起，
// 因此改小/改大/关回 0 都会立刻生效（见 supervision_progress_check.go）。
func (h *Handler) SetSupervisionConfig(cfg supervision.Config) {
	if h == nil {
		return
	}
	h.supervisionStoreMu.Lock()
	h.supervisionConfig = cfg
	h.supervisionStoreMu.Unlock()
	h.syncSupervisionProgressCheck()
}

// supervisionTuning returns this host's supervision config with semantic
// defaults applied, so callers never branch on zero values themselves.
func (h *Handler) supervisionTuning() supervision.Config {
	if h == nil {
		return supervision.DefaultConfig()
	}
	h.supervisionStoreMu.RLock()
	cfg := h.supervisionConfig
	h.supervisionStoreMu.RUnlock()
	return cfg.WithDefaults()
}

// SetSupervisionWakeScheduler sets the wake scheduler.
func (h *Handler) SetSupervisionWakeScheduler(scheduler *supervision.WakeScheduler) {
	if h == nil {
		return
	}
	h.supervisionStoreMu.Lock()
	h.supervisionWakes = scheduler
	h.supervisionStoreMu.Unlock()
	if scheduler != nil {
		// G2：scheduler 可能晚于 batch store 到达（runtime-server 是先 store 后
		// scheduler），这里补一次接线，保证 resume 上下文有账本/进度投影。
		h.wireSupervisionSources()
		h.bindSupervisionTurnEndConsumer()
	}
}

// bindSupervisionTurnEndConsumer subscribes parent session turn-end events
// once so wakes accumulated while a parent was busy are drained as soon as
// the parent becomes idle again (doc 6.5 rule 2 closure). It reacts to any
// session turn end: for a root session it drains root wakes; for an
// intermediate parent it drains that parent's own child wakes.
//
// 2026-09-22 调整（docs/plan/supervision-manual-audit-plan-20260922.md）：该
// 自动核查默认关闭（supervision.turn_end_check，nil/false 等价）。开关在事件
// 回调里判定而不是订阅时判定，因为 SetSupervisionWakeScheduler 先于
// SetSupervisionConfig 调用（cmd/runtime-server/main.go），订阅时配置尚未到达。
// 关闭时 turn 结束不产生任何 drain/self-check；积压 wake 由下一次自然 turn 的
// preflight digest 被动注入，或经 POST /supervision/wake/drain 显式投递。
func (h *Handler) bindSupervisionTurnEndConsumer() {
	if h == nil {
		return
	}
	h.supervisionWakeOnce.Do(func() {
		bus := h.getRuntimeEventBus()
		if bus == nil {
			return
		}
		bus.SubscribeCancelable(chat.EventSessionEnd, func(event runtimeevents.Event) {
			if !h.supervisionTuning().TurnEndCheckEnabled() {
				return
			}
			sessionID := strings.TrimSpace(event.SessionID)
			if sessionID == "" {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if h.sessionManager == nil {
				return
			}
			session, err := h.sessionManager.Get(ctx, sessionID)
			if err != nil || session == nil {
				return
			}
			rootScopeID := apiAgentRootSessionID(session, sessionID)
			controller := &sessionAgentController{handler: h}
			err = controller.wakeSupervisedParent(ctx, rootScopeID, sessionID)
			if errors.Is(err, supervision.ErrWakeRateLimited) {
				// P1-6 方案 4: the class budget deferred the wake, so the
				// parent would go idle with an undelivered digest. Give it one
				// bounded digest-only turn (opt-in via
				// supervision.wake_self_check_per_window).
				_ = controller.selfCheckSupervisedParent(ctx, rootScopeID, sessionID)
			}
		})
	})
}

// SetSupervisionDescendantProvider sets the runtime descendant provider
// (agentcontrol + team store rollup) used by snapshot aggregation.
func (h *Handler) SetSupervisionDescendantProvider(provider supervision.DescendantProvider) {
	if h == nil {
		return
	}
	h.supervisionStoreMu.Lock()
	h.supervisionDescendantProvider = provider
	h.supervisionStoreMu.Unlock()
}

// CloseAgentSessionByID closes a child agent session through the runtime
// session controller. It backs the durable supervision action executor so
// cancel/close actions actually stop the live session (doc 6.6).
func (h *Handler) CloseAgentSessionByID(ctx context.Context, sessionID string) error {
	if h == nil {
		return fmt.Errorf("handler is nil")
	}
	controller := h.getAgentSessionController()
	if controller == nil {
		return fmt.Errorf("agent session controller is not configured")
	}
	_, err := controller.Close(ctx, sessionID)
	return err
}

func (h *Handler) getSupervisionStore() supervision.Store {
	if h == nil {
		return nil
	}
	h.supervisionStoreMu.RLock()
	store := h.supervisionStore
	h.supervisionStoreMu.RUnlock()
	return store
}

func (h *Handler) getSupervisionActionService() *supervision.ActionService {
	if h == nil {
		return nil
	}
	h.supervisionStoreMu.RLock()
	service := h.supervisionActions
	h.supervisionStoreMu.RUnlock()
	return service
}

func (h *Handler) getSupervisionWakeScheduler() *supervision.WakeScheduler {
	if h == nil {
		return nil
	}
	h.supervisionStoreMu.RLock()
	scheduler := h.supervisionWakes
	h.supervisionStoreMu.RUnlock()
	return scheduler
}

// supervisionWakeBudgetStates projects the auto-wake budget (plan P1-6) of the
// requested root scopes as one read-only row per scope × budget class, in the
// class order used by the CLI `/debug supervision list` view so both hosts
// render the same ledger. Returning nil (scheduler not wired, or no non-empty
// scope requested) keeps the field out of HTTP responses entirely: a host
// without the wake ledger must not advertise a misleading 0/limit budget.
func (h *Handler) supervisionWakeBudgetStates(ctx context.Context, scopes ...string) []supervision.WakeBudgetState {
	scheduler := h.getSupervisionWakeScheduler()
	if scheduler == nil {
		return nil
	}
	classes := []supervision.WakeBudgetClass{
		supervision.WakeBudgetClassApproval,
		supervision.WakeBudgetClassFailure,
		supervision.WakeBudgetClassOther,
		supervision.WakeBudgetClassProgress,
	}
	seen := make(map[string]bool, len(scopes))
	states := make([]supervision.WakeBudgetState, 0, len(scopes)*len(classes))
	for _, raw := range scopes {
		scope := strings.TrimSpace(raw)
		if scope == "" || seen[scope] {
			continue
		}
		seen[scope] = true
		for _, class := range classes {
			states = append(states, scheduler.BudgetState(ctx, scope, class))
		}
	}
	if len(states) == 0 {
		return nil
	}
	return states
}

func (h *Handler) getSupervisionDescendantProvider() supervision.DescendantProvider {
	if h == nil {
		return nil
	}
	h.supervisionStoreMu.RLock()
	provider := h.supervisionDescendantProvider
	h.supervisionStoreMu.RUnlock()
	return provider
}

func (h *Handler) supervisionUnavailable(w http.ResponseWriter) bool {
	if h.getSupervisionStore() == nil {
		h.writeError(w, http.StatusServiceUnavailable, runtimeerrors.New(runtimeerrors.ErrConfigInvalid, "supervision store not configured"))
		return true
	}
	return false
}

// supervisionSubjectPresence wires control-plane existence checks so that
// notifications whose subject rows disappeared (P2-12 stale notifications) stop
// counting as critical. Unverifiable subjects keep their severity, so partial
// wiring can never hide a real critical.
func (h *Handler) supervisionSubjectPresence() supervision.SubjectPresenceFunc {
	if h == nil {
		return nil
	}
	var runStore supervision.ExecutionRunStore
	if store := h.getSupervisionStore(); store != nil {
		runStore, _ = store.(supervision.ExecutionRunStore)
	}
	teamStore := h.getTeamStore()
	if runStore == nil && teamStore == nil {
		return nil
	}
	return func(ctx context.Context, n supervision.Notification) (bool, bool) {
		subjectID := strings.TrimSpace(n.SubjectID)
		if subjectID == "" {
			return false, false
		}
		switch n.SubjectKind {
		case supervision.SubjectAgentRun:
			if runStore == nil {
				return false, false
			}
			if _, err := runStore.GetExecutionRun(ctx, subjectID); err != nil {
				if errors.Is(err, supervision.ErrRunNotFound) {
					return false, true
				}
				// Store errors stay inconclusive: keep the row critical.
				return false, false
			}
			return true, true
		case supervision.SubjectTeam:
			if teamStore == nil {
				return false, false
			}
			record, err := teamStore.GetTeam(ctx, subjectID)
			if err != nil {
				return false, false
			}
			return record != nil, true
		default:
			return false, false
		}
	}
}

// supervisionHostCapabilities declares which notification action channels this
// HTTP host can actually execute, so digest/snapshot only announce reachable
// actions (P2-12 方案 1). The action service is shared with the runtime control
// plane, so ExecutorReady reports whether mutation actions (cancel/close) have a
// real runtime executor; without one those rows could only be recorded.
func (h *Handler) supervisionHostCapabilities() *supervision.HostCapabilities {
	if h == nil || h.getSupervisionStore() == nil {
		return nil
	}
	caps := supervision.FullHostCapabilities()
	service := h.getSupervisionActionService()
	caps.ControlActions = service != nil && service.ExecutorReady()
	return &caps
}

// injectSupervisionPreflight builds the unresolved lifecycle digest immediately
// before a parent/lead turn starts and prepends it to that turn's prompt. A
// notification becoming visible in the prompt is marked delivered+seen, but is
// deliberately not acknowledged (doc 6.3: seen != acknowledged).
func (h *Handler) injectSupervisionPreflight(ctx context.Context, sessionID, prompt string, runMeta *team.RunMeta) (string, error) {
	store := h.getSupervisionStore()
	sessionID = strings.TrimSpace(sessionID)
	if store == nil || sessionID == "" {
		return prompt, nil
	}
	rootScopeID := sessionID
	targetTeamID := ""
	isTeamLead := false
	if runMeta != nil && runMeta.Team != nil {
		targetTeamID = strings.TrimSpace(runMeta.Team.TeamID)
		// A child task's run meta also has a TeamID. Only the team's registered
		// lead session is allowed to consume the team's supervision inbox.
		if targetTeamID != "" {
			if teamStore := h.getTeamStore(); teamStore != nil {
				if teamRecord, err := teamStore.GetTeam(ctx, targetTeamID); err == nil && teamRecord != nil {
					isTeamLead = strings.TrimSpace(teamRecord.LeadSessionID) == sessionID
				}
			}
		}
	}
	if isTeamLead {
		rootScopeID = targetTeamID
	} else {
		targetTeamID = ""
	}
	digest, err := supervision.BuildDigest(ctx, store, supervision.DigestRequest{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: sessionID,
		TargetParentTeamID:    targetTeamID,
		// Same knob as the CLI preflight (DigestMaxItems): before this the API
		// host hardcoded 20 while the CLI read config, so the two hosts could
		// disagree on the injection budget.
		Limit:                h.supervisionTuning().DigestMaxItems,
		IncludeResolvedSince: true,
		SubjectPresence:      h.supervisionSubjectPresence(),
		HostCapabilities:     h.supervisionHostCapabilities(),
		// P0-B：宿主级共享 batch store 让 progress 区块真正有数据可读（此前
		// Progress 为 nil，判定退化成"只看 lifecycle 行"，与 CLI 宿主不对齐）。
		// 无 batch 控制面的宿主仍是 nil ⇒ digest 与改动前逐字节一致。
		Progress: h.supervisionProgressSource(),
	})
	if err != nil {
		return "", fmt.Errorf("build supervision preflight digest: %w", err)
	}
	// P0-B：progress 区块与 lifecycle 行共用同一个注入判定 —— 只要 batch 还在
	// 跑（Progress 非空），父 turn 就会拿到"2/3 完成、谁还在跑"的 rollup，而不是
	// 等终态 lifecycle 行才第一次可见。
	if digest == nil || (len(digest.Items) == 0 && len(digest.Progress) == 0) {
		return prompt, nil
	}
	now := time.Now().UTC()
	for _, item := range digest.Items {
		// Throttled (plan §4.3): rows the parent already saw are not re-marked,
		// because each write only churned version/updated_at.
		if err := supervision.MarkDigestItemDelivered(ctx, store, item, now); err != nil {
			return "", err
		}
	}
	text := strings.TrimSpace(digest.Text)
	// P1-6 follow-up: carry the auto-wake ledger into the model-visible text, so
	// a rate-limited wake (deferred, never dropped) is observable inside the
	// parent turn instead of only through host diagnostics. A team lead
	// projects both its own session scope and the team scope, matching the HTTP
	// snapshot projection; the helper collapses duplicates and returns nil when
	// no scheduler is wired, keeping unwired hosts byte-identical.
	if budgetLine := supervision.FormatWakeBudgetLine(h.supervisionWakeBudgetStates(ctx, sessionID, targetTeamID)); budgetLine != "" {
		text = strings.TrimSpace(text + "\n" + budgetLine)
	}
	return text + "\n\n" + prompt, nil
}

// InjectSupervisionPreflight exposes the parent/lead turn hook to local hosts
// which submit actor prompts directly rather than through the HTTP runtime
// command endpoint.
func (h *Handler) InjectSupervisionPreflight(ctx context.Context, sessionID, prompt string, runMeta *team.RunMeta) (string, error) {
	return h.injectSupervisionPreflight(ctx, sessionID, prompt, runMeta)
}

// GetSupervisionDigest returns the parent turn preflight lifecycle digest
// (doc 6.4). Query params: root_scope_id, target_parent_session_id,
// target_parent_team_id, after_seq, limit, include_resolved_since.
func (h *Handler) GetSupervisionDigest(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	q := r.URL.Query()
	digest, err := supervision.BuildDigest(r.Context(), h.getSupervisionStore(), supervision.DigestRequest{
		RootScopeID:           strings.TrimSpace(q.Get("root_scope_id")),
		TargetParentSessionID: strings.TrimSpace(q.Get("target_parent_session_id")),
		TargetParentTeamID:    strings.TrimSpace(q.Get("target_parent_team_id")),
		AfterSeq:              int64Query(q.Get("after_seq")),
		Limit:                 intQuery(q.Get("limit")),
		IncludeResolvedSince:  boolQuery(q.Get("include_resolved_since")),
		SubjectPresence:       h.supervisionSubjectPresence(),
		HostCapabilities:      h.supervisionHostCapabilities(),
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	payload := map[string]interface{}{
		"digest": digest,
		// P2-9 可见性：最近一次 registry 一致性对账的缓存摘要。
		"agent_registry_reconcile": h.agentRegistryReconcileSummary(),
	}
	// P1-6 可见性：API 宿主侧的 auto-wake 预算投影，与 CLI
	// `/debug supervision list` 同源（同一 scheduler、同一账本）。
	if budget := h.supervisionWakeBudgetStates(r.Context(), q.Get("root_scope_id")); budget != nil {
		payload["wake_budget"] = budget
	}
	h.writeJSON(w, http.StatusOK, payload)
}

// GetSupervisionSnapshot returns the unified supervision read model
// (doc 6.2). Query params: root_session_id, root_team_id, mode,
// after_seq, health, include_terminal, limit.
func (h *Handler) GetSupervisionSnapshot(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	q := r.URL.Query()
	snapshot, err := supervision.BuildSnapshot(r.Context(), h.getSupervisionStore(), supervision.SnapshotRequest{
		Scope: supervision.Scope{
			RootSessionID: strings.TrimSpace(q.Get("root_session_id")),
			RootTeamID:    strings.TrimSpace(q.Get("root_team_id")),
			Mode:          strings.TrimSpace(q.Get("mode")),
		},
		AfterSeq:         int64Query(q.Get("after_seq")),
		Health:           strings.TrimSpace(q.Get("health")),
		IncludeTerminal:  boolQuery(q.Get("include_terminal")),
		Limit:            intQuery(q.Get("limit")),
		DefaultLimit:     h.supervisionTuning().SnapshotMaxItems,
		Provider:         h.getSupervisionDescendantProvider(),
		HostCapabilities: h.supervisionHostCapabilities(),
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	payload := map[string]interface{}{
		"snapshot": snapshot,
		// P2-9 可见性：最近一次 registry 一致性对账的缓存摘要。
		"agent_registry_reconcile": h.agentRegistryReconcileSummary(),
	}
	// P1-6 可见性：与 digest 同一投影；snapshot 的 root scope 可能是会话
	// （普通会话）或团队（Team lead），两者都算，重复时由 helper 去重。
	if budget := h.supervisionWakeBudgetStates(r.Context(), q.Get("root_session_id"), q.Get("root_team_id")); budget != nil {
		payload["wake_budget"] = budget
	}
	h.writeJSON(w, http.StatusOK, payload)
}

// RequestSupervisionAction persists a durable control action request
// (doc 6.6). Body: root_scope_id, requested_by_kind, requested_by_id,
// target_kind, target_id, action, cascade_mode, reason, expected_version,
// expected_fencing_token.
func (h *Handler) RequestSupervisionAction(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	var req supervision.ActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	service := h.getSupervisionActionService()
	if service == nil {
		service = supervision.NewActionService(h.getSupervisionStore(), nil, nil)
	}
	record, err := service.RequestAction(r.Context(), req)
	if err != nil {
		h.writeSupervisionActionError(w, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"action": record,
	})
}

// ListSupervisionActions lists durable actions. Query params:
// root_scope_id, target_kind, target_id, action, status, limit.
func (h *Handler) ListSupervisionActions(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	q := r.URL.Query()
	records, err := h.getSupervisionStore().ListActions(r.Context(), supervision.ActionFilter{
		RootScopeID: strings.TrimSpace(q.Get("root_scope_id")),
		TargetKind:  supervision.SubjectKind(strings.TrimSpace(q.Get("target_kind"))),
		TargetID:    strings.TrimSpace(q.Get("target_id")),
		Action:      supervision.ActionKind(strings.TrimSpace(q.Get("action"))),
		Status:      supervision.ActionStatus(strings.TrimSpace(q.Get("status"))),
		Limit:       intQuery(q.Get("limit")),
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"actions": records,
	})
}

// GetSupervisionAction returns one durable action.
func (h *Handler) GetSupervisionAction(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	actionID := mux.Vars(r)["id"]
	service := h.getSupervisionActionService()
	if service == nil {
		service = supervision.NewActionService(h.getSupervisionStore(), nil, nil)
	}
	record, err := service.GetAction(r.Context(), actionID)
	if err != nil {
		h.writeSupervisionActionError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"action": record,
	})
}

// AcceptSupervisionAction moves requested -> accepted (CAS), freezing cascade
// roots first (doc 6.6 constraint 3).
func (h *Handler) AcceptSupervisionAction(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	service := h.getSupervisionActionService()
	if service == nil {
		service = supervision.NewActionService(h.getSupervisionStore(), nil, nil)
	}
	record, err := service.AcceptAction(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		h.writeSupervisionActionError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"action": record,
	})
}

// ExecuteSupervisionAction runs the executor and records the terminal outcome
// plus resolution notification (doc 6.6 constraint 7).
func (h *Handler) ExecuteSupervisionAction(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	service := h.getSupervisionActionService()
	if service == nil {
		service = supervision.NewActionService(h.getSupervisionStore(), nil, nil)
	}
	record, err := service.ExecuteAction(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		h.writeSupervisionActionError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"action": record,
	})
}

// AcknowledgeSupervisionNotification acknowledges a lifecycle notification
// (CAS on expected_version). Body: expected_version, note.
func (h *Handler) AcknowledgeSupervisionNotification(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	var req struct {
		ExpectedVersion int64  `json:"expected_version"`
		Note            string `json:"note,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	notificationID := mux.Vars(r)["id"]
	ok, err := h.getSupervisionStore().AcknowledgeNotification(r.Context(), notificationID, time.Now().UTC(), req.ExpectedVersion)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		h.writeError(w, http.StatusConflict, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "notification version changed; re-read snapshot"))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"acknowledged":    true,
		"notification_id": notificationID,
	})
}

// DeferSupervisionNotification defers a lifecycle notification until a time.
// Body: until (RFC3339), reason, expected_version.
func (h *Handler) DeferSupervisionNotification(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	var req struct {
		Until           string `json:"until"`
		Reason          string `json:"reason"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	until, err := time.Parse(time.RFC3339, strings.TrimSpace(req.Until))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "until must be RFC3339"))
		return
	}
	notificationID := mux.Vars(r)["id"]
	ok, err := h.getSupervisionStore().DeferNotification(r.Context(), notificationID, until.UTC(), strings.TrimSpace(req.Reason), req.ExpectedVersion)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		h.writeError(w, http.StatusConflict, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "notification version changed; re-read snapshot"))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"deferred":        true,
		"until":           until.UTC(),
		"notification_id": notificationID,
	})
}

// ScheduleSupervisionWake persists a durable parent-turn wake request
// (doc 6.5 rule 3). Body: root_scope_id, target_parent_session_id,
// target_parent_team_id, wake_reason, notification_seq.
func (h *Handler) ScheduleSupervisionWake(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	var req supervision.WakeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	scheduler := h.getSupervisionWakeScheduler()
	if scheduler == nil {
		// Ad-hoc schedulers built for a single wake request must announce the
		// same reachable actions as the registered control plane (P2-12 方案 1).
		scheduler = supervision.NewWakeScheduler(h.getSupervisionStore(), supervision.WakeSchedulerConfig{
			HostCapabilities: h.supervisionHostCapabilities,
		})
	}
	result, err := scheduler.ScheduleWake(r.Context(), req)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"wake": result,
	})
}

// GetSupervisionAudit is the read-only manual audit endpoint
// (2026-09-22 调整，docs/plan/supervision-manual-audit-plan-20260922.md)。
//
// 语义与 CLI `/supervision audit` 完全一致：把 lifecycle digest、descendant
// 快照、未领取 durable wake 与 wake 预算聚合成一份只读报告。它不 claim、不
// resolve、不改任何通知/动作状态，因此可以随时安全调用；turn 结束自动核查
// 关闭（supervision.turn_end_check）后，这是运维与脚本的显式核查入口。
//
// Query params: root_scope_id, root_session_id, root_team_id, mode,
// target_parent_session_id, target_parent_team_id, after_seq, limit,
// include_terminal, include_resolved_since.
func (h *Handler) GetSupervisionAudit(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	q := r.URL.Query()
	rootSessionID := strings.TrimSpace(q.Get("root_session_id"))
	rootTeamID := strings.TrimSpace(q.Get("root_team_id"))
	rootScopeID := strings.TrimSpace(q.Get("root_scope_id"))
	if rootScopeID == "" {
		rootScopeID = firstNonEmptySupervision(rootSessionID, rootTeamID)
	}
	if rootScopeID == "" {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "root_scope_id or root_session_id is required"))
		return
	}
	targetParentSessionID := strings.TrimSpace(q.Get("target_parent_session_id"))
	targetParentTeamID := strings.TrimSpace(q.Get("target_parent_team_id"))
	limit := intQuery(q.Get("limit"))
	afterSeq := int64Query(q.Get("after_seq"))
	if rootSessionID == "" && rootTeamID == "" {
		rootSessionID = rootScopeID
	}
	digest, err := supervision.BuildDigest(r.Context(), h.getSupervisionStore(), supervision.DigestRequest{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: targetParentSessionID,
		TargetParentTeamID:    targetParentTeamID,
		AfterSeq:              afterSeq,
		Limit:                 limit,
		IncludeResolvedSince:  boolQuery(q.Get("include_resolved_since")),
		SubjectPresence:       h.supervisionSubjectPresence(),
		HostCapabilities:      h.supervisionHostCapabilities(),
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	snapshot, err := supervision.BuildSnapshot(r.Context(), h.getSupervisionStore(), supervision.SnapshotRequest{
		Scope: supervision.Scope{
			RootSessionID: rootSessionID,
			RootTeamID:    rootTeamID,
			Mode:          strings.TrimSpace(q.Get("mode")),
		},
		AfterSeq:         afterSeq,
		Health:           strings.TrimSpace(q.Get("health")),
		IncludeTerminal:  boolQuery(q.Get("include_terminal")),
		Limit:            limit,
		DefaultLimit:     h.supervisionTuning().SnapshotMaxItems,
		Provider:         h.getSupervisionDescendantProvider(),
		HostCapabilities: h.supervisionHostCapabilities(),
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	pending, err := h.getSupervisionStore().ListWakePending(r.Context(), supervision.WakeFilter{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: targetParentSessionID,
		TargetParentTeamID:    targetParentTeamID,
		UnclaimedOnly:         true,
		Limit:                 limit,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if pending == nil {
		pending = []supervision.WakePending{}
	}
	turnEndCheck := h.supervisionTuning().TurnEndCheckEnabled()
	payload := map[string]interface{}{
		"generated_at": time.Now().UTC(),
		// 自动核查开关：false（默认）表示 turn 结束不再 drain / 不起
		// digest-only self-check，核查只能由本端点或 CLI `/supervision` 显式发起。
		"auto_audit_enabled": turnEndCheck,
		"turn_end_check":     turnEndCheck,
		"root_scope_id":      rootScopeID,
		"digest":             digest,
		"snapshot":           snapshot,
		"pending_wakes":      pending,
		"pending_wake_count": len(pending),
	}
	if budget := h.supervisionWakeBudgetStates(r.Context(), rootScopeID); budget != nil {
		payload["wake_budget"] = budget
	}
	h.writeJSON(w, http.StatusOK, payload)
}

// DrainSupervisionWakes is the explicit manual delivery endpoint
// (POST /api/runtime/supervision/wake/drain).
//
// 它复用宿主唯一的 wake consumer（同一 runnable 门 + 同一预算账本 + 同一
// Deliver 路径），因此手动投递不会绕过"父会话忙 / 预算耗尽"闸门：被闸门拦下
// 时 wake 保持 durable，下一次自然 turn 的 preflight digest 仍会注入。
//
// Body: root_scope_id, target_parent_session_id, target_parent_team_id,
// dry_run (只统计不投递), limit。
func (h *Handler) DrainSupervisionWakes(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	var req struct {
		RootScopeID           string `json:"root_scope_id"`
		TargetParentSessionID string `json:"target_parent_session_id"`
		TargetParentTeamID    string `json:"target_parent_team_id"`
		DryRun                bool   `json:"dry_run"`
		Limit                 int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	rootScopeID := strings.TrimSpace(req.RootScopeID)
	if rootScopeID == "" {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "root_scope_id is required"))
		return
	}
	scheduler := h.getSupervisionWakeScheduler()
	if scheduler == nil {
		h.writeError(w, http.StatusServiceUnavailable, runtimeerrors.New(runtimeerrors.ErrConfigInvalid, "supervision wake scheduler not configured"))
		return
	}
	targetParentSessionID := strings.TrimSpace(req.TargetParentSessionID)
	targetParentTeamID := strings.TrimSpace(req.TargetParentTeamID)
	filter := supervision.WakeFilter{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: targetParentSessionID,
		TargetParentTeamID:    targetParentTeamID,
		UnclaimedOnly:         true,
		Limit:                 req.Limit,
	}
	pending, err := h.getSupervisionStore().ListWakePending(r.Context(), filter)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	payload := map[string]interface{}{
		"root_scope_id":      rootScopeID,
		"pending_wake_count": len(pending),
		"pending_wakes":      pending,
	}
	if budget := h.supervisionWakeBudgetStates(r.Context(), rootScopeID); budget != nil {
		payload["wake_budget"] = budget
	}
	if req.DryRun {
		payload["dry_run"] = true
		payload["reason"] = "dry_run"
		h.writeJSON(w, http.StatusOK, payload)
		return
	}
	consumer := h.supervisionWakeConsumer(scheduler)
	if consumer == nil {
		h.writeError(w, http.StatusServiceUnavailable, runtimeerrors.New(runtimeerrors.ErrConfigInvalid, "supervision wake consumer not configured"))
		return
	}
	deliverErr := consumer.MaybeWakeParent(r.Context(), targetParentSessionID, targetParentTeamID, rootScopeID)
	remaining, listErr := h.getSupervisionStore().ListWakePending(r.Context(), filter)
	if listErr != nil {
		h.writeError(w, http.StatusInternalServerError, listErr)
		return
	}
	consumed := len(pending) - len(remaining)
	payload["pending_remaining"] = len(remaining)
	payload["consumed"] = consumed
	status := http.StatusOK
	reason := "delivered"
	switch {
	case deliverErr == nil:
		switch {
		case len(pending) == 0:
			reason = "no_pending_wake"
		case consumed == 0:
			// 消费 0 行：digest 无投递内容（通知已被 ack/resolve）或并发 drainer
			// 抢先 claim；wake 行按既有语义已被清理，不需要重试。
			reason = "no_deliverable_content"
		}
	case isSupervisionSentinel(deliverErr, supervision.ErrWakeParentBusy):
		status = http.StatusConflict
		reason = "parent_busy"
	case isSupervisionSentinel(deliverErr, supervision.ErrWakeRateLimited):
		status = http.StatusTooManyRequests
		reason = "wake_rate_limited"
	default:
		h.writeError(w, http.StatusInternalServerError, deliverErr)
		return
	}
	if deliverErr != nil {
		payload["delivery_error"] = deliverErr.Error()
	}
	payload["reason"] = reason
	h.writeJSON(w, status, payload)
}

// RecordSupervisionTeamEdge records a durable parent Team -> child Team edge
// (doc 6.7 rule 1). Body: root_team_id, parent_team_id, parent_kind,
// parent_id, child_team_id, relation, created_by.
func (h *Handler) RecordSupervisionTeamEdge(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	var req struct {
		RootTeamID   string `json:"root_team_id"`
		ParentTeamID string `json:"parent_team_id"`
		ParentKind   string `json:"parent_kind"`
		ParentID     string `json:"parent_id"`
		ChildTeamID  string `json:"child_team_id"`
		Relation     string `json:"relation"`
		CreatedBy    string `json:"created_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	edge := supervision.TeamEdge{
		RootScopeID:  firstNonEmptySupervision(req.RootTeamID, req.ParentTeamID),
		RootTeamID:   req.RootTeamID,
		ParentTeamID: req.ParentTeamID,
		ParentKind:   req.ParentKind,
		ParentID:     req.ParentID,
		ChildTeamID:  req.ChildTeamID,
		Relation:     firstNonEmptySupervision(req.Relation, "nested"),
		CreatedBy:    req.CreatedBy,
		CreatedAt:    time.Now().UTC(),
		Status:       supervision.TeamEdgeStatusActive,
		Version:      1,
	}
	recorded, err := h.getSupervisionStore().UpsertTeamEdge(r.Context(), edge)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"edge": recorded,
	})
}

// ListSupervisionTeamEdges lists active child Team edges. Query params:
// parent_team_id.
func (h *Handler) ListSupervisionTeamEdges(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	parentTeamID := strings.TrimSpace(r.URL.Query().Get("parent_team_id"))
	edges, err := h.getSupervisionStore().ListChildTeams(r.Context(), parentTeamID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"edges": edges,
	})
}

func (h *Handler) writeSupervisionActionError(w http.ResponseWriter, err error) {
	switch {
	case isSupervisionSentinel(err, supervision.ErrActionConflict):
		h.writeError(w, http.StatusConflict, runtimeerrors.New(runtimeerrors.ErrValidationFailed, err.Error()))
	case isSupervisionSentinel(err, supervision.ErrActionNotAllowed):
		h.writeError(w, http.StatusForbidden, runtimeerrors.New(runtimeerrors.ErrValidationFailed, err.Error()))
	case isSupervisionSentinel(err, supervision.ErrActionNotFound):
		h.writeError(w, http.StatusNotFound, runtimeerrors.New(runtimeerrors.ErrValidationFailed, err.Error()))
	case isSupervisionSentinel(err, supervision.ErrActionNotActionable):
		h.writeError(w, http.StatusConflict, runtimeerrors.New(runtimeerrors.ErrValidationFailed, err.Error()))
	default:
		h.writeError(w, http.StatusInternalServerError, err)
	}
}

func isSupervisionSentinel(err error, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

func firstNonEmptySupervision(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func int64Query(raw string) int64 {
	value, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return value
}

func intQuery(raw string) int {
	value, _ := strconv.Atoi(strings.TrimSpace(raw))
	return value
}

func boolQuery(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
