// P2-1A：子代理控制面弹层（AgentControl 身份图）。
//
// 数据由 `useSessionAgents` 统一提供（与会话顶栏面包屑共用一次加载，避免双请求）；
// 面板只渲染 + 触发 close/resume 两个动作。
//
// 诚实口径：
//   * 身份行缺失 → 明说「未登记身份」，不伪造一条根记录；
//   * 目录触顶（后端 count > 返回行数）→ 显式提示，不假装全量；
//   * `unknown` 状态不给停止/恢复按钮，并说明原因（不知道就别动）；
//   * 动作失败展示后端错误原文，不做本地乐观改写（状态以响应回写为准）。
// P2-9 子片 2：后代目录改为可折叠的**子代理树**（层级 / 只读原因 / 记录跨度
// 见 `session-agents-tree.tsx` 与 `session-agents-panel-shared.ts`）。
// 交互：Esc / 遮罩点击关闭，关闭后焦点回到触发按钮（use-focus-restore）。

import {
  AlertTriangleIcon,
  NetworkIcon,
  RefreshCwIcon,
  XIcon,
} from "lucide-react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import {
  agentDisplayName,
  agentStatusLabelKey,
  agentStatusToneClass,
  splitSessionAgents,
} from "@/components/workspace/session-agents-panel-shared";
import { SessionAgentsTree } from "@/components/workspace/session-agents-tree";
import { useFocusRestore } from "@/hooks/workspace/use-focus-restore";
import type { UseSessionAgentsResult } from "@/hooks/use-session-agents";
import { cn } from "@/lib/utils";

