// 2026-09-15 布局优化：**工作目录分区的唯一操作入口**。
//
// 背景：合并方案 Phase 2 把「工作目录」与「会话」并成一段后，原会话段的整条浏览器工具条
// （统计摘要 + 刷新 / 排序双档 / 管理目录 / 分组双档 / 归档开关）被原样平铺在段内，
// 5 行常驻控件压在目录会话树上方，树本身被挤到侧栏下半屏。
//
// 落位（本次）：这些**低频浏览设置**收敛到段头的 ⋯ 弹出面板，段内只留用户选择、错误提示
// 与会话树；段头保持「添加目录（一键）」+「⋯（浏览设置）」两个 14px 图标。
//
// 与目录组头菜单（`directory-group-actions.tsx`）的分工：
//   * 组头 ⋯ = 单个目录的注册表动作（新建会话 / 重命名 / 移除）；
//   * 段头 ⋯ = 整段的浏览口径（统计 / 排序 / 分组 / 管理目录 / 归档）。
//
// 语义选择：面板里既有分段控件（排序 / 分组是既有 `role="group"` + `aria-pressed` 件，
// e2e 按该口径定位）又有按钮，因此面板用 `role="dialog"`（非模态弹出层）而不是 `role="menu"`，
// 触发键相应地声明 `aria-haspopup="dialog"`；键盘路径为 Escape 关闭并还焦触发键。
// 统计降级（不可用 / 加载失败）在触发键上留一个橙色圆点：面板关闭时错误仍可发现，
// 不会被收进弹层后「藏起来」。
//
// 定位：面板的定位基准是**分区标题行**（`section-shell.tsx` 头行上的 `relative`），宽度随行宽
// 自适应（`right-1` + `w-[calc(100%-1rem)]`）。若以 ⋯ 图标自身为基准，固定宽度会向左越出侧栏，
// 被 `aside` 的 `overflow-hidden` 裁掉。代价是面板会盖住段内其下的内容（用户卡片 / 首个会话行），
// 这是弹出层的固有行为：点面板外的任意位置、Escape、或再点一次 ⋯ 都能收起。

