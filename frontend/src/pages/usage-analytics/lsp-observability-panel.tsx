// 跨会话 LSP 观测面板（方案 §5.5，fleet 级视图）：
//   * 数据来源：观测面 `/api/runtime/observe/v1/events`（event_type 精确匹配，
//     由 api/runtime/lsp.ts 按类型并发拉取并合并）；
//   * 展示**事实与窗口计数**（服务器最新状态 / 请求 outcome 与原因计数 / 最近事件），
//     不重算比率指标——覆盖率、fallback、闭环率等口径单源于 §3.3（TUI 与 web 会话内面板）；
//   * 诚实降级：观测面不可用（403/404/405）→ "未授权或观测面未启用"；
//     其他错误 → role="alert" + 原因；
//     窗口内无事件 → "暂无 LSP 事件"，不渲染 0 指标冒充健康。

import type { LspBaselineResponse, LspObserveEvent } from "@/api/runtime/lsp";
import {
  fetchLspObserveFeed,
  getLspBaseline,
  LspObserveUnavailableError,
} from "@/api/runtime/lsp";
import { RuntimeApiError, readErrorEnvelope } from "@/api/runtime/shared";
import { Button } from "@/components/ui/button";
import { RefreshCwIcon } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { formatNumber } from "./format";
import {
  coldFastFailCount,
  eventDurationLabel,
  eventOutcomeLabel,
  eventSessionLabel,
  latestServerStates,
  recentLspEvents,
  requestOutcomeCounts,
  requestReasonCounts,
  shortEventType,
} from "./lsp-observability-shared";

const FEED_LIMIT = 100;
const RECENT_LIMIT = 20;

function formatEventTime(value: string): string {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return value;
  }
  return parsed.toLocaleTimeString();
}

/**
 * 错误降级文案：观测面不可用统一走"未授权或未启用"，其余用后端消息，
 * 最后兜底异常文本。
 *
 * 关键修正：后端在 observe.enabled=false 时**根本不注册** /observe/v1/* 路由
 * （runtimeapi/handler.go 的 ensureObserveService 守卫），因此信号是 **404**
 * 而非 403。原先只特判 403，导致用户直接看到
 * "runtime request failed with status 404" 这种无法据以排查的原始文案。
 * 现在由 api/runtime/lsp.ts 把 403/404/405 统一归一为 LspObserveUnavailableError。
 */
