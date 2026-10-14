package runtimeapi

// web inline 路径的 plan_review 控制器
// （docs/architecture/unified-tool-surface-registry.md §16.5 后置项的第一步）。
//
// 为什么先接 plan_review：它是交互通道三工具（ask_user_question / plan_enter|exit /
// plan_review）里唯一**只读、无跨请求挂起**的——PlanID 走 planstore 归档读取、
// session plan 读 durable 会话上下文里的 planmode 状态，都是同步操作，web 直连
// 请求内即可完成。ask_user_question 与 plan_enter/exit 依赖跨请求的挂起/恢复
// 语义（actor 已实现 waiter + answer_question 命令唤醒），inline 请求生命周期
// 无法承载，仍归 actor 路径（web 会话需要交互能力时经 durable actor 使用）。
//
// 载荷契约与 SessionActor.ReviewPlan（internal/chat/plan_review_tool.go）一致：
// 同一 PlanReviewResult / PlanReviewDiff 形状、同一 64KB 截断口径、归档 diff 走
// 同一 planmode.DiffArchivedVersions 引擎，两个表面的输出逐字节同源。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// maxPlanReviewBytes 与 actor 侧同一截断口径：载荷要同时被模型与用户阅读，
// 超限截断而不是失败（全文经 /plan review 与 plans browser 可得）。
const maxPlanReviewBytes = 64 * 1024

// planReviewVerdictOptions 与 actor 侧同串：裁决命令是 TUI 面（/plan approve…），
// web 面的裁决入口由 pending approval 通道承载，这里原样返回保持模型提示一致。
func planReviewVerdictOptions() []string {
	return []string{"/plan approve", "/plan request_changes <notes>", "/plan quit"}
}

// planReviewController 提供 web inline 路径的 plan_review 只读实现。
// plansStore 可空（未配置归档目录时仅支持 session plan）。
type planReviewController struct {
	handler    *Handler
	plansStore *planstore.Store
}

func (h *Handler) planReviewControllerForSession() *planReviewController {
	if h == nil {
		return nil
	}
	return &planReviewController{handler: h, plansStore: h.plansStore}
}

