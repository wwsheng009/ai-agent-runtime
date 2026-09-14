// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// P1-9 增量：行内相对时间、归档状态徽标与带键盘/aria 的操作菜单（新增 props 均可选）。

import { MoreHorizontalIcon, PencilIcon } from "lucide-react";
import { useEffect, useRef, useState, type DragEvent } from "react";
import { type RuntimeSessionRecord } from "@/lib/runtime-api";
import { cn } from "@/lib/utils";

import { type SidebarSessionRowState } from "./session-row-status";
import { SidebarStateIcon } from "./state-icons";
import { type SidebarStateIconSpec } from "./types";

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

export type SidebarSessionItemActionLabels = {
  archivedBadge: string;
  archive: string;
  delete: string;
  fork: string;
  menu: string;
  restore: string;
};

export type SidebarSessionItemTime = {
  relative: string;
  /** 悬浮完整文案（含「创建于」）。 */
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
  statusIcon: SidebarStateIconSpec;
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

type SessionRowMenuProps = {
  actionLabels: SidebarSessionItemActionLabels;
  archived: boolean;
  onArchive?: (sessionId: string) => void;
  onDelete?: (sessionId: string) => void;
  onFork?: (sessionId: string) => void;
  onRestore?: (sessionId: string) => void;
  sessionId: string;
};

function SessionRowMenu({
  actionLabels,
  archived,
  onArchive,
  onDelete,
  onFork,
  onRestore,
  sessionId,
}: SessionRowMenuProps) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!open) {
      return;
    }
    const items = menuRef.current?.querySelectorAll<HTMLButtonElement>(
      '[role="menuitem"]',
    );
    items?.[0]?.focus();
  }, [open]);

  function closeMenu(focusTrigger: boolean) {
    setOpen(false);
    if (focusTrigger) {
      triggerRef.current?.focus();
    }
  }

  function moveFocus(delta: number | "first" | "last") {
    const items = Array.from(
      menuRef.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') ??
        [],
    );
    if (items.length === 0) {
      return;
    }
    if (delta === "first") {
      items[0]?.focus();
      return;
    }
    if (delta === "last") {
      items[items.length - 1]?.focus();
      return;
    }
    const activeIndex = items.findIndex(
      (item) => item === document.activeElement,
    );
    const nextIndex = (activeIndex + delta + items.length) % items.length;
    items[nextIndex]?.focus();
  }

  return (
    <div
      className="relative"
      onBlur={(event) => {
        if (
          !event.currentTarget.contains(event.relatedTarget as Node | null)
        ) {
          setOpen(false);
        }
      }}
    >
      <button
        ref={triggerRef}
        type="button"
        aria-expanded={open}
        aria-haspopup="menu"
        aria-label={actionLabels.menu}
        title={actionLabels.menu}
        onClick={(event) => {
          event.stopPropagation();
          setOpen((current) => !current);
        }}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown") {
            event.preventDefault();
            setOpen(true);
          } else if (event.key === "Escape" && open) {
            event.preventDefault();
            closeMenu(false);
          }
        }}
        className="rounded-chip p-1 text-muted-foreground transition hover:bg-surface-soft hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent-primary-border"
      >
        <MoreHorizontalIcon size={12} />
      </button>
      {open ? (
        <div
          ref={menuRef}
          role="menu"
          aria-label={actionLabels.menu}
          onClick={(event) => event.stopPropagation()}
          onKeyDown={(event) => {
            if (event.key === "Escape") {
              event.preventDefault();
              event.stopPropagation();
              closeMenu(true);
            } else if (event.key === "ArrowDown") {
              event.preventDefault();
              moveFocus(1);
            } else if (event.key === "ArrowUp") {
              event.preventDefault();
              moveFocus(-1);
            } else if (event.key === "Home") {
              event.preventDefault();
              moveFocus("first");
            } else if (event.key === "End") {
              event.preventDefault();
              moveFocus("last");
            } else if (event.key === "Tab") {
              closeMenu(false);
            }
          }}
          className="absolute right-0 top-full z-20 mt-1 min-w-[9rem] rounded-[0.6rem] border border-border bg-surface-solid py-1 shadow-lg"
        >
          {!archived && onFork ? (
            <button
              type="button"
              role="menuitem"
              tabIndex={-1}
              onClick={() => {
                onFork(sessionId);
                closeMenu(true);
              }}
              className="block w-full px-2.5 py-1.5 text-left text-xs text-foreground transition hover:bg-surface-soft"
            >
              {actionLabels.fork}
            </button>
          ) : null}
          {archived ? (
            onRestore ? (
              <button
                type="button"
                role="menuitem"
                tabIndex={-1}
                onClick={() => {
                  onRestore(sessionId);
                  closeMenu(true);
                }}
                className="block w-full px-2.5 py-1.5 text-left text-xs text-foreground transition hover:bg-surface-soft"
              >
                {actionLabels.restore}
              </button>
            ) : null
          ) : onArchive ? (
            <button
              type="button"
              role="menuitem"
              tabIndex={-1}
              onClick={() => {
                onArchive(sessionId);
                closeMenu(true);
              }}
              className="block w-full px-2.5 py-1.5 text-left text-xs text-foreground transition hover:bg-surface-soft"
            >
              {actionLabels.archive}
            </button>
          ) : null}
          {onDelete ? (
            <button
              type="button"
              role="menuitem"
              tabIndex={-1}
              onClick={() => {
                onDelete(sessionId);
                closeMenu(true);
              }}
              className="block w-full px-2.5 py-1.5 text-left text-xs text-muted-foreground transition hover:bg-surface-soft hover:text-foreground"
            >
              {actionLabels.delete}
            </button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

export function SidebarSessionItem({
  actionLabels,
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
      ? Boolean(onRestore) || Boolean(onDelete)
      : Boolean(onArchive) || Boolean(onDelete) || Boolean(onFork));

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
        title={`${title} · ${statusIcon.label}`}
        onClick={onSelect}
        className={cn(
          "flex w-full items-center gap-2 rounded-[0.72rem] border py-1.5 pl-2 text-left transition",
          showMenu ? "pr-16" : "pr-9",
          isActive
            ? "border-accent-secondary-border bg-accent-secondary-soft"
            : archived
              ? "border-border border-dashed bg-surface-softer text-muted-foreground"
              : "border-border bg-surface-softer hover:border-border-strong hover:bg-surface-soft",
        )}
      >
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium text-foreground">
            {title}
          </div>
          {time ? (
            <div
              className="truncate app-text-10 text-muted-foreground"
              title={time.title}
            >
              {time.relative}
            </div>
          ) : null}
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
        <SidebarStateIcon spec={statusIcon} />
      </button>
      <div className="absolute right-1 top-1/2 flex -translate-y-1/2 items-center gap-0.5 opacity-0 transition focus-within:opacity-100 group-hover/session:opacity-100">
        <button
          type="button"
          aria-label={renameLabels.rename}
          title={renameLabels.rename}
          onClick={(event) => {
            event.stopPropagation();
            onStartRename(session.id, title);
          }}
          className="rounded-chip p-1 text-muted-foreground transition hover:bg-surface-soft hover:text-foreground focus-visible:opacity-100"
        >
          <PencilIcon size={12} />
        </button>
        {showMenu && actionLabels ? (
          <SessionRowMenu
            actionLabels={actionLabels}
            archived={archived}
            onArchive={onArchive}
            onDelete={onDelete}
            onFork={onFork}
            onRestore={onRestore}
            sessionId={session.id}
          />
        ) : null}
      </div>
    </div>
  );
}
