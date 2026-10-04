// 会话 MCP「工具列表」弹窗：查看某个 server 在会话生效 manager 下的工具清单。
//
// 数据由 GET /api/runtime/sessions/{id}/runtime/mcps/{name}/tools 现取；与设置域
// 工具弹窗（全局端点 + 工具级启停）不同，这里只做**只读查看**：会话面板回答
// 「这个会话实际能用什么」，写操作（server 启停/工具级开关）留给配置面。
// 未启用 / 未连接时后端返回空数组，UI 渲染空态而不是报错；契约异常（tools 非
// 数组）由客户端抛错并展示为错误态 + 重试。外壳沿用设置域对话框约定：portal
// 挂载、Esc / 遮罩关闭、关闭后焦点回位。

import { RefreshCwIcon } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { listRuntimeSessionMcpTools } from "@/api/runtime/mcp";
import { SESSION_DETAIL_CHIP_CLASS } from "@/components/workspace/session-detail-panel-shared";
import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import { useFocusRestore } from "@/hooks/workspace/use-focus-restore";
import { cn } from "@/lib/utils";
import type {
  RuntimeMcpTool,
  RuntimeSessionMcpToolsResponse,
} from "@/types/runtime";

export type SessionMcpToolsDialogProps = {
  sessionId: string;
  name: string;
  onClose: () => void;
};

export function SessionMcpToolsDialog({
  sessionId,
  name,
  onClose,
}: SessionMcpToolsDialogProps) {
  const { t } = useTranslation("workspace");
  const [payload, setPayload] = useState<RuntimeSessionMcpToolsResponse | null>(
    null,
  );
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setIsLoading(true);
    setError(null);
    try {
      setPayload(await listRuntimeSessionMcpTools(sessionId, name));
    } catch (cause) {
      setPayload(null);
      const message = cause instanceof Error ? cause.message.trim() : "";
      setError(message || t("panels.sessionMcp.toolsLoadFailed"));
    } finally {
      setIsLoading(false);
    }
  }, [name, sessionId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  useDialogLifecycle(true, onClose);
  useFocusRestore(true);

  if (typeof document === "undefined") {
    return null;
  }

  const tools = payload?.tools ?? [];
  const scopeLabel = payload?.scope
    ? t(
        payload.scope === "workspace"
          ? "panels.sessionMcp.toolsScopeWorkspace"
          : "panels.sessionMcp.toolsScopeGlobal",
      )
    : "";

  return createPortal(
    <DialogOverlay className="z-[110]" onDismiss={onClose}>
      <DialogPanel
        aria-label={t("panels.sessionMcp.toolsAriaLabel", { name })}
        aria-modal="true"
        className="max-w-[46rem]"
        data-testid="session-mcp-tools-dialog"
        elevation="lg"
        role="dialog"
      >
        <div className="flex items-center justify-between gap-2 border-b border-border px-3 py-2.5 sm:px-4">
          <div className="flex min-w-0 items-center gap-2">
            <h2 className="min-w-0 truncate text-sm font-semibold">
              {t("panels.sessionMcp.toolsTitle", { name })}
            </h2>
            {scopeLabel ? (
              <span
                className={cn(
                  SESSION_DETAIL_CHIP_CLASS,
                  "border-border bg-surface-soft text-muted-foreground",
                )}
                data-testid="session-mcp-tools-scope"
              >
                {scopeLabel}
              </span>
            ) : null}
          </div>
          <div className="flex shrink-0 items-center gap-2">
            {payload && !isLoading ? (
              <span className="text-xs text-muted-foreground">
                {t("panels.sessionMcp.toolCount", { count: payload.count })}
              </span>
            ) : null}
            <Button
              aria-label={t("panels.sessionMcp.refresh")}
              data-testid="session-mcp-tools-refresh"
              disabled={isLoading}
              onClick={() => {
                void load();
              }}
              size="sm"
              variant="secondary"
            >
              <RefreshCwIcon
                className={cn(isLoading && "animate-spin")}
                size={13}
              />
            </Button>
            <Button
              data-testid="session-mcp-tools-close"
              size="sm"
              variant="secondary"
              onClick={onClose}
            >
              {t("panels.sessionMcp.toolsClose")}
            </Button>
          </div>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-3 py-3 sm:px-4">
          {isLoading ? (
            <p className="text-xs text-muted-foreground">
              {t("panels.sessionMcp.toolsLoading")}
            </p>
          ) : null}
          {!isLoading && error ? (
            <div className="flex items-center justify-between gap-2">
              <p className="text-xs text-accent-orange">
                {t("panels.sessionMcp.toolsLoadFailed")}: {error}
              </p>
              <Button
                data-testid="session-mcp-tools-retry"
                size="sm"
                variant="secondary"
                onClick={() => {
                  void load();
                }}
              >
                {t("panels.sessionMcp.toolsRetry")}
              </Button>
            </div>
          ) : null}
          {!isLoading && !error && tools.length === 0 ? (
            <p
              className="text-xs text-muted-foreground"
              data-testid="session-mcp-tools-empty"
            >
              {t("panels.sessionMcp.toolsEmpty")}
            </p>
          ) : null}
          {!isLoading && !error && tools.length > 0 ? (
            <ul className="space-y-2" data-testid="session-mcp-tools-list">
              {tools.map((tool) => (
                <SessionMcpToolRow key={tool.name} tool={tool} />
              ))}
            </ul>
          ) : null}
        </div>
      </DialogPanel>
    </DialogOverlay>,
    document.body,
  );
}

/** 单行工具：名称 + 状态标注 + 描述 + 可折叠 inputSchema（只读）。 */
function SessionMcpToolRow({ tool }: { tool: RuntimeMcpTool }) {
  const { t } = useTranslation("workspace");
  return (
    <li
      className="rounded-panel border border-border px-2.5 py-2"
      data-testid={`session-mcp-tool-${tool.name}`}
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-xs font-medium">{tool.name}</span>
        {tool.configured_enabled === false ? (
          <span className="text-[11px] text-accent-orange">
            {t("panels.sessionMcp.toolsDisabled")}
          </span>
        ) : null}
        {tool.healthy === false ? (
          <span className="text-[11px] text-accent-orange">
            {t("panels.sessionMcp.toolsUnhealthy")}
          </span>
        ) : null}
        {tool.enabled === false &&
        tool.configured_enabled !== false &&
        tool.healthy !== false ? (
          <span className="text-[11px] text-muted-foreground">
            {t("panels.sessionMcp.toolsNotExposed")}
          </span>
        ) : null}
      </div>
      {tool.healthy === false ? (
        <p className="mt-1 text-xs text-muted-foreground">
          {t("panels.sessionMcp.toolsUnhealthyHint")}
        </p>
      ) : null}
      {tool.description ? (
        <p className="mt-1 text-xs text-muted-foreground">{tool.description}</p>
      ) : null}
      {tool.inputSchema ? (
        <details className="mt-1.5">
          <summary className="cursor-pointer text-xs text-muted-foreground">
            {t("panels.sessionMcp.toolsSchema")}
          </summary>
          <pre className="mt-1 max-h-60 overflow-auto rounded-panel border border-border bg-muted/30 p-2 text-[11px] leading-relaxed">
            {JSON.stringify(tool.inputSchema, null, 2)}
          </pre>
        </details>
      ) : null}
    </li>
  );
}