export type SessionAgentsPanelProps = {
  agents: UseSessionAgentsResult;
  onClose: () => void;
  open: boolean;
};

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function SessionAgentsPanel({ agents, onClose, open }: SessionAgentsPanelProps) {
  const { t } = useTranslation("workspace");
  const {
    actionError,
    actionErrorAgentId,
    catalog,
    closeAgent,
    error,
    pendingAgentId,
    refresh,
    resumeAgent,
    status,
    tree,
  } = agents;

  useDialogLifecycle(open, onClose);
  useFocusRestore(open);

  if (!open || typeof document === "undefined") {
    return null;
  }

  const { running, settled } = splitSessionAgents(tree.descendants);
  const isLoading = status === "loading";
  const directoryTitle = catalog
    ? t("panels.agents.descendantsCount", { count: tree.descendants.length })
    : t("panels.agents.descendantsTitle");

  return createPortal(
    <DialogOverlay className="z-[120] backdrop-blur-sm" onDismiss={onClose}>
      <DialogPanel
        aria-label={t("panels.agents.ariaLabel")}
        className="max-w-3xl"
        data-testid="session-agents-panel"
        role="dialog"
      >
        <div className="flex items-start justify-between gap-3 border-b border-border px-3.5 py-3 sm:px-4">
          <div className="min-w-0">
            <div className="app-text-11 uppercase tracking-[0.16em] text-accent-secondary">
              {t("panels.agents.eyebrow")}
            </div>
            <h2 className="mt-1 flex items-center gap-2 text-lg font-semibold tracking-[-0.03em] text-foreground">
              <NetworkIcon size={17} className="text-accent-primary" />
              {t("panels.agents.title")}
            </h2>
            <p className="mt-1 max-w-2xl text-sm leading-6 text-muted-foreground">
              {t("panels.agents.description")}
            </p>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            <Button
              aria-label={t("panels.agents.refresh")}
              disabled={isLoading}
              onClick={refresh}
              size="icon"
              title={t("panels.agents.refresh")}
              variant="ghost"
            >
              <RefreshCwIcon size={15} className={cn(isLoading && "animate-spin")} />
            </Button>
            <Button
              aria-label={t("panels.agents.close")}
              onClick={onClose}
              size="icon"
              title={t("panels.agents.close")}
              variant="ghost"
            >
              <XIcon size={16} />
            </Button>
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto px-3.5 py-3.5 sm:px-4">
          {status === "unavailable" ? (
            <div
              className="flex items-start gap-2 rounded-card border border-border bg-surface-softer px-2.5 py-2 text-xs text-muted-foreground"
              data-testid="agents-unavailable"
            >
              <AlertTriangleIcon size={14} className="mt-0.5 shrink-0" />
              <div className="min-w-0">
                <div className="font-medium text-foreground">
                  {t("panels.agents.unavailableTitle")}
                </div>
                <div className="mt-0.5 break-words">{t("panels.agents.unavailableHint")}</div>
                <div className="mt-1 break-words app-text-11">{errorText(error)}</div>
              </div>
            </div>
          ) : null}

          {status === "error" ? (
            <div
              className="flex items-start gap-2 rounded-card border border-border bg-surface-softer px-2.5 py-2 text-xs text-analytics-danger"
              data-testid="agents-error"
            >
              <AlertTriangleIcon size={14} className="mt-0.5 shrink-0" />
              <div className="min-w-0 flex-1">
                <div className="font-medium">{t("panels.agents.errorTitle")}</div>
                <div className="mt-0.5 break-words text-muted-foreground">
                  {errorText(error)}
                </div>
              </div>
              <Button onClick={refresh} size="sm" variant="ghost">
                {t("panels.agents.retry")}
              </Button>
            </div>
          ) : null}

          {status === "loading" ? (
            <div className="mb-3 flex items-center gap-2 rounded-card border border-border bg-surface-softer px-2.5 py-2 text-xs text-muted-foreground">
              <RefreshCwIcon size={13} className="animate-spin" />
              {t("panels.agents.loading")}
            </div>
          ) : null}

          {status === "ready" && !tree.hasIdentity ? (
            <div
              className="rounded-card border border-border bg-surface-softer px-2.5 py-2.5"
              data-testid="agents-no-identity"
            >
              <div className="app-text-13 font-medium text-foreground">
                {t("panels.agents.noIdentityTitle")}
              </div>
              <p className="mt-1 text-xs leading-5 text-muted-foreground">
                {t("panels.agents.noIdentityHint")}
              </p>
            </div>
          ) : null}

          {status === "ready" && tree.hasIdentity ? (
            <>
              <section data-testid="agents-lineage">
                <div className="app-text-11 uppercase tracking-[0.16em] text-muted-foreground">
                  {t("panels.agents.lineageTitle")}
                </div>
                <ol className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-2">
                  {tree.lineage.map((agent, index) => (
                    <li className="flex items-center gap-2" key={agent.agentId}>
                      {index > 0 ? (
                        <span aria-hidden="true" className="text-muted-foreground">
                          ›
                        </span>
                      ) : null}
                      <span
                        className={cn(
                          "flex items-center gap-1.5 rounded-card border border-border px-2 py-1",
                          agent.agentId === tree.currentAgent?.agentId
                            ? "bg-surface-softer"
                            : null,
                        )}
                        data-current={
                          agent.agentId === tree.currentAgent?.agentId ? "true" : "false"
                        }
                        data-testid="agents-lineage-node"
                        title={agent.agentPath ?? agent.agentId}
                      >
                        <span className="app-text-11 font-medium text-foreground">
                          {agentDisplayName(agent)}
                        </span>
                        <Badge
                          className={cn(
                            "h-5 px-1.5 text-[10px]",
                            agentStatusToneClass(agent.status),
                          )}
                        >
                          {t(agentStatusLabelKey(agent.status), {
                            defaultValue: agent.status,
                          })}
                        </Badge>
                      </span>
                    </li>
                  ))}
                </ol>
              </section>

              <section className="mt-4" data-testid="agents-descendants">
                <div className="flex items-center justify-between gap-2">
                  <div className="app-text-11 uppercase tracking-[0.16em] text-muted-foreground">
                    {directoryTitle}
                  </div>
                  {agents.truncated ? (
                    <span
                      className="app-text-10 text-analytics-warning"
                      data-testid="agents-truncated"
                    >
                      {t("panels.agents.truncated", {
                        count: catalog?.count ?? tree.descendants.length,
                        limit: String(catalog?.limit ?? 0),
                      })}
                    </span>
                  ) : null}
                </div>

                {tree.descendants.length === 0 ? (
                  <div
                    className="mt-2 rounded-card border border-border bg-surface-softer px-2.5 py-2.5"
                    data-testid="agents-empty"
                  >
                    <div className="app-text-13 font-medium text-foreground">
                      {t("panels.agents.emptyDescendants")}
                    </div>
                    <p className="mt-1 text-xs leading-5 text-muted-foreground">
                      {t("panels.agents.emptyDescendantsHint")}
                    </p>
                  </div>
                ) : (
                  <>
                    <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 app-text-10 text-muted-foreground">
                      <span data-testid="agents-count-running">
                        {t("panels.agents.running", { count: running.length })}
                      </span>
                      <span data-testid="agents-count-settled">
                        {t("panels.agents.settled", { count: settled.length })}
                      </span>
                    </div>
                    <SessionAgentsTree
                      actionError={actionError}
                      actionErrorAgentId={actionErrorAgentId}
                      agents={tree.descendants}
                      onClose={(agentId) => {
                        void closeAgent(agentId);
                      }}
                      onResume={(agentId) => {
                        void resumeAgent(agentId);
                      }}
                      pendingAgentId={pendingAgentId}
                    />
                  </>
                )}
              </section>
            </>
          ) : null}
        </div>
      </DialogPanel>
    </DialogOverlay>,
    document.body,
  );
}
