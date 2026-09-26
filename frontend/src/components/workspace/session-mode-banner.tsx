// 会话常驻状态标识（§4.6 模式 + §6.8 托管挂起）：聊天区顶部常显当前权限模式，
// plan 模式下补计划上下文；托管挂起期间并列显示「托管中：…」。
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

import { HourglassIcon, ScrollTextIcon, ShieldAlertIcon, ShieldIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import {
  sessionModeBannerHintLeaf,
  sessionModeBannerLabelKey,
  sessionModeBannerTone,
  sessionModeBannerToneClass,
} from "@/components/workspace/session-mode-banner-shared";
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
  const mode = plan?.permission_mode?.trim() ?? "";

  // 无会话 / 既无模式快照也无挂起态：不占位（模式控件仍在下一条输入卡里）。
  if (!sessionId || (!mode && !parkedTurn)) {
    return null;
  }

  const tone = sessionModeBannerTone(mode);
  const labelKey = sessionModeBannerLabelKey(mode);
  const modeLabel = labelKey ? (t(labelKey as never) as string) : mode;
  const hintLeaf = sessionModeBannerHintLeaf(plan);
  const planActive = Boolean(plan?.active);
  const TitleIcon = tone === "danger" ? ShieldAlertIcon : tone === "plan" ? ScrollTextIcon : ShieldIcon;

  const parkedTurnSnapshot = parkedTurn?.turn ?? null;
  const parkedTaskCounts = parkedTurn?.taskCounts ?? null;
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

  return (
    <div
      className={cn(
        "mb-2 flex flex-wrap items-center gap-x-2 gap-y-1 px-0.5 app-text-10 tracking-[0.08em] text-muted-foreground",
        className,
      )}
      data-mode={mode}
      data-parked={parkedTurnSnapshot ? "true" : "false"}
      data-testid="session-mode-banner"
      data-tone={tone}
    >
      {mode ? (
        <>
          <TitleIcon aria-hidden="true" className="size-3.5 shrink-0" />
          <span className="uppercase">{t("composer.modeBanner.title")}</span>
          <Badge className={sessionModeBannerToneClass(tone)}>{modeLabel}</Badge>
          {planActive && planStatusLabel ? <Badge>{planStatusLabel}</Badge> : null}
          {planActive && plan?.plan_path ? (
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
    </div>
  );
}
