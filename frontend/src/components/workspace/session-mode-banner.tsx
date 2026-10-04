// 会话上沿停靠列的状态行（plan 上下文 + §6.8 托管挂起）。
//
// 权限模式不再在此处常显：composer 底部的 `ComposerPermissionModeControl` 已是模式
// 展示与切换的唯一入口，顶部再渲染一份「模式」属于重复表达（用户可见的重复控件），
// 故移除。本组件只保留底部控件不承载的信息：plan 模式下的状态读法 / 计划路径，
// 以及托管挂起与迟到唤醒提示。
//
// 落点理由（§6.8 的可观测表达）：本组件是 composer 上沿停靠列里**始终可见**的
// 状态行——会话存在即渲染，不依赖弹层或输入态；`composer-status-row` 承载的是
// 附件 / 命令行这类输入态，`session-agents-panel` 只在面板打开时可见，都无法让
// 用户随时区分「挂起等待」与「卡死」。为不与「模式」语义混淆，托管段独立成段、
// 带自己的 testid（`session-parked-turn`），且模式快照缺失时也能单独显示。
//
// 契约：只消费页面既有的 `/plan` 快照与已归约的挂起快照（不新增取数），
// 不承载任何裁决动作——裁决入口仍是 composer 上沿的 pending bar 与右侧计划面板，
// 避免出现第二套 pending 判定（P1-7 的口径）。

import { HourglassIcon, RotateCcwIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { sessionModeBannerHintLeaf } from "@/components/workspace/session-mode-banner-shared";
import type { ParkedTurnView } from "@/lib/parked-turn";
import type { RuntimeSessionPlanMode } from "@/lib/runtime-api";
import { cn } from "@/lib/utils";

type SessionModeBannerProps = {
  className?: string;
  /** §6.8 托管挂起视图（快照 + 可选任务投影）；null / 缺省 = 未挂起（不占位）。 */
  parkedTurn?: ParkedTurnView | null;
  plan: RuntimeSessionPlanMode | null;
  planStatusLabel?: string;
  sessionId?: string;
};

export function SessionModeBanner({
  className,
  parkedTurn,
  plan,
  planStatusLabel,
  sessionId,
}: SessionModeBannerProps) {
  const { t } = useTranslation("workspace");
  const planActive = Boolean(plan?.active);
  const hintLeaf = sessionModeBannerHintLeaf(plan);

  const parkedTurnSnapshot = parkedTurn?.turn ?? null;
  const parkedTaskCounts = parkedTurn?.taskCounts ?? null;
  const resumedNotice = parkedTurn?.resumedNotice ?? null;

  // 无会话 / 既无 plan 上下文也无挂起态：不占位（权限模式控件在 composer 底部，不在这里重复）。
  if (!sessionId || (!planActive && !parkedTurn)) {
    return null;
  }

  const taskTotal = parkedTaskCounts
    ? parkedTaskCounts.running + parkedTaskCounts.completed + parkedTaskCounts.failed
    : 0;
  // 设计文案（§6.8）：有任务投影时显示「N 个任务运行中（M 完成 / K 异常）」；
  // 目录未加载 / 投影为空时如实降级为挂起事件里的义务数，不编造计数。
  const parkedLabel = parkedTurnSnapshot
    ? parkedTaskCounts && taskTotal > 0
      ? (t("composer.parkedTurn.tasks", {
          // i18next 的静态键插值类型按字符串收口（与 formatAgentDuration 的
          // Label.values 同口径），计数在这里显式转字符串。
          running: String(parkedTaskCounts.running),
          completed: String(parkedTaskCounts.completed),
          failed: String(parkedTaskCounts.failed),
        }) as string)
      : (t("composer.parkedTurn.waiting", {
          count: parkedTurnSnapshot.obligationCount,
        }) as string)
    : "";
  // 迟到唤醒提示：与「托管中」同一状态行但独立成段；trigger 缺失时走通用文案，
  // 不渲染空括号。turn_id / wake_reasons / 时间戳保留在归约数据里（诊断可见）。
  const resumedLabel = resumedNotice
    ? resumedNotice.trigger
      ? (t("composer.parkedTurn.resumed", { trigger: resumedNotice.trigger }) as string)
      : (t("composer.parkedTurn.resumedFallback") as string)
    : "";

  return (
    <div
      className={cn(
        "mb-2 flex flex-wrap items-center gap-x-2 gap-y-1 px-0.5 app-text-10 tracking-[0.08em] text-muted-foreground",
        className,
      )}
      data-parked={parkedTurnSnapshot ? "true" : "false"}
      data-resumed={resumedNotice ? "true" : "false"}
      data-testid="session-mode-banner"
    >
      {planActive ? (
        <>
          {planStatusLabel ? <Badge>{planStatusLabel}</Badge> : null}
          {plan?.plan_path ? (
            <span className="truncate" title={plan.plan_path}>
              {plan.plan_path}
            </span>
          ) : null}
          {hintLeaf ? (
            <span data-testid="session-mode-hint">
              {t(`composer.modeBanner.hint.${hintLeaf}` as never) as string}
            </span>
          ) : null}
        </>
      ) : null}
      {parkedTurnSnapshot ? (
        <span
          className="inline-flex items-center gap-1 text-analytics-warning"
          data-testid="session-parked-turn"
          role="status"
        >
          <HourglassIcon aria-hidden="true" className="size-3.5 shrink-0" />
          {parkedLabel}
        </span>
      ) : null}
      {resumedNotice ? (
        <span
          className="inline-flex items-center gap-1 text-accent-teal"
          data-testid="session-resumed-notice"
          role="status"
        >
          <RotateCcwIcon aria-hidden="true" className="size-3.5 shrink-0" />
          {resumedLabel}
        </span>
      ) : null}
    </div>
  );
}
