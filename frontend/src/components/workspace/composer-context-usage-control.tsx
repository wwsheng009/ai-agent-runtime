// composer 发送按钮左侧的「上下文进度」控件：圆形进度环 + 点击展开的会话上下文面板。
//
// 分工：
//   * 环：用最近一次带用量上报的 LLM 请求反推的占用率画进度（见 composer-context-usage-shared），
//     unknown 时退化为中性环并显示「—」，不假装有百分比；
//   * 面板：把该快照展开成明细（已用/窗口/剩余/预算/观测时间）；手动 compact 见
//     composer-context-compact-section（它自己持状态，关面板即卸载归零）；
//   * 压缩确实替换了历史时，先把环切到响应里的 token，等新用量落库（引用变化）再交还实时数据。
//
// 与 composer 的耦合方式与权限控件一致：composer 只保留一个 ReactNode 座位，
// 会话身份 / 事件计数由宿主注入，控件自己负责取数与提交。

import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { GaugeIcon, RefreshCwIcon, XIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { type SessionCompactOutcome } from "@/api/runtime";
import { ComposerContextCompactSection } from "@/components/workspace/composer-context-compact-section";
import {
  formatContextPercent,
  formatContextPercentCompact,
  formatContextTokens,
  resolveContextUsage,
  resolveContextUsageFromCompact,
} from "@/components/workspace/composer-context-usage-shared";
import { ContextUsageRing } from "@/components/workspace/composer-context-usage-ring";
import { formatUsageTimestamp } from "@/components/workspace/session-usage-panel-shared";
import { Button } from "@/components/ui/button";
import {
  resolvePopoverPosition,
  type PopoverPosition,
  type PopoverPositionOptions,
} from "@/components/ui/popover-position";
import { useSessionUsage } from "@/hooks/workspace/use-session-usage";
import { cn } from "@/lib/utils";
import type { AnalyticsSessionUsageDetail } from "@/types/runtime";

export type ComposerContextUsageControlProps = {
  className?: string;
  /** 响应中：禁止手动 compact（后端会话忙会直接拒绝），并让用量低频轮询。 */
  isResponding?: boolean;
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
  /** 会话 id；空 = 草稿会话，只显示空态、不发请求。 */
  sessionId?: string;
};

/** 面板贴着触发按钮上沿弹出：min/max 高度保证长内容不被视口裁掉也不塌成一条缝。 */
const PANEL_POSITION_OPTIONS: PopoverPositionOptions = {
  align: "end",
  gap: 8,
  maxHeight: 420,
  minHeight: 220,
  minWidth: 300,
  side: "top",
};

/** 手动压缩后的覆盖显示：只在「压缩那一刻的用量」还没被新数据替换时生效。 */
type CompactOverride = {
  outcome: SessionCompactOutcome;
  usageAtCompact: AnalyticsSessionUsageDetail | null;
};

export function ComposerContextUsageControl({
  className,
  isResponding = false,
  lastRuntimeEventType,
  runtimeEventCount,
  sessionId,
}: ComposerContextUsageControlProps) {
  const { t } = useTranslation("workspace");
  const normalizedSessionId = sessionId?.trim() ?? "";
  const { error, loading, refresh, usage } = useSessionUsage({
    live: isResponding,
    lastRuntimeEventType,
    runtimeEventCount,
    sessionId: normalizedSessionId,
  });

  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<PopoverPosition | null>(null);
  const [compactOverride, setCompactOverride] = useState<CompactOverride | null>(null);

  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const panelRef = useRef<HTMLDivElement | null>(null);

  const liveSnapshot = resolveContextUsage(usage);
  // 覆盖是纯派生：等新用量落库（usage 换了对象引用）就让实时数据接管显示，
  // 因此不需要用 effect 去清状态（effect 里 setState 会触发级联渲染）。
  const compactSnapshot =
    compactOverride && usage === compactOverride.usageAtCompact
      ? resolveContextUsageFromCompact(compactOverride.outcome)
      : null;
  const snapshot = compactSnapshot ?? liveSnapshot;
  const utilization = snapshot?.utilization ?? null;
  const level = snapshot?.level ?? "unknown";

  const closePanel = useCallback(() => {
    setOpen(false);
    // 关闭即丢弃覆盖：再次打开时看到的应当是当前状态，而不是上一次的旧结论。
    setCompactOverride(null);
  }, []);

  const panelPositionFromTrigger = useCallback((): PopoverPosition | null => {
    const rect = triggerRef.current?.getBoundingClientRect();
    return rect ? resolvePopoverPosition(rect, PANEL_POSITION_OPTIONS) : null;
  }, []);

  useEffect(() => {
    if (!open) {
      return;
    }

    function reposition() {
      const next = panelPositionFromTrigger();
      if (next) {
        setPosition(next);
      }
    }

    function handlePointerDown(event: MouseEvent) {
      const target = event.target as Node | null;
      if (
        target &&
        !panelRef.current?.contains(target) &&
        !triggerRef.current?.contains(target)
      ) {
        closePanel();
      }
    }

    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.preventDefault();
        closePanel();
        triggerRef.current?.focus();
      }
    }

    reposition();
    window.addEventListener("resize", reposition);
    document.addEventListener("mousedown", handlePointerDown);
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("resize", reposition);
      document.removeEventListener("mousedown", handlePointerDown);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [closePanel, open, panelPositionFromTrigger]);

  function togglePanel() {
    if (open) {
      closePanel();
      return;
    }
    const next = panelPositionFromTrigger();
    if (next) {
      setPosition(next);
    }
    setOpen(true);
  }

  function handleCompacted(outcome: SessionCompactOutcome) {
    setCompactOverride({ outcome, usageAtCompact: usage });
    // 压缩已改变历史：让用量面板/环重新拉取落库数据（环同时已被响应覆盖）。
    refresh();
  }

  const percentText = formatContextPercent(utilization);
  const ringPercentText = formatContextPercentCompact(utilization);
  const progress = utilization ?? 0;
  const triggerLabel = t("composer.contextUsage.triggerLabel", {
    percent: percentText,
  });
  const compactDisabledReason = !normalizedSessionId
    ? t("composer.contextUsage.compact.disabledNoSession")
    : isResponding
      ? t("composer.contextUsage.compact.disabledResponding")
      : null;

  const metrics = snapshot
    ? [
        {
          key: "used",
          label: t("composer.contextUsage.metrics.used"),
          value: `${formatContextTokens(snapshot.usedTokens)} token`,
        },
        {
          key: "window",
          label: t("composer.contextUsage.metrics.window"),
          value:
            snapshot.windowTokens > 0
              ? `${formatContextTokens(snapshot.windowTokens)} token`
              : t("composer.contextUsage.unknownValue"),
        },
        {
          key: "remaining",
          label: t("composer.contextUsage.metrics.remaining"),
          value:
            snapshot.remainingTokens === null
              ? t("composer.contextUsage.unknownValue")
              : `${formatContextTokens(snapshot.remainingTokens)} token`,
        },
        {
          key: "budget",
          label: t("composer.contextUsage.metrics.budget"),
          value:
            snapshot.budgetTokens === null
              ? t("composer.contextUsage.unknownValue")
              : `${formatContextTokens(snapshot.budgetTokens)} token`,
        },
        {
          key: "observed",
          label: t("composer.contextUsage.metrics.observed"),
          value: snapshot.observedAt
            ? formatUsageTimestamp(snapshot.observedAt)
            : t("composer.contextUsage.unknownValue"),
        },
      ]
    : [];

  return (
    <div className={cn("flex shrink-0 items-center", className)}>
      <button
        ref={triggerRef}
        type="button"
        data-testid="composer-context-usage-trigger"
        aria-label={triggerLabel}
        aria-expanded={open}
        aria-haspopup="dialog"
        title={triggerLabel}
        onClick={togglePanel}
        className="relative inline-flex size-8 shrink-0 items-center justify-center rounded-full border border-border bg-surface-soft p-0 transition-colors hover:border-border-strong"
      >
        <ContextUsageRing
          animated
          className="pointer-events-none"
          level={level}
          percentText={ringPercentText}
          progress={progress}
          size={32}
          textClassName="text-[9px] font-medium"
        />
      </button>

      {open && position
        ? createPortal(
            <div
              ref={panelRef}
              role="dialog"
              aria-label={t("composer.contextUsage.title")}
              data-testid="composer-context-usage-panel"
              style={{ position: "fixed", ...position }}
              className="z-[160] flex flex-col gap-3 overflow-y-auto rounded-card-lg border border-border bg-surface-overlay shadow-[0_10px_24px_rgba(0,0,0,0.24)] p-3.5 text-sm"
            >
              <div className="flex items-start justify-between gap-2">
                <h3 className="flex items-center gap-1.5 text-xs font-semibold text-foreground">
                  <GaugeIcon size={14} className="text-accent-primary" aria-hidden="true" />
                  {t("composer.contextUsage.title")}
                </h3>
                <div className="flex shrink-0 items-center gap-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 w-6 px-0"
                    aria-label={t("composer.contextUsage.refresh")}
                    title={t("composer.contextUsage.refresh")}
                    disabled={loading || !normalizedSessionId}
                    onClick={refresh}
                  >
                    <RefreshCwIcon size={12} className={cn(loading && "animate-spin")} />
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 w-6 px-0"
                    aria-label={t("composer.contextUsage.close")}
                    title={t("composer.contextUsage.close")}
                    onClick={closePanel}
                  >
                    <XIcon size={12} />
                  </Button>
                </div>
              </div>

              {error ? (
                <p className="text-xs leading-5 text-accent-orange" role="alert">
                  {t("composer.contextUsage.error")}
                  {": "}
                  {error}
                </p>
              ) : null}

              {snapshot ? (
                <>
                  <div className="flex items-center gap-3">
                    <ContextUsageRing
                      className="shrink-0"
                      level={level}
                      percentText={percentText}
                      progress={progress}
                      size={56}
                      textClassName="text-xs font-semibold"
                    />
                    <dl className="grid min-w-0 flex-1 grid-cols-1 gap-1">
                      {metrics.map((metric) => (
                        <div
                          key={metric.key}
                          className="flex items-baseline justify-between gap-3 text-xs"
                        >
                          <dt className="shrink-0 text-muted-foreground">{metric.label}</dt>
                          <dd className="truncate text-right tabular-nums text-foreground">
                            {metric.value}
                          </dd>
                        </div>
                      ))}
                    </dl>
                  </div>
                  <p className="text-[11px] leading-5 text-muted-foreground">
                    {t("composer.contextUsage.sourceNote", {
                      source: t(`composer.contextUsage.source.${snapshot.source}`),
                    })}
                  </p>
                </>
              ) : (
                <p className="text-xs leading-5 text-muted-foreground">
                  {loading
                    ? t("composer.contextUsage.loading")
                    : t("composer.contextUsage.empty")}
                </p>
              )}

              <ComposerContextCompactSection
                disabledReason={compactDisabledReason}
                onCompacted={handleCompacted}
                sessionId={normalizedSessionId}
              />
            </div>,
            document.body,
          )
        : null}
    </div>
  );
}
