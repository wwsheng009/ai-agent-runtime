// MCP 工具清单对话框（设置域 MCP 面板「工具」按钮）。
//
// 数据由 GET /api/runtime/mcps/{name}/tools 现取：未启用 / 未连接时后端返回空数组，
// UI 渲染空态而不是报错；契约异常（tools 非数组）由客户端抛错并展示为错误态 + 重试。
// 工具级启用/停用走 POST /tools/{tool}/enable|disable，批量走 POST /tools/enable|disable；
// 任何行内操作成功后都重新拉取工具清单（局部刷新），失败只影响操作提示、不吞掉已有列表。
// 外壳沿用设置域对话框约定：portal 挂载、Esc / 遮罩关闭、关闭后焦点回位。

import { useCallback, useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import {
  listRuntimeMcpTools,
  setRuntimeMcpToolEnabled,
  setRuntimeMcpToolsEnabled,
} from "@/api/runtime/mcp";
import { Button } from "@/components/ui/button";
import { CheckboxInput } from "@/components/ui/checkbox";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import { useFocusRestore } from "@/hooks/workspace/use-focus-restore";
import type { RuntimeMcpTool, RuntimeMcpToolsResponse } from "@/types/runtime";

type McpToolsDialogProps = {
  name: string;
  onClose: () => void;
};

export function McpToolsDialog({ name, onClose }: McpToolsDialogProps) {
  const { t } = useTranslation("runtimeConfig");
  const [payload, setPayload] = useState<RuntimeMcpToolsResponse | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  /** 行内操作（单工具 / 批量）的进行中 key；非空时禁用全部控件，避免并发写。 */
  const [pending, setPending] = useState<string | null>(null);
  /** 操作失败提示；与加载错误分开，失败时保留已加载的列表。 */
  const [actionError, setActionError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setIsLoading(true);
    setError(null);
    setActionError(null);
    try {
      setPayload(await listRuntimeMcpTools(name));
    } catch (cause) {
      setPayload(null);
      const message = cause instanceof Error ? cause.message.trim() : "";
      setError(message || t("mcp.tools.loadFailed"));
    } finally {
      setIsLoading(false);
    }
  }, [name, t]);

  useEffect(() => {
    void load();
  }, [load]);

  /** 操作成功后局部刷新工具清单（不进入整页 loading，避免列表闪空）。 */
  const refreshTools = useCallback(async () => {
    setPayload(await listRuntimeMcpTools(name));
  }, [name]);

  const runAction = async (key: string, action: () => Promise<unknown>) => {
    if (pending) {
      return;
    }
    setPending(key);
    setActionError(null);
    try {
      await action();
      await refreshTools();
    } catch (cause) {
      const message = cause instanceof Error ? cause.message.trim() : "";
      setActionError(message || t("mcp.tools.actionFailed"));
    } finally {
      setPending(null);
    }
  };

  useDialogLifecycle(true, onClose);
  useFocusRestore(true);

  if (typeof document === "undefined") {
    return null;
  }

  const tools = payload?.tools ?? [];

  /** 用户配置态：旧后端缺 configured_enabled 时以有效暴露态兜底。 */
  const isConfiguredEnabled = (tool: RuntimeMcpTool) =>
    tool.configured_enabled ?? tool.enabled;

  const toggleTool = (tool: RuntimeMcpTool, enabled: boolean) => {
    void runAction(`tool:${tool.name}`, () =>
      setRuntimeMcpToolEnabled(name, tool.name, enabled),
    );
  };

  /**
   * 批量目标只包含「需要变更」的工具；没有需要变更的工具时传空数组，
   * 按后端契约空数组等价于「全部工具」（对已是目标态的工具是无副作用重放）。
   */
  const toggleAll = (enabled: boolean) => {
    const targets = tools
      .filter((tool) => isConfiguredEnabled(tool) !== enabled)
      .map((tool) => tool.name);
    void runAction(enabled ? "bulk:enable" : "bulk:disable", () =>
      setRuntimeMcpToolsEnabled(name, enabled, targets),
    );
  };

  return createPortal(
    <DialogOverlay className="z-[110]" onDismiss={onClose}>
      <DialogPanel
        aria-label={t("mcp.tools.dialogAriaLabel", { name })}
        aria-modal="true"
        className="max-w-[46rem]"
        data-testid="mcp-tools-dialog"
        elevation="lg"
        role="dialog"
      >
        <div className="flex items-center justify-between gap-2 border-b border-border px-3 py-2.5 sm:px-4">
          <h2 className="min-w-0 truncate text-sm font-semibold">
            {t("mcp.tools.title", { name })}
          </h2>
          <div className="flex shrink-0 items-center gap-2">
            {payload && !isLoading ? (
              <span className="text-xs text-muted-foreground">
                {t("mcp.counts.tools", { count: payload.count })}
              </span>
            ) : null}
            <Button
              data-testid="mcp-tools-dialog-close"
              size="sm"
              variant="secondary"
              onClick={onClose}
            >
              {t("mcp.actions.close")}
            </Button>
          </div>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-3 py-3 sm:px-4">
          {actionError ? (
            <p
              className="mb-2 text-xs text-accent-orange"
              data-testid="mcp-tools-action-error"
              role="alert"
            >
              {t("mcp.tools.actionFailed")}: {actionError}
            </p>
          ) : null}
          {isLoading ? (
            <p className="text-xs text-muted-foreground">
              {t("mcp.tools.loading")}
            </p>
          ) : null}
          {!isLoading && error ? (
            <div className="flex items-center justify-between gap-2">
              <p className="text-xs text-accent-orange">
                {t("mcp.tools.loadFailed")}: {error}
              </p>
              <Button
                data-testid="mcp-tools-dialog-retry"
                size="sm"
                variant="secondary"
                onClick={() => {
                  void load();
                }}
              >
                {t("mcp.actions.retry")}
              </Button>
            </div>
          ) : null}
          {!isLoading && !error && payload && tools.length === 0 ? (
            <p className="text-xs text-muted-foreground">{t("mcp.tools.empty")}</p>
          ) : null}
          {!isLoading && !error && tools.length > 0 ? (
            <ul className="space-y-2" data-testid="mcp-tools-list">
              {tools.map((tool) => {
                const configuredEnabled = isConfiguredEnabled(tool);
                return (
                  <li
                    key={tool.name}
                    className="rounded-panel border border-border px-2.5 py-2"
                    data-testid={`mcp-tool-row-${tool.name}`}
                  >
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-mono text-xs font-medium">
                        {tool.name}
                      </span>
                      {tool.configured_enabled === false ? (
                        <span className="text-[11px] text-accent-orange">
                          {t("mcp.tools.disabled")}
                        </span>
                      ) : null}
                      {tool.healthy === false ? (
                        <span className="text-[11px] text-accent-orange">
                          {t("mcp.tools.unhealthy")}
                        </span>
                      ) : null}
                      {tool.enabled === false &&
                      tool.configured_enabled !== false &&
                      tool.healthy !== false ? (
                        <span className="text-[11px] text-muted-foreground">
                          {t("mcp.tools.notExposed")}
                        </span>
                      ) : null}
                      <label className="ml-auto inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                        <span>
                          {configuredEnabled
                            ? t("mcp.tools.toggleOn")
                            : t("mcp.tools.toggleOff")}
                        </span>
                        <CheckboxInput
                          aria-label={t("mcp.tools.toggleAriaLabel", {
                            tool: tool.name,
                          })}
                          checked={configuredEnabled}
                          data-testid={`mcp-tool-toggle-${tool.name}`}
                          disabled={pending !== null}
                          onChange={(event) => {
                            toggleTool(tool, event.target.checked);
                          }}
                        />
                      </label>
                    </div>
                    {tool.healthy === false ? (
                      <p className="mt-1 text-xs text-muted-foreground">
                        {t("mcp.tools.unhealthyHint")}
                      </p>
                    ) : null}
                    {tool.description ? (
                      <p className="mt-1 text-xs text-muted-foreground">
                        {tool.description}
                      </p>
                    ) : null}
                    {tool.inputSchema ? (
                      <details className="mt-1.5">
                        <summary className="cursor-pointer text-xs text-muted-foreground">
                          {t("mcp.tools.schema")}
                        </summary>
                        <pre className="mt-1 max-h-60 overflow-auto rounded-panel border border-border bg-muted/30 p-2 text-[11px] leading-relaxed">
                          {JSON.stringify(tool.inputSchema, null, 2)}
                        </pre>
                      </details>
                    ) : null}
                  </li>
                );
              })}
            </ul>
          ) : null}
        </div>
        {!isLoading && !error && tools.length > 0 ? (
          <div className="flex flex-wrap items-center justify-end gap-2 border-t border-border px-3 py-2.5 sm:px-4">
            <Button
              data-testid="mcp-tools-enable-all"
              disabled={pending !== null}
              size="sm"
              variant="secondary"
              onClick={() => {
                toggleAll(true);
              }}
            >
              {t("mcp.tools.enableAll")}
            </Button>
            <Button
              data-testid="mcp-tools-disable-all"
              disabled={pending !== null}
              size="sm"
              variant="secondary"
              onClick={() => {
                toggleAll(false);
              }}
            >
              {t("mcp.tools.disableAll")}
            </Button>
          </div>
        ) : null}
      </DialogPanel>
    </DialogOverlay>,
    document.body,
  );
}