import {
  ArchiveIcon,
  FolderCogIcon,
  MoreHorizontalIcon,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { type TFunction } from "i18next";

import { cn } from "@/lib/utils";

import type { SessionStatsStatus } from "@/hooks/workspace/use-session-stats";
import { type SessionGroupingMode } from "@/lib/workspace/session-grouping";
import { type SessionOrderMode } from "@/lib/workspace/session-order";
import { type RuntimeSessionStats } from "@/types/runtime";

import { WorkspaceSidebarSessionGroupingControl } from "./session-grouping-control";
import { WorkspaceSidebarSessionOrderControl } from "./session-order-control";
import { WorkspaceSidebarSessionStatsSummary } from "./session-stats-summary";

export type WorkspaceSidebarDirectorySectionMenuProps = {
  /** 是否存在可列举的会话组：无组时排序 / 分组控件不出现（不给空控件）。 */
  hasGroups: boolean;
  hiddenArchivedCount: number;
  /** 管理目录弹层入口（目录 CRUD 的唯一入口，平铺视图下也不丢失）。 */
  onManageDirectories: () => void;
  onRefreshSessionStats: () => void;
  onSelectSessionGroupingMode: (mode: SessionGroupingMode) => void;
  onSelectSessionOrderMode: (mode: SessionOrderMode) => void;
  onToggleArchivedSessions: () => void;
  sessionGroupingMode: SessionGroupingMode;
  sessionOrderMode: SessionOrderMode;
  sessionStats: RuntimeSessionStats | null;
  sessionStatsError: unknown;
  sessionStatsStatus: SessionStatsStatus;
  sessionStatsUnavailable: boolean;
  showArchivedSessions: boolean;
  t: TFunction<"workspace">;
};

export function WorkspaceSidebarDirectorySectionMenu({
  hasGroups,
  hiddenArchivedCount,
  onManageDirectories,
  onRefreshSessionStats,
  onSelectSessionGroupingMode,
  onSelectSessionOrderMode,
  onToggleArchivedSessions,
  sessionGroupingMode,
  sessionOrderMode,
  sessionStats,
  sessionStatsError,
  sessionStatsStatus,
  sessionStatsUnavailable,
  showArchivedSessions,
  t,
}: WorkspaceSidebarDirectorySectionMenuProps) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const panelRef = useRef<HTMLDivElement | null>(null);
  const menuLabel = t("sidebar.directories.menu");
  // 统计降级（不可用 / 加载失败）：面板关闭时也要有可见信号，否则错误被收进弹层即失联。
  const statsDegraded = sessionStatsStatus === "error";

  useEffect(() => {
    if (open) {
      // 打开即把焦点移进面板：Escape 立即可用，Tab 从面板内继续走。
      panelRef.current?.focus();
    }
  }, [open]);

  function close(focusTrigger: boolean) {
    setOpen(false);
    if (focusTrigger) {
      triggerRef.current?.focus();
    }
  }

  const itemClassName =
    "flex w-full items-center gap-1.5 rounded-[0.5rem] px-1.5 py-1 text-left text-xs transition hover:bg-surface-soft";

  return (
    <div
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) {
          setOpen(false);
        }
      }}
    >
      <button
        ref={triggerRef}
        type="button"
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={menuLabel}
        title={menuLabel}
        data-testid="sidebar-directories-menu-trigger"
        onClick={() => setOpen((current) => !current)}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown") {
            event.preventDefault();
            setOpen(true);
          } else if (event.key === "Escape" && open) {
            event.preventDefault();
            close(false);
          }
        }}
        className="relative rounded-chip p-1 text-muted-foreground transition hover:bg-surface-soft hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent-primary-border"
      >
        <MoreHorizontalIcon size={14} />
        {statsDegraded ? (
          <span
            aria-hidden="true"
            data-testid="sidebar-directories-menu-alert"
            className="absolute right-0.5 top-0.5 size-1.5 rounded-full bg-accent-orange"
          />
        ) : null}
      </button>

      {open ? (
        <div
          ref={panelRef}
          role="dialog"
          aria-label={menuLabel}
          tabIndex={-1}
          data-testid="sidebar-directories-menu-panel"
          onKeyDown={(event) => {
            if (event.key === "Escape") {
              event.preventDefault();
              event.stopPropagation();
              close(true);
            }
          }}
          // 定位基准是**分区标题行**（见 `section-shell.tsx` 的 `relative`）：`right-1` 让面板
          // 右缘与 ⋯ 图标对齐，宽度按行宽自适应（而非固定 16rem），面板绝不越出侧栏左缘。
          className="absolute right-1 top-full z-30 mt-1 w-[calc(100%-1rem)] space-y-2 rounded-[0.7rem] border border-border bg-surface-popover p-2 shadow-lg focus:outline-none"
        >
          <section className="space-y-1">
            <p className="px-1 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
              {t("sidebar.sessionStats.label")}
            </p>
            <WorkspaceSidebarSessionStatsSummary
              error={sessionStatsError}
              onRefresh={onRefreshSessionStats}
              stats={sessionStats}
              status={sessionStatsStatus}
              t={t}
              unavailable={sessionStatsUnavailable}
            />
          </section>

          {hasGroups ? (
            <>
              <div className="h-px bg-border" role="presentation" />
              <section className="space-y-1">
                <WorkspaceSidebarSessionOrderControl
                  mode={sessionOrderMode}
                  onSelect={onSelectSessionOrderMode}
                  t={t}
                />
                <WorkspaceSidebarSessionGroupingControl
                  mode={sessionGroupingMode}
                  onSelect={onSelectSessionGroupingMode}
                  t={t}
                />
              </section>
            </>
          ) : null}

          <div className="h-px bg-border" role="presentation" />
          <section className="space-y-0.5">
            <button
              type="button"
              data-testid="sidebar-directories-manage-entry"
              onClick={() => {
                close(false);
                onManageDirectories();
              }}
              className={cn(
                itemClassName,
                "text-muted-foreground hover:text-foreground",
              )}
            >
              <FolderCogIcon size={12} className="shrink-0" />
              {t("sidebar.directories.manage")}
            </button>
            {showArchivedSessions || hiddenArchivedCount > 0 ? (
              <button
                type="button"
                aria-pressed={showArchivedSessions}
                data-testid="sidebar-session-archived-toggle"
                onClick={onToggleArchivedSessions}
                className={cn(
                  itemClassName,
                  showArchivedSessions
                    ? "text-accent-secondary"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                <ArchiveIcon size={12} className="shrink-0" />
                {showArchivedSessions
                  ? t("sidebar.session.hideArchived")
                  : t("sidebar.session.showArchived", {
                      count: hiddenArchivedCount,
                    })}
              </button>
            ) : null}
          </section>
        </div>
      ) : null}
    </div>
  );
}
