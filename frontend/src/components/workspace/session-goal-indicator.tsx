/**
 * 会话目标四相指示（P2-9 子片 3，MVP）。
 *
 * 只读投影：数据来自 `lib/session-goal`（SSE 边界捕获的 get_goal / update_goal
 * 完整结果）。无数据不渲染；状态未知时如实显示「状态未知」并透出后端原始 status。
 * 暂停/恢复/完成入口不由本组件提供——后端尚未暴露 REST（§6.3 P2-1B），
 * 不放置死按钮。
 */
import { TargetIcon } from "lucide-react";
import { type TFunction } from "i18next";
import { useTranslation } from "react-i18next";

import { formatGoalTokens, type SessionGoal, type SessionGoalPhase } from "@/lib/session-goal/derive";
import { useSessionGoal } from "@/lib/session-goal/store";

function phaseLabel(phase: SessionGoalPhase | null, t: TFunction<"workspace">): string {
  switch (phase) {
    case "active":
      return t("topbar.goal.phase.active");
    case "paused":
      return t("topbar.goal.phase.paused");
    case "budget_limited":
      return t("topbar.goal.phase.budget_limited");
    case "complete":
      return t("topbar.goal.phase.complete");
    default:
      return t("topbar.goal.phase.unknown");
  }
}

/** token 展示：两侧都有才显示 `used / budget`；只有一侧时只显示该侧（不补零）。 */
function usageLabel(goal: SessionGoal, t: TFunction<"workspace">): string {
  const used =
    goal.tokensUsed === undefined ? "" : formatGoalTokens(goal.tokensUsed);
  const budget =
    goal.tokenBudget === undefined ? "" : formatGoalTokens(goal.tokenBudget);
  if (used && budget) {
    return t("topbar.goal.usage", { used, budget });
  }
  if (used) {
    return t("topbar.goal.usageUsed", { used });
  }
  return "";
}

function goalTitle(goal: SessionGoal, label: string, t: TFunction<"workspace">): string {
  const lines = [label];
  if (goal.objective) {
    lines.push(t("topbar.goal.objective", { objective: goal.objective }));
  }
  if (!goal.phase && goal.statusRaw) {
    lines.push(t("topbar.goal.statusRaw", { status: goal.statusRaw }));
  }
  const usage = usageLabel(goal, t);
  if (usage) {
    lines.push(usage);
  }
  if (goal.completedBy) {
    lines.push(t("topbar.goal.completedBy", { who: goal.completedBy }));
  }
  if (goal.completionSummary) {
    lines.push(t("topbar.goal.completionSummary", { summary: goal.completionSummary }));
  }
  if (goal.updatedAt) {
    lines.push(t("topbar.goal.updatedAt", { time: goal.updatedAt }));
  }
  lines.push(t("topbar.goal.derivedNote"));
  return lines.join("\n");
}

export function SessionGoalIndicator({ sessionId }: { sessionId?: string }) {
  const goal = useSessionGoal(sessionId);
  const { t } = useTranslation("workspace");
  if (!goal) {
    return null;
  }

  const label = phaseLabel(goal.phase, t);
  const usage = usageLabel(goal, t);

  return (
    <div
      className="hidden shrink-0 items-center gap-1.5 rounded-card border border-border px-2 py-1 text-muted-foreground sm:flex"
      data-phase={goal.phase ?? "unknown"}
      data-testid="topbar-session-goal"
      title={goalTitle(goal, label, t)}
    >
      <TargetIcon className="shrink-0" size={13} />
      <span className="app-text-10" data-testid="topbar-session-goal-phase">
        {label}
      </span>
      {goal.objective ? (
        <span
          className="max-w-[14rem] truncate app-text-10 text-muted-foreground/80"
          data-testid="topbar-session-goal-objective"
        >
          {goal.objective}
        </span>
      ) : null}
      {usage ? (
        <span className="app-text-10" data-testid="topbar-session-goal-usage">
          {usage}
        </span>
      ) : null}
    </div>
  );
}
