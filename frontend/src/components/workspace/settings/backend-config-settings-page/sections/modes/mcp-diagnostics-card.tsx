// MCP 管理面板「配置来源」诊断卡：
// 展示生效文件 + 命中来源 + 候选存在性 + 汇总统计，解释"为什么某个 server 没加载"
// （例如 runtime-server 按会话工作区锚定后解析到了另一个 mcp.yaml）。
//
// 纯展示组件：数据由 McpModeSection 的 listRuntimeMcps 结果透传，自身不发请求。

import { FileCogIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";
import type {
  RuntimeMcpConfigDiagnostics,
  RuntimeMcpListSummary,
} from "@/types/runtime";

/** 解析来源 → 文案 key；未知来源原样回显（不臆造中文名）。 */
const MCP_CONFIG_SOURCE_KEYS: Record<string, string> = {
  explicit: "mcp.diagnostics.sources.explicit",
  local: "mcp.diagnostics.sources.local",
  project: "mcp.diagnostics.sources.project",
  user: "mcp.diagnostics.sources.user",
  upward: "mcp.diagnostics.sources.upward",
  executable: "mcp.diagnostics.sources.executable",
  "user-fallback": "mcp.diagnostics.sources.user-fallback",
  default: "mcp.diagnostics.sources.default",
};

function mcpConfigSourceLabel(
  source: string,
  translate: (key: string) => string,
) {
  const key = MCP_CONFIG_SOURCE_KEYS[source];
  return key ? translate(key) : source;
}

export type McpDiagnosticsCardProps = {
  diagnostics: RuntimeMcpConfigDiagnostics;
  summary: RuntimeMcpListSummary | null;
};

export function McpDiagnosticsCard({
  diagnostics,
  summary,
}: McpDiagnosticsCardProps) {
  const { t } = useTranslation("runtimeConfig");
  const translate = (key: string) => t(key as never) as string;

  return (
    <div
      className="rounded-panel border border-border bg-surface-softer p-3"
      data-testid="mcp-config-diagnostics"
    >
      <div className="flex flex-wrap items-center gap-2">
        <FileCogIcon className="text-accent-primary" size={13} />
        <span className="text-xs font-semibold text-foreground">
          {t("mcp.diagnostics.title")}
        </span>
        {diagnostics.source ? (
          <span className="inline-flex items-center rounded-full border border-border bg-surface-soft px-1.5 py-0.5 app-text-10 text-muted-foreground">
            {mcpConfigSourceLabel(diagnostics.source, translate)}
          </span>
        ) : null}
        <span className="app-text-11 text-muted-foreground">
          {t(
            diagnostics.manager_loaded
              ? "mcp.diagnostics.managerLoaded"
              : "mcp.diagnostics.managerMissing",
          )}
        </span>
        {summary ? (
          <span
            className="app-text-11 text-muted-foreground"
            data-testid="mcp-config-summary"
          >
            {t("mcp.diagnostics.summary", {
              total: String(summary.total),
              enabled: String(summary.enabled),
              connected: String(summary.connected),
              tools: String(summary.tools),
            })}
          </span>
        ) : null}
      </div>
      <div className="mt-1.5 flex items-start gap-2">
        <span className="shrink-0 app-text-11 text-muted-foreground">
          {t("mcp.diagnostics.effectivePath")}
        </span>
        <code
          className="min-w-0 break-all font-mono app-text-11 text-foreground"
          data-testid="mcp-config-path"
        >
          {diagnostics.path}
        </code>
      </div>
      {diagnostics.candidates && diagnostics.candidates.length > 0 ? (
        <div className="mt-2 border-t border-border/60 pt-2">
          <div className="app-text-11 text-muted-foreground">
            {t("mcp.diagnostics.candidates")}
          </div>
          <ul className="mt-1 grid gap-1">
            {diagnostics.candidates.map((candidate, index) => (
              <li
                className="flex items-start gap-2"
                key={`${candidate.source}-${candidate.path}-${index}`}
              >
                <span
                  className={cn(
                    "mt-1 size-1.5 shrink-0 rounded-full",
                    candidate.exists ? "bg-accent-primary" : "bg-border",
                  )}
                />
                <span className="shrink-0 app-text-11 text-muted-foreground">
                  {mcpConfigSourceLabel(candidate.source, translate)}
                </span>
                <code className="min-w-0 break-all font-mono app-text-11 text-foreground">
                  {candidate.path}
                </code>
                <span className="shrink-0 app-text-11 text-muted-foreground">
                  {candidate.exists
                    ? t("mcp.diagnostics.exists")
                    : t("mcp.diagnostics.missing")}
                </span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}
