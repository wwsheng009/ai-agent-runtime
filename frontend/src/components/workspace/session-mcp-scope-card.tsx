// 会话 MCP「配置来源」卡：展示会话实际生效的配置文件与工作区锚定状态
// （数据来自 listRuntimeSessionMcps 的 scope 字段；旧后端缺省时整卡隐藏）。
//
// 纯展示组件：不发起请求、不改配置，只解释"这个会话的 MCP 到底来自哪个文件"。

import { FolderCogIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  SESSION_DETAIL_CARD_CLASS,
  SESSION_DETAIL_CHIP_CLASS,
} from "@/components/workspace/session-detail-panel-shared";
import { cn } from "@/lib/utils";
import type { RuntimeSessionMcpScope } from "@/types/runtime";

export type SessionMcpScopeCardProps = {
  scope: RuntimeSessionMcpScope;
};

export function SessionMcpScopeCard({ scope }: SessionMcpScopeCardProps) {
  const { t } = useTranslation("workspace");
  const workspaceScoped = scope.workspace_scoped;
  const effectivePath = scope.read?.path?.trim() ?? "";

  return (
    <div
      className={cn(SESSION_DETAIL_CARD_CLASS, "grid gap-1.5")}
      data-testid="session-mcp-scope"
      data-workspace-scoped={workspaceScoped}
    >
      <div className="flex flex-wrap items-center gap-1.5">
        <FolderCogIcon className="text-accent-primary" size={13} />
        <span className="text-xs font-semibold text-foreground">
          {t("panels.sessionMcp.scopeTitle")}
        </span>
        <span
          className={cn(
            SESSION_DETAIL_CHIP_CLASS,
            workspaceScoped
              ? "border-accent-primary/30 bg-accent-primary/10 text-accent-primary"
              : "border-border bg-surface-soft text-muted-foreground",
          )}
          data-testid="session-mcp-scope-badge"
        >
          {t(
            workspaceScoped
              ? "panels.sessionMcp.scopeWorkspace"
              : "panels.sessionMcp.scopeGlobal",
          )}
        </span>
      </div>
      {effectivePath ? (
        <div className="flex items-start gap-2">
          <span className="shrink-0 app-text-11 text-muted-foreground">
            {t("panels.sessionMcp.scopeEffectivePath")}
          </span>
          <code
            className="min-w-0 break-all font-mono app-text-11 text-foreground"
            data-testid="session-mcp-scope-path"
          >
            {effectivePath}
          </code>
        </div>
      ) : null}
      {scope.workspace && scope.workspace_fallback && scope.write?.path ? (
        <p
          className="app-text-11 leading-4 text-muted-foreground"
          data-testid="session-mcp-scope-fallback"
        >
          {t("panels.sessionMcp.scopeFallback", { path: scope.write.path })}
        </p>
      ) : null}
    </div>
  );
}
