import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { CacheRequestRecord } from "@/types/runtime";
import { ChevronLeftIcon, ChevronRightIcon, EyeIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { formatNumber, formatPercent, formatTimestamp, shortID } from "./format";
import {
  requestCacheRatio,
  requestHasReportedCache,
  requestInputTokens,
  requestModel,
  requestOutputTokens,
  requestProvider,
  requestRecordSuccess,
  requestSessionID,
  requestTablePageSize,
  requestTotalTokens,
} from "./request-analysis-data";
import { cacheStatusLabel } from "./request-labels";
import { RequestDetailPanel } from "./request-detail-panel";

export function RequestTable({
  records,
  total,
  matchedTotal,
  page,
  pageCount,
  onPage,
}: {
  records: CacheRequestRecord[];
  total: number;
  matchedTotal: number;
  page: number;
  pageCount: number;
  onPage: (page: number) => void;
}) {
  const { t } = useTranslation("usageAnalytics");
  const [selectedRecord, setSelectedRecord] = useState<CacheRequestRecord | null>(null);
  const from = total === 0 ? 0 : page * requestTablePageSize + 1;
  const to = Math.min(total, (page + 1) * requestTablePageSize);

  return (
    <article className="mt-2 min-w-0 rounded-panel border border-border bg-surface-softer">
      <div className="flex flex-col gap-2 px-3 py-2 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            {t("requestAnalysis.table.title")}
          </h3>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t("requestAnalysis.table.subtitle", {
              loaded: formatNumber(total),
              matched: formatNumber(matchedTotal),
            })}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-xs tabular-nums text-muted-foreground">
            {t("requestAnalysis.table.pageRange", {
              from: formatNumber(from),
              to: formatNumber(to),
              total: formatNumber(total),
            })}
          </span>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 w-7 p-0"
            disabled={page <= 0}
            aria-label={t("requestAnalysis.table.previous")}
            title={t("requestAnalysis.table.previous")}
            onClick={() => onPage(page - 1)}
          >
            <ChevronLeftIcon size={14} aria-hidden="true" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 w-7 p-0"
            disabled={page >= pageCount - 1}
            aria-label={t("requestAnalysis.table.next")}
            title={t("requestAnalysis.table.next")}
            onClick={() => onPage(page + 1)}
          >
            <ChevronRightIcon size={14} aria-hidden="true" />
          </Button>
        </div>
      </div>
      <div className="overflow-x-auto border-t border-border">
        <table className="w-full min-w-[1080px] text-xs">
          <caption className="sr-only">{t("requestAnalysis.table.title")}</caption>
          <thead className="bg-surface-softer text-left text-muted-foreground">
            <tr>
              <th scope="col" className="px-3 py-2 font-medium">
                {t("requestAnalysis.table.columns.time")}
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                {t("requestAnalysis.table.columns.request")}
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                {t("requestAnalysis.table.columns.route")}
              </th>
              <th scope="col" className="px-3 py-2 text-right font-medium">
                {t("requestAnalysis.table.columns.tokens")}
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                {t("requestAnalysis.table.columns.cache")}
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                {t("requestAnalysis.table.columns.outcome")}
              </th>
            </tr>
          </thead>
          <tbody>
            {records.map((record) => {
              const sessionID = requestSessionID(record);
              const provider = requestProvider(record) || t("requestAnalysis.unknown");
              const model = requestModel(record) || t("requestAnalysis.unknown");
              const success = requestRecordSuccess(record);
              const ratio = requestCacheRatio(record);
              return (
                <tr key={record.llm_request_id} className="border-t border-border align-top">
                  <td className="whitespace-nowrap px-3 py-2 text-muted-foreground">
                    {formatTimestamp(record.started_at)}
                  </td>
                  <td className="px-3 py-2">
                    <div
                      className="max-w-52 truncate font-mono text-xs"
                      title={record.llm_request_id}
                    >
                      {shortID(record.llm_request_id)}
                    </div>
                    {sessionID ? (
                      <Link
                        className="mt-0.5 block max-w-52 truncate text-xs text-accent-primary underline-offset-2 hover:underline"
                        to={`/usage/sessions/${encodeURIComponent(sessionID)}`}
                        aria-label={t("requestAnalysis.table.openSession", { id: sessionID })}
                        title={sessionID}
                      >
                        {shortID(sessionID)}
                      </Link>
                    ) : (
                      <span className="mt-0.5 block text-xs text-muted-foreground">
                        {t("requestAnalysis.unknown")}
                      </span>
                    )}
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      className="mt-1 h-6 gap-1 px-0 text-xs"
                      aria-label={t("requestAnalysis.details.open", {
                        id: record.llm_request_id,
                      })}
                      onClick={() => setSelectedRecord(record)}
                    >
                      <EyeIcon size={12} aria-hidden="true" />
                      {t("requestAnalysis.details.openLabel")}
                    </Button>
                  </td>
                  <td className="px-3 py-2">
                    <div className="font-medium">{provider}</div>
                    <div className="text-xs text-muted-foreground">{model}</div>
                  </td>
                  <td className="whitespace-nowrap px-3 py-2 text-right tabular-nums">
                    {formatNumber(requestInputTokens(record))} /{" "}
                    {formatNumber(requestOutputTokens(record))} /{" "}
                    <span className="font-medium">
                      {formatNumber(requestTotalTokens(record))}
                    </span>
                  </td>
                  <td className="whitespace-nowrap px-3 py-2">
                    <Badge className={cacheRatioTone(ratio)}>
                      {ratio === null ? t("requestAnalysis.cacheStatus.not_reported") : formatPercent(ratio)}
                    </Badge>
                    <div className="mt-1 text-xs text-muted-foreground">
                      {cacheStatusLabel(t, record.cache_status)}
                      {requestHasReportedCache(record)
                        ? ` · ${formatNumber(record.usage?.cache_read_tokens ?? 0)}`
                        : ""}
                    </div>
                  </td>
                  <td className="px-3 py-2">
                    <Badge
                      className={cn(
                        success
                          ? "border-analytics-success-border bg-analytics-success-soft text-analytics-success"
                          : "border-analytics-danger-border bg-analytics-danger-soft text-analytics-danger",
                      )}
                    >
                      {success
                        ? t("requestAnalysis.table.success")
                        : t("requestAnalysis.table.failure")}
                    </Badge>
                    <div className="mt-1 text-xs text-muted-foreground">
                      {record.status || t("requestAnalysis.unknown")}
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {selectedRecord ? (
        <RequestDetailPanel
          record={selectedRecord}
          onClose={() => setSelectedRecord(null)}
        />
      ) : null}
    </article>
  );
}

function cacheRatioTone(ratio: number | null): string {
  if (ratio === null) {
    return "border-border bg-surface-soft text-muted-foreground";
  }
  if (ratio < 0.5) {
    return "border-analytics-warning-border bg-analytics-warning-soft text-analytics-warning";
  }
  return "border-analytics-success-border bg-analytics-success-soft text-analytics-success";
}
