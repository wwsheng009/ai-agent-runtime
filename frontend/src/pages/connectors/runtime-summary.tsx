// 连接器运行快照（累计计数 + 最近错误）的原子展示组件：从 connectors-page.tsx
// 提取（P0-2 行数门禁）。runtime 缺失（旧后端）时不渲染。
import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";
import type { ConnectorRuntimeView } from "@/types/runtime";

export function ConnectorRuntimeSummary({
  runtime,
}: {
  runtime?: ConnectorRuntimeView;
}) {
  const { t } = useTranslation("connectors");
  if (!runtime) {
    return null;
  }
  return (
    <>
      <span className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
        <span>{t("stats.inbound", { value: String(runtime.stats.inbound) })}</span>
        <span>·</span>
        <span>{t("stats.outbound", { value: String(runtime.stats.outbound) })}</span>
        <span>·</span>
        <span
          className={cn(
            runtime.stats.errors > 0 ? "font-medium text-analytics-danger" : undefined,
          )}
        >
          {t("stats.errors", { value: String(runtime.stats.errors) })}
        </span>
      </span>
      {runtime.stats.last_error ? (
        <details className="text-xs text-muted-foreground">
          <summary className="cursor-pointer select-none">{t("page.lastError")}</summary>
          <p className="mt-1 whitespace-pre-wrap break-words font-mono text-[11px] text-analytics-danger">
            {runtime.stats.last_error}
            {runtime.stats.last_error_at ? ` · ${runtime.stats.last_error_at}` : ""}
          </p>
        </details>
      ) : null}
    </>
  );
}