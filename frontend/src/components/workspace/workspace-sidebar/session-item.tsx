// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// P1-9 增量：行内相对时间、归档状态徽标与带键盘/aria 的操作菜单（新增 props 均可选）。
// 2026-09-16 样式优化：相对时间并入标题行末尾，与 hover 菜单图标同槽互换（整行单行高）。

import { useRef, useState, type DragEvent } from "react";
import { type RuntimeSessionRecord } from "@/lib/runtime-api";
import { cn } from "@/lib/utils";

import {
  SessionRowMenu,
  type SidebarSessionItemActionLabels,
} from "./session-row-menu";
import { type SidebarSessionRowState } from "./session-row-status";
import { SidebarStateIcon } from "./state-icons";
import { type SidebarStateIconSpec } from "./types";

// 菜单与文案类型已外置到 `session-row-menu.tsx`（P0-2）；保持原导出路径可用。
export type { SidebarSessionItemActionLabels } from "./session-row-menu";

export type InlineRenameInputProps = {
  ariaLabel: string;
  initial: string;
  onCancel: () => void;
  onSubmit: (value: string) => void;
  placeholder: string;
};

export function InlineRenameInput({
  ariaLabel,
  initial,
  onCancel,
  onSubmit,
  placeholder,
}: InlineRenameInputProps) {
  const [value, setValue] = useState(initial);
  const settledRef = useRef(false);

  return (
    <input
      autoFocus
      value={value}
      aria-label={ariaLabel}
      onChange={(event) => setValue(event.target.value)}
      onClick={(event) => event.stopPropagation()}
      onKeyDown={(event) => {
        if (event.key === "Enter") {
          event.preventDefault();
          const trimmed = value.trim();
          if (!trimmed) {
            settledRef.current = true;
            onCancel();
            return;
          }
          settledRef.current = true;
          onSubmit(trimmed);
        } else if (event.key === "Escape") {
          event.preventDefault();
          settledRef.current = true;
          onCancel();
        }
      }}
      onBlur={() => {
        if (!settledRef.current) {
          onCancel();
        }
      }}
      placeholder={placeholder}
      spellCheck={false}
      className="w-full min-w-0 rounded-[0.55rem] border border-accent-primary-border bg-surface-solid px-2 py-1 text-sm text-foreground outline-none"
    />
  );
}

export type SidebarSessionItemTime = {
  relative: string;
  /** 完整时间文案（含「创建于」）；2026-09-16 起并入整行 tooltip，不再挂在时间元素上。 */
  title: string;
};

/**
 * 批次 3（§5.5）谱系呈现：分支子行的缩进层级与来源徽标（文案已本地化）。
 * 缺省（无谱系）时行外观与 aria 保持原样。
 */
export type SidebarSessionItemLineage = {
  /** 0 = 无同批可见父行（含跨组 / 被过滤）；1 = 紧随父行的分支子行。 */
  depth: 0 | 1;
  /** 徽标文案（已本地化）。 */
  badgeLabel: string;
  /** 徽标悬浮提示（来源标题不可得时缺省，不伪造标题）。 */
  badgeTitle?: string;
};

export type SidebarSessionItemProps = {
  /** 归档/关闭等快照状态（P1-9）；缺省视为 active。 */
  rowState?: SidebarSessionRowState;
  /** 行内相对时间（P1-9）；缺省不渲染时间行。 */
  time?: SidebarSessionItemTime;
  /** 批次 3：分支谱系（缩进 + 来源徽标）；缺省保持既有无谱系外观。 */
  lineage?: SidebarSessionItemLineage;
  actionLabels?: SidebarSessionItemActionLabels;
  onArchive?: (sessionId: string) => void;
  onDelete?: (sessionId: string) => void;
  onFork?: (sessionId: string) => void;
  /** §4.8：该行当前是否可以就地停止（由活动投影决定，缺省不渲染菜单项）。 */
  canStop?: boolean;
  onStop?: (sessionId: string) => void;
  onRestore?: (sessionId: string) => void;
  isActive: boolean;
  onCancelRename: () => void;
  onRenameSubmit: (sessionId: string, title: string) => void;
  onSelect: () => void;
  onStartRename: (sessionId: string, currentTitle: string) => void;
  renameLabels: {
    placeholder: string;
    rename: string;
  };
  renaming: boolean;
  session: RuntimeSessionRecord;
  /** 行状态图标；null 表示该状态不渲染图标（如「已恢复会话」）。 */
  statusIcon: SidebarStateIconSpec | null;
  title: string;
  /** P2-6：组内拖拽重排。缺省整行不可拖（不渲染拖拽相关属性）。 */
  dragEnabled?: boolean;
  dragging?: boolean;
  dropEdge?: "before" | "after" | null;
  onDragStart?: (event: DragEvent<HTMLDivElement>) => void;
  onDragOver?: (event: DragEvent<HTMLDivElement>) => void;
  onDragLeave?: (event: DragEvent<HTMLDivElement>) => void;
  onDrop?: (event: DragEvent<HTMLDivElement>) => void;
  onDragEnd?: (event: DragEvent<HTMLDivElement>) => void;
};

