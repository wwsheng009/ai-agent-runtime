// Phase 1（合并方案 §3.6）：会话浏览工具条。
// 从 sessions-section.tsx 原样搬迁：用户选择（选中用户的会话树由调用方作为 children 注入，
// 保持既有 DOM 嵌套）→ 会话统计 + 刷新 → 管理目录 → 排序 / 分组 / 归档开关 → 跨组移动错误条。
// 纯装配件：不持有状态，所有模式与回调都由接线层（workspace-sidebar.tsx）提供。

import {
  ArchiveIcon,
  ChevronDownIcon,
  FolderCogIcon,
  LoaderCircleIcon,
  UserIcon,
} from "lucide-react";
import { type ReactNode } from "react";
import { type TFunction } from "i18next";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

import { type SessionStatsStatus } from "@/hooks/workspace/use-session-stats";
import { type SessionGroupingMode } from "@/lib/workspace/session-grouping";
import { type SessionOrderMode } from "@/lib/workspace/session-order";
import { type RuntimeSessionStats } from "@/types/runtime";

import { WorkspaceSidebarSessionGroupingControl } from "./session-grouping-control";
import { WorkspaceSidebarSessionOrderControl } from "./session-order-control";
import { WorkspaceSidebarSessionStatsSummary } from "./session-stats-summary";
import { type WorkspaceSidebarSessionUserMenuItem } from "./session-user-menu";

export type WorkspaceSidebarSessionBrowserToolbarProps = {
  /** 选中用户下的会话树（由调用方装配，保持既有的缩进与落点语义）。 */
  children: ReactNode;
  /** 没有任何用户条目时渲染的内容：旧会话段用空态提示，合并段用目录树。 */
  emptyState: ReactNode;
  /** 是否存在可列举的会话组：决定排序 / 分组控件是否出现（无组时不给空控件）。 */
  hasGroups: boolean;
  hiddenArchivedCount: number;
  /** 管理目录弹层入口：平铺视图没有组头，目录管理必须由工具条承载（方案 §3.5-D）。 */
  onManageDirectories: () => void;
  onRefreshSessionStats: () => void;
  onSelectRuntimeSessionUser: (userId: string) => void;
  onSelectSessionGroupingMode: (mode: SessionGroupingMode) => void;
  onSelectSessionOrderMode: (mode: SessionOrderMode) => void;
  onToggleArchivedSessions: () => void;
  runtimeSessionUsersError: string | null;
  runtimeSessionUsersLoading: boolean;
  selectedRuntimeSessionUserId: string;
  sessionGroupingMode: SessionGroupingMode;
  /** 跨组移动失败的就地提示（失败即回滚）。 */
  sessionMoveError: string | null;
  sessionOrderMode: SessionOrderMode;
  sessionStats: RuntimeSessionStats | null;
  sessionStatsError: unknown;
  sessionStatsStatus: SessionStatsStatus;
  sessionStatsUnavailable: boolean;
  sessionUserMenuItems: WorkspaceSidebarSessionUserMenuItem[];
  showArchivedSessions: boolean;
  t: TFunction<"workspace">;
};

