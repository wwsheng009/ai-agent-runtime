// 「会话 MCP」面的行列表：配置/状态徽标 + 两个层次的启停 + 删除。
//
// 语义（与后端对齐）：
//   * 主操作「启用/停用」= 持久化写入会话生效的配置文件（scope=workspace/global），
//     影响使用同一配置的所有会话；仅在会话配置面可用（manageEnabled）。
//   * 次操作「本会话停用/恢复」= 会话级覆盖，不写配置文件；
//       - 停用：从本会话工具面移除该 server；
//       - 恢复：清除覆盖；后端对"停用的 server"返回 409（无临时连接），入口禁用。
//   * 删除：从会话生效的配置文件移除（写文件 + 热重载）。

import { ListIcon, Trash2Icon } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  SESSION_DETAIL_CARD_CLASS,
  SESSION_DETAIL_CHIP_CLASS,
} from "@/components/workspace/session-detail-panel-shared";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { RuntimeMcpEntry, RuntimeSessionMcpEntry } from "@/types/runtime";

/** 全局/生效启用位：状态优先，配置兜底；两者都缺时按启用（后端默认值）。 */
function isEnabled(entry: RuntimeMcpEntry): boolean {
  return entry.status?.enabled ?? entry.config?.enabled ?? true;
}

/** 行内徽标文案：连接态如实回显，未连接不伪造成错误。 */
function connectionLabelKey(
  entry: RuntimeMcpEntry,
): "panels.sessionMcp.connected" | "panels.sessionMcp.disconnected" {
  return entry.status?.connected
    ? "panels.sessionMcp.connected"
    : "panels.sessionMcp.disconnected";
}

export type SessionMcpRowsProps = {
  entries: RuntimeSessionMcpEntry[];
  disabledSet: Set<string>;
  /** 「本会话临时启用」名单（配置停用 + 会话私有连接）。 */
  tempEnabledSet: Set<string>;
  pendingName: string;
  /** 会话配置面可用（scope 字段存在）时展示持久化启停与删除。 */
  manageEnabled: boolean;
  onViewTools: (entry: RuntimeSessionMcpEntry) => void;
  onToggleSession: (entry: RuntimeSessionMcpEntry) => void;
  onTogglePersist: (entry: RuntimeSessionMcpEntry) => void;
  onDelete: (entry: RuntimeSessionMcpEntry) => void;
};

