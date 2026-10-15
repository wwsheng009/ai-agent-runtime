/**
 * 会话目标四相指示 + 管理入口（P2-9 子片 3；2026-10-15 接入 REST）。
 *
 * 数据来源两级（见 `lib/session-goal/store`）：后端权威快照（GET/POST
 * `/api/runtime/sessions/{id}/goal`，经 `useRuntimeSessionGoal`）与 SSE 派生投影
 * （`tool_end` 的 get_goal / update_goal 完整结果）。展示的是否为权威数据由
 * `authoritative` 如实标注：非权威时保留"只读投影"说明，权威时标注快照来源。
 *
 * 动作按当前相位给**有效**的那一个（active → 暂停；paused/budget_limited → 恢复；
 * 非 complete → 标记完成；恒有清除），不放置无效按钮。服务端守卫拒绝（applied=false）
 * 时如实显示 message，不伪装成功。
 */
import { CheckIcon, PauseIcon, PlayIcon, TargetIcon, Trash2Icon } from "lucide-react";
import { type ReactNode } from "react";
import { type TFunction } from "i18next";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { useRuntimeSessionGoal } from "@/hooks/workspace/use-runtime-goal";
import { type SessionGoalAction } from "@/api/runtime/goal";
import { formatGoalTokens, type SessionGoal, type SessionGoalPhase } from "@/lib/session-goal/derive";

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

function goalTitle(
  goal: SessionGoal,
  label: string,
  authoritative: boolean,
  t: TFunction<"workspace">,
): string {
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
  lines.push(
    authoritative ? t("topbar.goal.authoritativeNote") : t("topbar.goal.derivedNote"),
  );
  return lines.join("\n");
}

type GoalActionButton = {
  key: SessionGoalAction;
  label: string;
  icon: ReactNode;
  destructive?: boolean;
};

/** 按相位给出有效动作（无效动作不渲染，避免死按钮）。 */
function goalActions(goal: SessionGoal, t: TFunction<"workspace">): GoalActionButton[] {
  const actions: GoalActionButton[] = [];
  if (goal.phase === "active") {
    actions.push({
      key: "pause",
      label: t("topbar.goal.actionPause"),
      icon: <PauseIcon size={12} />,
    });
  }
  if (goal.phase === "paused" || goal.phase === "budget_limited") {
    actions.push({
      key: "resume",
      label: t("topbar.goal.actionResume"),
      icon: <PlayIcon size={12} />,
    });
  }
  if (goal.phase !== "complete") {
    actions.push({
      key: "complete",
      label: t("topbar.goal.actionComplete"),
      icon: <CheckIcon size={12} />,
    });
  }
  actions.push({
    key: "clear",
    label: t("topbar.goal.actionClear"),
    icon: <Trash2Icon size={12} />,
    destructive: true,
  });
  return actions;
}

export function SessionGoalIndicator({ sessionId }: { sessionId?: string }) {
  const { goal, authoritative, pending, error, notice, run } =
    useRuntimeSessionGoal(sessionId);
  const { t } = useTranslation("workspace");
  if (!goal) {
    return null;
  }

  const label = phaseLabel(goal.phase, t);
  const usage = usageLabel(goal, t);
  const status = error ?? notice;

  return (
    <div
      className="hidden shrink-0 items-center gap-1.5 rounded-card border border-border px-2 py-1 text-muted-foreground sm:flex"
      data-phase={goal.phase ?? "unknown"}
      data-authoritative={authoritative ? "true" : "false"}
      data-testid="topbar-session-goal"
      title={goalTitle(goal, label, authoritative, t)}
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
      {goalActions(goal, t).map((action) => (
        <Button
          key={action.key}
          type="button"
          variant="ghost"
          size="icon"
          className={
            action.destructive
              ? "size-5 text-muted-foreground hover:text-accent-orange"
              : "size-5"
          }
          disabled={pending}
          title={action.label}
          aria-label={action.label}
          data-testid={`topbar-session-goal-${action.key}`}
          onClick={() => {
            void run({ action: action.key });
          }}
        >
          {action.icon}
        </Button>
      ))}
      {status ? (
        <span
          className={
            error
              ? "max-w-[12rem] truncate app-text-10 text-accent-orange"
              : "max-w-[12rem] truncate app-text-10 text-muted-foreground/70"
          }
          role="status"
          data-testid="topbar-session-goal-status"
          title={status}
        >
          {status}
        </span>
      ) : null}
    </div>
  );
}