export function WorkspaceSidebarSessionBrowserToolbar({
  children,
  emptyState,
  hasGroups,
  hiddenArchivedCount,
  onManageDirectories,
  onRefreshSessionStats,
  onSelectRuntimeSessionUser,
  onSelectSessionGroupingMode,
  onSelectSessionOrderMode,
  onToggleArchivedSessions,
  runtimeSessionUsersError,
  runtimeSessionUsersLoading,
  selectedRuntimeSessionUserId,
  sessionGroupingMode,
  sessionMoveError,
  sessionOrderMode,
  sessionStats,
  sessionStatsError,
  sessionStatsStatus,
  sessionStatsUnavailable,
  sessionUserMenuItems,
  showArchivedSessions,
  t,
}: WorkspaceSidebarSessionBrowserToolbarProps) {
  return (
    <div className="space-y-1.5">
      {runtimeSessionUsersLoading && sessionUserMenuItems.length === 0 ? (
        <div className="inline-flex items-center gap-1.5 rounded-control border border-border bg-surface-soft px-2 py-1 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
          <LoaderCircleIcon size={12} className="animate-spin" />
          {t("sidebar.sessionUsersLoading")}
        </div>
      ) : null}
      {runtimeSessionUsersError ? (
        <div className="rounded-[0.75rem] border border-accent-orange/18 bg-accent-orange/8 px-2.5 py-2 text-xs leading-5 text-muted-foreground">
          {runtimeSessionUsersError}
        </div>
      ) : null}
      <WorkspaceSidebarSessionStatsSummary
        error={sessionStatsError}
        onRefresh={onRefreshSessionStats}
        stats={sessionStats}
        status={sessionStatsStatus}
        t={t}
        unavailable={sessionStatsUnavailable}
      />
      {hasGroups ? (
        <WorkspaceSidebarSessionOrderControl
          mode={sessionOrderMode}
          onSelect={onSelectSessionOrderMode}
          t={t}
        />
      ) : null}
      <button
        type="button"
        onClick={onManageDirectories}
        className="flex w-full items-center justify-center gap-1.5 rounded-control border border-dashed border-border bg-surface-softer px-2 py-1 app-text-10 uppercase tracking-[0.14em] text-muted-foreground transition hover:text-foreground"
      >
        <FolderCogIcon size={12} />
        {t("sidebar.directories.manage")}
      </button>
      {hasGroups ? (
        <WorkspaceSidebarSessionGroupingControl
          mode={sessionGroupingMode}
          onSelect={onSelectSessionGroupingMode}
          t={t}
        />
      ) : null}
      {sessionMoveError ? (
        <div className="rounded-[0.75rem] border border-accent-orange/18 bg-accent-orange/8 px-2.5 py-2 text-xs leading-5 text-muted-foreground">
          {sessionMoveError}
        </div>
      ) : null}
      {showArchivedSessions || hiddenArchivedCount > 0 ? (
        <button
          type="button"
          onClick={onToggleArchivedSessions}
          className={cn(
            "flex w-full items-center justify-center gap-1.5 rounded-control border px-2 py-1 app-text-10 uppercase tracking-[0.14em] transition",
            showArchivedSessions
              ? "border-accent-secondary-border bg-accent-secondary-soft text-accent-secondary"
              : "border-dashed border-border bg-surface-softer text-muted-foreground hover:text-foreground",
          )}
        >
          <ArchiveIcon size={12} />
          {showArchivedSessions
            ? t("sidebar.session.hideArchived")
            : t("sidebar.session.showArchived", {
                count: hiddenArchivedCount,
              })}
        </button>
      ) : null}
      {sessionUserMenuItems.length > 0
        ? sessionUserMenuItems.map((user) => {
            const isSelectedUser =
              user.userId === selectedRuntimeSessionUserId.trim();

            return (
              <div key={user.userId} className="space-y-1">
                <button
                  type="button"
                  title={user.userId}
                  onClick={() => onSelectRuntimeSessionUser(user.userId)}
                  className={cn(
                    "flex w-full items-center gap-2 rounded-card border px-2.5 py-2 text-left transition",
                    isSelectedUser
                      ? "border-accent-secondary-border bg-accent-secondary-soft"
                      : "border-border bg-surface-softer hover:border-border-strong hover:bg-surface-soft",
                  )}
                >
                  <UserIcon
                    size={14}
                    className="shrink-0 text-accent-secondary"
                  />
                  <span className="min-w-0 flex-1 truncate text-sm font-semibold text-foreground">
                    {user.displayName}
                  </span>
                  {user.isDefaultUser ? (
                    <span className="shrink-0 rounded-[0.55rem] border border-border bg-surface-soft px-1.5 py-0.5 app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
                      {t("sidebar.sessionUserDefault")}
                    </span>
                  ) : null}
                  <Badge>{user.sessionCount}</Badge>
                  <ChevronDownIcon
                    size={14}
                    className={cn(
                      "shrink-0 text-muted-foreground transition-transform duration-200",
                      isSelectedUser ? "rotate-0" : "-rotate-90",
                    )}
                  />
                </button>

                {isSelectedUser ? (
                  // 2026-09-15 样式优化（第二轮）：目录组头是**第一层级元素**，
                  // 取消会话树的嵌套缩进（原 `ml-3` 12px，目录分组模式再叠加
                  // `border-l` + `pl-2` 共 20px）——组头左缘现在与分区标题、
                  // 用户卡片同一条基准线，直接顶到侧栏内容盒左侧。
                  <div className="space-y-1">{children}</div>
                ) : null}
              </div>
            );
          })
        : !runtimeSessionUsersLoading
          ? emptyState
          : null}
    </div>
  );
}
