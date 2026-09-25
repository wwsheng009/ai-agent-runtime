package chat

import (
	"context"
	"fmt"
	"strings"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// maxPlanReviewBytes bounds the plan body returned by plan_review. Reviews are
// read by a model and a user, so the tool truncates instead of failing: the
// full text stays available through /plan review and the plans browser.
const maxPlanReviewBytes = 64 * 1024

// planReviewVerdictOptions lists the host commands that decide a review. They
// are returned with every payload so the model can relay an actionable hint
// instead of inventing a verdict.
func planReviewVerdictOptions() []string {
	return []string{"/plan approve", "/plan request_changes <notes>", "/plan quit"}
}

// ReviewPlan implements toolbroker.PlanReviewController: it loads a plan (the
// session plan or an archived one) for the review surface without mutating plan
// state. Opening a review never approves, edits or exits a plan.
func (a *SessionActor) ReviewPlan(ctx context.Context, sessionID string, args toolbroker.PlanReviewArgs) (*toolbroker.PlanReviewResult, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if a.IsStopped() {
		return nil, ErrSessionActorStopped
	}
	if err := a.ensurePlanModeSessionID(sessionID); err != nil {
		return nil, err
	}

	session, err := a.loadSession(ctx)
	if err != nil {
		return nil, err
	}

	planID := strings.TrimSpace(args.PlanID)
	if planID != "" {
		return a.reviewArchivedPlan(planID, args.Version)
	}

	state := planmode.Load(session)
	workspace := planModeWorkspacePath(session)
	planPath := strings.TrimSpace(args.PlanPath)
	if planPath == "" {
		planPath = strings.TrimSpace(state.PlanPath)
	}
	if planPath == "" {
		planPath = planmode.DefaultPlanPath
	}

	result := &toolbroker.PlanReviewResult{
		Active:         planmode.IsActive(state),
		Status:         string(state.Status),
		PlanPath:       planPath,
		ReviewRound:    state.ReviewRound,
		Source:         "session",
		VerdictOptions: planReviewVerdictOptions(),
	}
	// A session plan may also have an archived record (same path, newest owner
	// wins): surface its id/version so follow-up calls can address the archive.
	if rec, ok := a.findArchivedPlan(session.ID, planPath); ok {
		result.PlanID = rec.ID
		result.Version = rec.Version
		if result.Status == "" {
			result.Status = string(rec.Status)
		}
	}

	content := planmode.ReadPlanArtifact(workspace, planPath)
	if len(content) == 0 {
		result.Hint = fmt.Sprintf("计划文件 %s 目前为空或不可读；请先写入计划，再让用户裁决（%s）。",
			planPath, strings.Join(result.VerdictOptions, " / "))
	} else {
		body, truncated := clampPlanReviewContent(content)
		result.Content = body
		result.ContentSize = len(content)
		result.Truncated = truncated
		if result.Active {
			result.Hint = fmt.Sprintf("计划 %s（第 %d 轮）已打开供评审；等待用户裁决：%s。",
				planPath, result.ReviewRound, strings.Join(result.VerdictOptions, " / "))
		} else {
			result.Hint = fmt.Sprintf("计划 %s 已打开（状态 %s）；如需再次评审，请用户用 /plan enter 重新进入计划模式。",
				planPath, firstNonEmptyString(result.Status, string(planstore.StatusPending)))
		}
	}

	a.publishPlanReviewRequested(result)
	return result, nil
}

// reviewArchivedPlan loads one archived snapshot by record id.
func (a *SessionActor) reviewArchivedPlan(planID string, version int) (*toolbroker.PlanReviewResult, error) {
	store := a.planArtifactStore()
	rec, ok, err := store.Get(planID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("plan %q not found in the plan archive", planID)
	}

	var content []byte
	if version > 0 {
		content, err = store.ReadVersion(planID, version)
	} else {
		version = rec.Version
		content, err = store.ReadLatest(planID)
	}
	if err != nil {
		return nil, err
	}

	result := &toolbroker.PlanReviewResult{
		Active:         false,
		Status:         string(rec.Status),
		PlanID:         rec.ID,
		PlanPath:       rec.PlanPath,
		Version:        version,
		ReviewRound:    len(rec.Rounds),
		Source:         "archive",
		ContentSize:    len(content),
		VerdictOptions: planReviewVerdictOptions(),
	}
	body, truncated := clampPlanReviewContent(content)
	result.Content = body
	result.Truncated = truncated
	if len(content) == 0 {
		result.Hint = fmt.Sprintf("归档计划 %s 暂无快照正文；可用 /plans %s 查看元数据。", rec.ID, rec.ID)
	} else {
		result.Hint = fmt.Sprintf("归档计划 %s（v%d，状态 %s，%d 轮）已打开供阅读；归档评审面只读，重新评审请先 /plan enter。",
			rec.ID, version, rec.Status, len(rec.Rounds))
	}

	a.publishPlanReviewRequested(result)
	return result, nil
}

// findArchivedPlan matches a session plan path against the archive index.
func (a *SessionActor) findArchivedPlan(sessionID, planPath string) (planstore.Record, bool) {
	planPath = strings.TrimSpace(planPath)
	if planPath == "" {
		return planstore.Record{}, false
	}
	records, err := a.planArtifactStore().List()
	if err != nil {
		return planstore.Record{}, false
	}
	want := normalizePlanPathForMatch(planPath)
	for _, rec := range records {
		if normalizePlanPathForMatch(rec.PlanPath) != want {
			continue
		}
		if sessionID != "" && rec.SessionID != "" && rec.SessionID != sessionID {
			continue
		}
		return rec, true
	}
	return planstore.Record{}, false
}

// normalizePlanPathForMatch lowercases and cleans a plan path for comparison so
// "docs/plan.md" and "docs\\plan.md" resolve to the same archive record.
func normalizePlanPathForMatch(path string) string {
	path = strings.TrimSpace(path)
	path = strings.ReplaceAll(path, "\\", "/")
	path = strings.TrimPrefix(path, "./")
	return strings.ToLower(path)
}

// clampPlanReviewContent bounds the returned body and reports truncation.
func clampPlanReviewContent(content []byte) (string, bool) {
	if len(content) <= maxPlanReviewBytes {
		return string(content), false
	}
	return string(content[:maxPlanReviewBytes]), true
}

// publishPlanReviewRequested announces that a review surface was opened. The
// Web panels use it to focus/refresh the plan view; CLI hosts render their own
// hint from the tool result.
func (a *SessionActor) publishPlanReviewRequested(result *toolbroker.PlanReviewResult) {
	if a == nil || result == nil {
		return
	}
	payload := map[string]interface{}{
		"plan_id":      result.PlanID,
		"plan_path":    result.PlanPath,
		"source":       result.Source,
		"status":       result.Status,
		"round":        result.ReviewRound,
		"active":       result.Active,
		"truncated":    result.Truncated,
		"content_size": result.ContentSize,
	}
	a.publish(runtimeevents.Event{
		Type:      EventPlanReviewRequested,
		SessionID: a.id,
		Payload:   payload,
	})
}

// firstNonEmptyString returns the first non-blank value.
func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