export function SessionMcpRows({
  entries,
  disabledSet,
  tempEnabledSet,
  pendingName,
  manageEnabled,
  onViewTools,
  onToggleSession,
  onTogglePersist,
  onDelete,
}: SessionMcpRowsProps) {
  const { t } = useTranslation("workspace");

  return (
    <ul className="grid gap-2">
      {entries.map((entry) => {
        const name = entry.config?.name?.trim() || entry.status?.name?.trim();
        if (!name) {
          return null;
        }
        const enabled = isEnabled(entry);
        // 顶层名单是会话覆盖的最新事实（refresh 时与服务端同步、toggle 后就地更新）；
        // 条目上的 session_disabled/session_enabled 来自上一次 fetch，若参与判定会把
        // 乐观更新用旧值遮蔽掉（点完按钮文案不变），因此这里只认名单。
        const sessionDisabled = disabledSet.has(name);
        const tempEnabled = tempEnabledSet.has(name);
        const pending = pendingName === name;
        const toolCount = entry.status?.toolCount ?? 0;

        return (
          <li
            className={cn(SESSION_DETAIL_CARD_CLASS, "grid gap-2")}
            data-global-enabled={enabled}
            data-session-state={
              sessionDisabled
                ? "disabled"
                : tempEnabled
                  ? "temp_enabled"
                  : "default"
            }
            data-source={entry.source ?? ""}
            data-testid="session-mcp-row"
            key={name}
          >
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <div
                  className="break-words font-mono app-text-12 text-foreground"
                  title={name}
                >
                  {name}
                </div>
                <div className="mt-1 flex flex-wrap items-center gap-1">
                  <span
                    className={cn(
                      SESSION_DETAIL_CHIP_CLASS,
                      enabled
                        ? "border-accent-primary/30 bg-accent-primary/10 text-accent-primary"
                        : "border-border bg-surface-soft text-muted-foreground",
                    )}
                    data-testid="session-mcp-global-badge"
                  >
                    {t(
                      enabled
                        ? "panels.sessionMcp.globalEnabled"
                        : "panels.sessionMcp.globalDisabled",
                    )}
                  </span>
                  {entry.source ? (
                    <span
                      className={cn(
                        SESSION_DETAIL_CHIP_CLASS,
                        "border-border bg-surface-soft text-muted-foreground",
                      )}
                      data-testid="session-mcp-source-badge"
                    >
                      {t(
                        entry.source === "workspace"
                          ? "panels.sessionMcp.sourceWorkspace"
                          : "panels.sessionMcp.sourceGlobal",
                      )}
                    </span>
                  ) : null}
                  <span
                    className={cn(
                      SESSION_DETAIL_CHIP_CLASS,
                      "border-border bg-surface-soft text-muted-foreground",
                    )}
                  >
                    {t(connectionLabelKey(entry))}
                  </span>
                  <span
                    className={cn(
                      SESSION_DETAIL_CHIP_CLASS,
                      "border-border bg-surface-soft text-muted-foreground",
                    )}
                  >
                    {t("panels.sessionMcp.toolCount", { count: toolCount })}
                  </span>
                  {sessionDisabled ? (
                    <span
                      className={cn(
                        SESSION_DETAIL_CHIP_CLASS,
                        "border-accent-gold/24 bg-accent-gold/10 text-accent-gold",
                      )}
                      data-testid="session-mcp-session-badge"
                    >
                      {t("panels.sessionMcp.sessionDisabled")}
                    </span>
                  ) : null}
                  {tempEnabled ? (
                    <span
                      className={cn(
                        SESSION_DETAIL_CHIP_CLASS,
                        "border-accent-primary/30 bg-accent-primary/10 text-accent-primary",
                      )}
                      data-testid="session-mcp-temp-badge"
                    >
                      {t("panels.sessionMcp.sessionTempBadge")}
                    </span>
                  ) : null}
                </div>
              </div>
              <div className="flex shrink-0 items-center gap-1">
                <Button
                  aria-label={t("panels.sessionMcp.toolsAction")}
                  className="h-7 w-7 px-0 text-muted-foreground"
                  data-testid="session-mcp-tools"
                  disabled={pending}
                  onClick={() => onViewTools(entry)}
                  size="sm"
                  title={t("panels.sessionMcp.toolsAction")}
                  variant="ghost"
                >
                  <ListIcon size={13} />
                </Button>
                {manageEnabled ? (
                  <Button
                    className="h-7 px-2 text-xs"
                    data-testid="session-mcp-persist-toggle"
                    disabled={pending}
                    onClick={() => onTogglePersist(entry)}
                    size="sm"
                    title={t("panels.sessionMcp.persistToggleTitle")}
                    variant="ghost"
                  >
                    {t(
                      enabled
                        ? "panels.sessionMcp.persistDisableAction"
                        : "panels.sessionMcp.persistEnableAction",
                    )}
                  </Button>
                ) : null}
                <Button
                  className="h-7 shrink-0 px-2 text-xs"
                  data-testid="session-mcp-toggle"
                  disabled={pending}
                  onClick={() => onToggleSession(entry)}
                  size="sm"
                  title={
                    sessionDisabled
                      ? t("panels.sessionMcp.enableTitle")
                      : tempEnabled
                        ? t("panels.sessionMcp.sessionTempDisableTitle")
                        : !enabled
                          ? t("panels.sessionMcp.sessionTempEnableTitle")
                          : t("panels.sessionMcp.disableTitle")
                  }
                  variant="ghost"
                >
                  {t(
                    sessionDisabled
                      ? "panels.sessionMcp.enableAction"
                      : tempEnabled || enabled
                        ? "panels.sessionMcp.disableAction"
                        : "panels.sessionMcp.sessionTempEnableAction",
                  )}
                </Button>
                {manageEnabled ? (
                  <Button
                    aria-label={t("panels.sessionMcp.deleteAction")}
                    className="h-7 w-7 px-0 text-muted-foreground"
                    data-testid="session-mcp-delete"
                    disabled={pending}
                    onClick={() => onDelete(entry)}
                    size="sm"
                    title={t("panels.sessionMcp.deleteAction")}
                    variant="ghost"
                  >
                    <Trash2Icon size={13} />
                  </Button>
                ) : null}
              </div>
            </div>
            {!enabled && !tempEnabled ? (
              <p className="app-text-11 leading-4 text-muted-foreground">
                {t("panels.sessionMcp.sessionTempHint")}
              </p>
            ) : null}
          </li>
        );
      })}
    </ul>
  );
}
