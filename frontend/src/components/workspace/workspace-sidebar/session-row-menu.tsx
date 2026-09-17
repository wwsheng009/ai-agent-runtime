// 由 session-item.tsx 机械拆分而来（P0-2 行数约束），仅搬迁不改语义。
// P1-9 增量：带键盘导航 / aria 的会话行操作菜单（Ark 风格：ArrowKeys / Home / End / Esc）。
// 2026-09-15 样式优化：重命名从行内铅笔收敛为菜单首项。
// §4.8（多会话并发）：后台会话也能就地停 —— 不依赖本地 controller，按会话投递 interrupt。

import { MoreHorizontalIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";

export type SidebarSessionItemActionLabels = {
  archivedBadge: string;
  archive: string;
  delete: string;
  fork: string;
  menu: string;
  restore: string;
  /** §4.8：停止在途回合（仅「运行中 / 等待类」会话渲染）。 */
  stop?: string;
};

export type SessionRowMenuProps = {
  actionLabels: SidebarSessionItemActionLabels;
  archived: boolean;
  canStop?: boolean;
  onArchive?: (sessionId: string) => void;
  onDelete?: (sessionId: string) => void;
  onFork?: (sessionId: string) => void;
  /** 2026-09-15 样式优化：行内铅笔按钮并入菜单，作为首个菜单项。 */
  onRename?: (() => void) | undefined;
  onRestore?: (sessionId: string) => void;
  onStop?: (sessionId: string) => void;
  /** 重命名菜单项文案（缺省不渲染该项）。 */
  renameLabel?: string | undefined;
  sessionId: string;
};

export function SessionRowMenu({
  actionLabels,
  archived,
  canStop,
  onArchive,
  onDelete,
  onFork,
  onRename,
  onRestore,
  onStop,
  renameLabel,
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
          className="absolute right-0 top-full z-20 mt-1 min-w-[9rem] rounded-[0.6rem] border border-border bg-surface-popover py-1 shadow-lg"
        >
          {/* 2026-09-15 样式优化：重命名从行内铅笔收敛为菜单首项，标题右侧只留一个入口。 */}
          {onRename && renameLabel ? (
            <button
              type="button"
              role="menuitem"
              tabIndex={-1}
              onClick={() => {
                onRename();
                closeMenu(true);
              }}
              className="block w-full px-2.5 py-1.5 text-left text-xs text-foreground transition hover:bg-surface-soft"
            >
              {renameLabel}
            </button>
          ) : null}
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
          {/* §4.8：后台会话也能就地停 —— 不依赖本地 controller，按会话投递 interrupt。 */}
          {!archived && canStop && onStop && actionLabels.stop ? (
            <button
              type="button"
              role="menuitem"
              tabIndex={-1}
              data-testid={`session-stop-${sessionId}`}
              onClick={() => {
                onStop(sessionId);
                closeMenu(true);
              }}
              className="block w-full px-2.5 py-1.5 text-left text-xs text-foreground transition hover:bg-surface-soft"
            >
              {actionLabels.stop}
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