export function SidebarSessionItem({
  actionLabels,
  canStop = false,
  dragEnabled = false,
  dragging = false,
  dropEdge = null,
  onDragEnd,
  onDragLeave,
  onDragOver,
  onDragStart,
  onDrop,
  isActive,
  lineage,
  onArchive,
  onCancelRename,
  onDelete,
  onFork,
  onRenameSubmit,
  onRestore,
  onStop,
  onSelect,
  onStartRename,
  renameLabels,
  renaming,
  rowState = "active",
  session,
  statusIcon,
  time,
  title,
}: SidebarSessionItemProps) {
  const archived = rowState === "archived";
  // 批次 3（§5.5）：深度 0/1 只影响缩进与 aria-level，不改变行结构。
  const depth = lineage?.depth ?? 0;
  const showMenu =
    Boolean(actionLabels) &&
    (archived
      ? Boolean(onRestore) || Boolean(onDelete) || Boolean(onStartRename)
      : Boolean(onArchive) ||
        Boolean(onDelete) ||
        Boolean(onFork) ||
        (canStop && Boolean(onStop)) ||
        Boolean(onStartRename));
  // 2026-09-16：相对时间并入标题行末尾，与 hover 菜单图标同槽互换（平时时间 / hover 图标）；
  // 完整时间戳随之内联进整行 tooltip，避免原生 tooltip 落在被让位隐藏的元素上。
  const rowTitle = [title, statusIcon?.label, time?.title]
    .filter((part): part is string => Boolean(part))
    .join(" · ");

  if (renaming) {
    return (
      <div
        className={cn(
          "flex w-full items-center gap-2 rounded-[0.72rem] border px-2 py-1 text-left transition",
          isActive
            ? "border-accent-secondary-border bg-accent-secondary-soft"
            : "border-border bg-surface-softer",
        )}
      >
        <InlineRenameInput
          ariaLabel={renameLabels.rename}
          initial={title}
          placeholder={renameLabels.placeholder}
          onCancel={onCancelRename}
          onSubmit={(value) => onRenameSubmit(session.id, value)}
        />
      </div>
    );
  }

  return (
    <div
      aria-level={depth + 1}
      className={cn("group/session relative", dragging && "opacity-60")}
      data-depth={depth}
      draggable={dragEnabled}
      onDragStart={onDragStart}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
      onDragEnd={onDragEnd}
      role="treeitem"
      style={depth > 0 ? { paddingLeft: depth * 12 } : undefined}
    >
      {dropEdge ? (
        <span
          aria-hidden
          data-testid={`session-drop-${dropEdge}`}
          className={cn(
            "pointer-events-none absolute inset-x-1 z-10 h-0.5 rounded-full bg-accent-primary",
            dropEdge === "before" ? "-top-1" : "-bottom-1",
          )}
        />
      ) : null}
      <button
        type="button"
        title={rowTitle}
        onClick={onSelect}
        className={cn(
          "flex w-full items-center gap-2 rounded-[0.72rem] border py-1.5 pl-2 text-left transition",
          // 2026-09-16：菜单入口与行内时间同槽 —— 时间顶到行尾（pr-2），图标悬浮其上；
          // 仅「没有时间可让位」的行（时间戳缺失 / 不可解析）保留右侧预留位，否则图标会压住徽标。
          showMenu && !time ? "pr-8" : "pr-2",
          isActive
            ? "border-accent-secondary-border bg-accent-secondary-soft"
            : archived
              ? "border-border border-dashed bg-surface-softer text-muted-foreground"
              : "border-border bg-surface-softer hover:border-border-strong hover:bg-surface-soft",
        )}
      >
        <div className="min-w-0 flex-1 truncate text-sm font-medium text-foreground">
          {title}
        </div>
        {lineage ? (
          <span
            className="shrink-0 rounded-[0.55rem] border border-border bg-surface-soft px-1.5 py-0.5 app-text-10 text-muted-foreground"
            data-testid="session-fork-badge"
            title={lineage.badgeTitle}
          >
            {lineage.badgeLabel}
          </span>
        ) : null}
        {archived && actionLabels ? (
          <span className="shrink-0 rounded-[0.55rem] border border-border bg-surface-soft px-1.5 py-0.5 app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
            {actionLabels.archivedBadge}
          </span>
        ) : null}
        {statusIcon ? <SidebarStateIcon spec={statusIcon} /> : null}
        {time ? (
          <span
            className={cn(
              "shrink-0 truncate app-text-10 text-muted-foreground transition",
              // 让位条件与菜单槽显示条件对齐（无菜单时不隐藏时间）：hover 整行，
              // 或键盘焦点落在菜单槽内。不用 `group-focus-within`：点击行后按钮保留焦点会误藏时间。
              showMenu &&
                "group-hover/session:opacity-0 group-has-[.session-row-actions:focus-within]/session:opacity-0",
            )}
            data-testid="session-row-time"
          >
            {time.relative}
          </span>
        ) : null}
      </button>
      {/* z-30：该容器被 `translate`/`opacity` 建立为层叠上下文，容器自身必须带正
          z-index，否则容器内菜单的 `z-20` 只在本上下文内生效，整行会被后续兄弟行
          （同为 z-index:auto 的定位元素，DOM 顺序在后）覆盖 —— 表现为菜单被下一行
          的按钮压住、菜单项点不到。 */}
      <div
        className={cn(
          "session-row-actions absolute right-1 top-1/2 z-30 flex -translate-y-1/2 items-center gap-0.5 opacity-0 transition",
          // 与时间同槽互换：hover 整行、或键盘焦点落在本槽（图标 / 菜单项）时图标显现。
          showMenu &&
            "group-hover/session:opacity-100 focus-within:opacity-100",
        )}
        data-testid="session-row-actions-slot"
      >
        {showMenu && actionLabels ? (
          <SessionRowMenu
            actionLabels={actionLabels}
            archived={archived}
            canStop={canStop}
            onArchive={onArchive}
            onDelete={onDelete}
            onFork={onFork}
            onRename={() => onStartRename(session.id, title)}
            onRestore={onRestore}
            onStop={onStop}
            renameLabel={renameLabels.rename}
            sessionId={session.id}
          />
        ) : null}
      </div>
    </div>
  );
}