// ReviewPlan implements toolbroker.PlanReviewController：只读加载，不改 plan 状态。
func (c *planReviewController) ReviewPlan(ctx context.Context, sessionID string, args toolbroker.PlanReviewArgs) (*toolbroker.PlanReviewResult, error) {
	if c == nil || c.handler == nil {
		return nil, fmt.Errorf("plan review controller is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID = strings.TrimSpace(sessionID)

	if planID := strings.TrimSpace(args.PlanID); planID != "" {
		return c.reviewArchivedPlan(planID, args.Version, args.CompareVersion)
	}

	session := c.handler.sessionByID(ctx, sessionID)
	if session == nil {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	state := planmode.Load(session)
	planPath := strings.TrimSpace(args.PlanPath)
	if planPath == "" {
		planPath = strings.TrimSpace(state.PlanPath)
	}
	if planPath == "" {
		planPath = planmode.DefaultPlanPath
	}

	content, readErr := readWorkspacePlanFile(session, planPath)
	if readErr != nil {
		return nil, readErr
	}
	body, truncated := clampPlanReviewContent(content)
	if args.CompareVersion > 0 {
		return nil, fmt.Errorf("plan review diff requires an archived plan (plan_id); session plans keep no round history")
	}

	return &toolbroker.PlanReviewResult{
		Active:         planmode.IsActive(state),
		Status:         string(state.Status),
		PlanPath:       planPath,
		ReviewRound:    state.ReviewRound,
		Source:         "session",
		VerdictOptions: planReviewVerdictOptions(),
		Content:        body,
		ContentSize:    len(body),
		Truncated:      truncated,
		Hint:           planReviewHint(state.ReviewRound),
	}, nil
}

// reviewArchivedPlan 从 planstore 归档读取（与 actor 侧同语义：version=0 取最新；
// compareVersion 走同一 DiffArchivedVersions 引擎）。
func (c *planReviewController) reviewArchivedPlan(planID string, version, compareVersion int) (*toolbroker.PlanReviewResult, error) {
	if c.plansStore == nil {
		return nil, fmt.Errorf("plans archive is not configured")
	}
	planID = strings.TrimSpace(planID)
	record, ok, err := c.plansStore.Get(planID)
	if err != nil {
		return nil, fmt.Errorf("load plan %q: %w", planID, err)
	}
	if !ok {
		return nil, fmt.Errorf("plan %q not found in the plan archive", planID)
	}

	var content []byte
	if version > 0 {
		content, err = c.plansStore.ReadVersion(planID, version)
	} else {
		version = record.Version
		content, err = c.plansStore.ReadLatest(planID)
	}
	if err != nil {
		return nil, fmt.Errorf("read plan %q: %w", planID, err)
	}

	body, truncated := clampPlanReviewContent(content)
	result := &toolbroker.PlanReviewResult{
		Active:         false,
		Status:         string(record.Status),
		PlanID:         record.ID,
		PlanPath:       record.PlanPath,
		Version:        version,
		ReviewRound:    len(record.Rounds),
		Source:         "archive",
		VerdictOptions: planReviewVerdictOptions(),
		Content:        body,
		ContentSize:    len(body),
		Truncated:      truncated,
	}
	if compareVersion > 0 {
		diff, diffErr := planmode.DiffArchivedVersions(planmode.DiffVersionsOptions{
			Store:    c.plansStore,
			RecordID: planID,
			From:     compareVersion,
			To:       version,
		})
		if diffErr != nil {
			return nil, fmt.Errorf("plan_review compare_version v%d: %w", compareVersion, diffErr)
		}
		result.Diff = &toolbroker.PlanReviewDiff{
			FromVersion: diff.FromVersion,
			ToVersion:   diff.ToVersion,
			Text:        diff.Text,
			Added:       diff.Added,
			Removed:     diff.Removed,
			Identical:   diff.Identical,
			Truncated:   diff.Truncated,
			Coarse:      diff.Coarse,
		}
	}
	result.Hint = planReviewArchiveHint(record, result)
	return result, nil
}

func clampPlanReviewContent(content []byte) (string, bool) {
	if len(content) > maxPlanReviewBytes {
		return string(content[:maxPlanReviewBytes]), true
	}
	return string(content), false
}

func planReviewHint(reviewRound int) string {
	if reviewRound > 0 {
		return fmt.Sprintf("Plan review round %d is open. Relay the verdict options to the user and wait for their decision.", reviewRound)
	}
	return "Relay the verdict options to the user and wait for their decision."
}

func planReviewArchiveHint(record planstore.Record, result *toolbroker.PlanReviewResult) string {
	if result.Content == "" {
		return fmt.Sprintf("归档计划 %s 暂无快照正文；可用 /plans %s 查看元数据。", record.ID, record.ID)
	}
	if result.Diff != nil {
		return fmt.Sprintf("归档计划 %s（v%d，状态 %s）：v%d -> v%d 的轮次对比已附（+%d -%d）。",
			record.ID, result.Version, record.Status, result.Diff.FromVersion, result.Diff.ToVersion, result.Diff.Added, result.Diff.Removed)
	}
	return fmt.Sprintf("归档计划 %s（v%d，状态 %s）。", record.ID, result.Version, record.Status)
}

// readWorkspacePlanFile 读会话工作区内的 plan 文件（session plan 路径相对
// workspace 解析，与 actor 侧 planModeWorkspacePath 同一锚定语义）。绝对路径
// 原样使用（planmode 状态写的是会话内的绝对路径）；相对路径锚定 workspace，
// 越出 workspace 的相对路径按错误处理（只读工具不放大读取面）。
func readWorkspacePlanFile(session *chat.Session, planPath string) ([]byte, error) {
	path := strings.TrimSpace(planPath)
	if path == "" {
		return nil, fmt.Errorf("plan path is empty")
	}
	if !filepath.IsAbs(path) {
		workspace := sessionWorkspaceDir(session)
		if workspace == "" {
			return nil, fmt.Errorf("plan path %q is relative but the session has no workspace", planPath)
		}
		joined := filepath.Clean(filepath.Join(workspace, path))
		if !strings.HasPrefix(joined, filepath.Clean(workspace)+string(filepath.Separator)) {
			return nil, fmt.Errorf("plan path %q escapes the session workspace", planPath)
		}
		path = joined
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plan file %s: %w", path, err)
	}
	return content, nil
}