function describeLspError(err: unknown, t: (key: "lspPanel.forbidden") => string): string {
  if (err instanceof LspObserveUnavailableError || (err instanceof RuntimeApiError && err.status === 403)) {
    return t("lspPanel.forbidden");
  }
  if (err instanceof RuntimeApiError) {
    return readErrorEnvelope(err.payload).message || err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

export function LspObservabilityPanel({ adminToken }: { adminToken?: string }) {
  const { t } = useTranslation("usageAnalytics");
  const [events, setEvents] = useState<LspObserveEvent[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [baseline, setBaseline] = useState<LspBaselineResponse | null>(null);
  const [baselineError, setBaselineError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    setBaselineError(null);
    // 两个数据面独立降级：事件流不可用时基线仍可展示，反之亦然（不互相冒充）。
    const [feedResult, baselineResult] = await Promise.allSettled([
      fetchLspObserveFeed({ limit: FEED_LIMIT, adminToken }),
      getLspBaseline({ adminToken }),
    ]);
    if (feedResult.status === "fulfilled") {
      setEvents(feedResult.value.events);
      setLoaded(true);
    } else {
      setError(describeLspError(feedResult.reason, t));
    }
    if (baselineResult.status === "fulfilled") {
      setBaseline(baselineResult.value);
    } else {
      setBaselineError(describeLspError(baselineResult.reason, t));
    }
    setLoading(false);
  }, [adminToken, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const states = useMemo(() => latestServerStates(events), [events]);
  const outcomes = useMemo(() => requestOutcomeCounts(events), [events]);
  const reasons = useMemo(() => requestReasonCounts(events), [events]);
  const coldFastFails = useMemo(() => coldFastFailCount(events), [events]);
  const recent = useMemo(() => recentLspEvents(events, RECENT_LIMIT), [events]);

  return (
    <section className="flex min-w-0 flex-col gap-4" aria-label={t("lspPanel.title")}>
      <div className="surface-panel flex flex-wrap items-start justify-between gap-3 rounded-panel-lg p-4">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold">{t("lspPanel.title")}</h2>
          <p className="mt-1 text-xs text-muted-foreground">{t("lspPanel.subtitle")}</p>
        </div>
        <Button
          type="button"
          variant="secondary"
          size="sm"
          onClick={() => void load()}
          disabled={loading}
        >
          <RefreshCwIcon size={14} aria-hidden="true" />
          {loading ? t("lspPanel.loading") : t("lspPanel.refresh")}
        </Button>
      </div>

      {baseline ? (
        <div className="surface-panel rounded-panel-lg p-4">
          <h3 className="text-sm font-semibold">{t("lspPanel.baseline")}</h3>
          <p className="mt-1 text-xs text-muted-foreground">
            {t("lspPanel.baselineNote")} · {t("lspPanel.baselineWindow")}: {baseline.window} ·{" "}
            {/* 事实源由后端声明（source），不由 UI 猜。数据面读分析库时不扫日志，
                scan.* 恒为 0 —— 渲染成"扫描 0 文件/0 损坏"会被读成"数据有问题"，
                所以那条路径下根本不提扫描量。 */}
            {baseline.source === "chat_logs" ? (
              <>
                {t("lspPanel.baselineScan")}: {formatNumber(baseline.scan.files)} /{" "}
                {formatNumber(baseline.scan.malformed)}
              </>
            ) : (
              t("lspPanel.baselineSourceDb")
            )}
            {baseline.cache?.hit ? (
              <>
                {" · "}
                {t("lspPanel.baselineCached", {
                  age: baseline.cache.age_seconds ?? 0,
                  ttl: baseline.cache.ttl_seconds ?? 0,
                })}
              </>
            ) : null}
            {/* 增量索引只在真的扫日志时才有意义。走分析库时后端不发这些字段，
                缺席是预期的 —— 此时说"未使用索引"是误导（真相是不扫日志）。 */}
            {baseline.source === "chat_logs" ? (
              <>
                {" · "}
                {baseline.scan.reused_files !== undefined ||
                baseline.scan.delta_files !== undefined
                  ? t("lspPanel.baselineIndexed", {
                      reused: formatNumber(baseline.scan.reused_files ?? 0),
                      delta: formatNumber(baseline.scan.delta_files ?? 0),
                    })
                  : t("lspPanel.baselineNoIndex")}
              </>
            ) : null}
          </p>
          <div className="mt-2 overflow-x-auto">
            <table className="w-full min-w-0 text-left text-xs">
              <thead className="text-muted-foreground">
                <tr>
                  <th className="py-1 pr-3 font-medium">{t("lspPanel.metric")}</th>
                  <th className="py-1 pr-3 font-medium">{t("lspPanel.value")}</th>
                  <th className="py-1 pr-3 font-medium">{t("lspPanel.samples")}</th>
                  <th className="py-1 pr-3 font-medium">{t("lspPanel.conclusion")}</th>
                </tr>
              </thead>
              <tbody>
                {baseline.rows.map((row) => (
                  <tr key={row.metric} className="border-t border-border/60">
                    <td className="py-1 pr-3 font-medium">{row.metric}</td>
                    <td className="py-1 pr-3 font-mono">{row.value}</td>
                    <td className="py-1 pr-3 text-muted-foreground">{row.samples || "—"}</td>
                    <td className="py-1 pr-3 text-muted-foreground">{row.conclusion}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      ) : null}

      {baselineError ? (
        <div role="alert" className="surface-panel rounded-panel-lg p-4 text-sm text-destructive">
          {t("lspPanel.baselineUnavailable")}: {baselineError}
        </div>
      ) : null}

      {error ? (
        <div role="alert" className="surface-panel rounded-panel-lg p-4 text-sm text-destructive">
          {t("lspPanel.error")}: {error}
        </div>
      ) : null}

      {!error && loaded && events.length === 0 ? (
        <div role="status" className="surface-panel rounded-panel-lg p-6 text-center text-sm text-muted-foreground">
          {t("lspPanel.empty")}
        </div>
      ) : null}

      {!error && events.length > 0 ? (
        <>
          <div className="surface-panel rounded-panel-lg p-4">
            <h3 className="text-sm font-semibold">{t("lspPanel.servers")}</h3>
            {states.length === 0 ? (
              <p className="mt-2 text-xs text-muted-foreground">{t("lspPanel.noServerStates")}</p>
            ) : (
              <div className="mt-2 overflow-x-auto">
                <table className="w-full min-w-0 text-left text-xs">
                  <thead className="text-muted-foreground">
                    <tr>
                      <th className="py-1 pr-3 font-medium">{t("lspPanel.server")}</th>
                      <th className="py-1 pr-3 font-medium">{t("lspPanel.state")}</th>
                      <th className="py-1 pr-3 font-medium">{t("lspPanel.reason")}</th>
                      <th className="py-1 pr-3 font-medium">{t("lspPanel.firstPublish")}</th>
                      <th className="py-1 pr-3 font-medium">{t("lspPanel.session")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {states.map((row) => (
                      <tr key={row.key} className="border-t border-border/60">
                        <td className="py-1 pr-3 font-medium">{row.server}</td>
                        <td className="py-1 pr-3">{row.state || "—"}</td>
                        <td className="py-1 pr-3">{row.reasonCategory || row.reason || "—"}</td>
                        <td className="py-1 pr-3">
                          {row.firstPublishMs === null ? "—" : `${formatNumber(row.firstPublishMs)} ms`}
                        </td>
                        <td className="py-1 pr-3 font-mono">{row.sessionId || "—"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          <div className="surface-panel rounded-panel-lg p-4">
            <h3 className="text-sm font-semibold">{t("lspPanel.requests")}</h3>
            <p className="mt-1 text-xs text-muted-foreground">{t("lspPanel.countsNote")}</p>
            <div className="mt-2 flex flex-wrap gap-2 text-xs">
              {outcomes.length === 0 ? (
                <span className="text-muted-foreground">{t("lspPanel.noRequests")}</span>
              ) : (
                outcomes.map((row) => (
                  <span key={row.key} className="rounded-control border border-border/60 px-2 py-1">
                    {row.key}: {formatNumber(row.count)}
                  </span>
                ))
              )}
            </div>
            {reasons.length > 0 ? (
              <div className="mt-2 flex flex-wrap gap-2 text-xs text-muted-foreground">
                {reasons.map((row) => (
                  <span key={row.key} className="rounded-control border border-border/60 px-2 py-1">
                    {t("lspPanel.reasons")} {row.key}: {formatNumber(row.count)}
                  </span>
                ))}
              </div>
            ) : null}
            <p className="mt-2 text-xs text-muted-foreground">
              {t("lspPanel.coldFastFail")}: {formatNumber(coldFastFails)}
            </p>
          </div>

          <div className="surface-panel rounded-panel-lg p-4">
            <h3 className="text-sm font-semibold">{t("lspPanel.recent")}</h3>
            <div className="mt-2 overflow-x-auto">
              <table className="w-full min-w-0 text-left text-xs">
                <thead className="text-muted-foreground">
                  <tr>
                    <th className="py-1 pr-3 font-medium">{t("lspPanel.time")}</th>
                    <th className="py-1 pr-3 font-medium">{t("lspPanel.type")}</th>
                    <th className="py-1 pr-3 font-medium">{t("lspPanel.outcome")}</th>
                    <th className="py-1 pr-3 font-medium">{t("lspPanel.duration")}</th>
                    <th className="py-1 pr-3 font-medium">{t("lspPanel.session")}</th>
                  </tr>
                </thead>
                <tbody>
                  {recent.map((event) => (
                    <tr key={event.observation_seq} className="border-t border-border/60">
                      <td className="py-1 pr-3">{formatEventTime(event.timestamp)}</td>
                      <td className="py-1 pr-3 font-mono">{shortEventType(event.type)}</td>
                      <td className="py-1 pr-3">{eventOutcomeLabel(event)}</td>
                      <td className="py-1 pr-3">{eventDurationLabel(event)}</td>
                      <td className="py-1 pr-3 font-mono">{eventSessionLabel(event)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </>
      ) : null}
    </section>
  );
}
