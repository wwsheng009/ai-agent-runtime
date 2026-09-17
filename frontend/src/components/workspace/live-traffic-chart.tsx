// 「流量波动图」：每秒吞吐（bytes/s）的迷你柱状图小组件。
//
// 它补的是计数网格看不出来的那一维——**时间形状**。累计字节只说明「收过多少」，
// 看不出「现在还收不收」「是持续在推还是刚才突了一下」「保活还在不在」。柱高按
// 秒对齐后，突发是并排高柱、静默是中间空缺，形态本身就是结论。
//
// 为什么是柱子而不是折线：观测到的是**分片**流量，密度极不均匀（突发时一秒上千
// 片、空闲时十几秒一个 keepalive）。折线在突发时会把横轴压成一撮，在空闲时又
// 拉成一条贴地的线；按秒聚合的柱子两种极端都读得出来。
//
// 颜色一律走语义 token（accent-secondary / border / muted-foreground），不写字面
// 色值——与「会话详情」其它区块同一口径，主题切换自动跟随。

import {
  formatLiveBytes,
  formatLiveClock,
  type LiveTrafficSeries,
} from "@/lib/live-diagnostics/derive";
import { SESSION_DETAIL_SECTION_LABEL_CLASS } from "@/components/workspace/session-detail-panel-shared";
import { cn } from "@/lib/utils";

/** 非零柱的最小可见高度：1 字节的 keepalive 也要看得见「它来过」。 */
const BAR_MIN_PERCENT = 6;

export type LiveTrafficChartProps = {
  series: LiveTrafficSeries;
  /** 已本地化文案：标题 / 空态 / 峰值前缀 / 合计前缀 / 进行中标记 / 轴两端。 */
  title: string;
  emptyLabel: string;
  peakLabel: string;
  totalLabel: string;
  currentLabel: string;
  windowStartLabel: string;
  windowNowLabel: string;
  testId: string;
};

export function LiveTrafficChart({
  series,
  title,
  emptyLabel,
  peakLabel,
  totalLabel,
  currentLabel,
  windowStartLabel,
  windowNowLabel,
  testId,
}: LiveTrafficChartProps) {
  const { buckets, max, total } = series;
  const hasTraffic = total > 0;
  // 峰值与合计都是「1s 桶」口径，因此峰值单位写作 /s，和标题的「流量」对齐。
  const summary = hasTraffic
    ? `${peakLabel} ${formatLiveBytes(max)}/s · ${totalLabel} ${formatLiveBytes(total)}`
    : "";

  return (
    <div
      className="grid gap-1"
      data-testid={testId}
      data-traffic-max={max}
      data-traffic-total={total}
    >
      <div className="flex min-w-0 items-baseline justify-between gap-2">
        <span className={SESSION_DETAIL_SECTION_LABEL_CLASS}>{title}</span>
        {hasTraffic ? (
          <span
            className="min-w-0 truncate app-text-10 text-muted-foreground"
            title={summary}
          >
            {summary}
          </span>
        ) : null}
      </div>

      {hasTraffic ? (
        <>
          <div
            aria-label={`${title}：${summary}`}
            className="flex h-6 items-end gap-px border-b border-border"
            data-testid={`${testId}-bars`}
            role="img"
          >
            {buckets.map((bucket, index) => {
              const isCurrent = index === buckets.length - 1;
              const percent =
                bucket.bytes <= 0
                  ? 0
                  : Math.max(
                      BAR_MIN_PERCENT,
                      Math.round((bucket.bytes / max) * 100),
                    );
              return (
                <span
                  className={cn(
                    "min-w-0 flex-1 rounded-[1px]",
                    bucket.bytes > 0 ? "bg-accent-secondary" : "bg-transparent",
                    // 当前秒还没走完，柱子天然偏矮：压暗一档，避免被读成「掉下来了」。
                    isCurrent && bucket.bytes > 0 ? "opacity-60" : "",
                  )}
                  data-bytes={bucket.bytes}
                  key={bucket.at}
                  style={{ height: `${percent}%` }}
                  title={
                    bucket.bytes > 0
                      ? [
                          formatLiveClock(bucket.at),
                          formatLiveBytes(bucket.bytes),
                          isCurrent ? currentLabel : "",
                        ]
                          .filter(Boolean)
                          .join(" · ")
                      : undefined
                  }
                />
              );
            })}
          </div>
          <div className="flex items-center justify-between app-text-10 text-muted-foreground">
            <span>{windowStartLabel}</span>
            <span>{windowNowLabel}</span>
          </div>
        </>
      ) : (
        <span className="text-xs text-muted-foreground">{emptyLabel}</span>
      )}
    </div>
  );
}
