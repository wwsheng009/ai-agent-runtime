// Phase 2（合并方案 §3.6）：目录组头动作的**单一入口**。
// 2026-09-15 样式优化（方案 §11）：原「新建会话 / 重命名 / 移除」三个并排图标按钮收敛为
// 一个 hover 才显示的菜单入口（`MoreHorizontalIcon` + `role="menu"`），把横向空间让给目录标题；
// 三个动作的文案、禁用/忙碌语义与点击回调逐字保留，只是从按钮变成菜单项。
// 键盘可达性沿用会话行菜单口径：`aria-haspopup` / `aria-expanded` / `ArrowUp` `ArrowDown`
// `Home` `End` `Escape` `Tab`，且宿主槽位的 `focus-within:opacity-100` 保证键盘路径不被隐藏。

import { LoaderCircleIcon, MoreHorizontalIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { type TFunction } from "i18next";

type WorkspaceSidebarDirectoryGroupActionsProps = {
  /** 该目录内正在新建会话：新会话菜单项禁用并换成旋转态。 */
  creating: boolean;
  onCreate: () => void;
  onRename: () => void;
  onRemove: () => void;
  t: TFunction<"workspace">;
};

export function WorkspaceSidebarDirectoryGroupActions({
  creating,
  onCreate,
  onRename,
  onRemove,
  t,
}: WorkspaceSidebarDirectoryGroupActionsProps) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const menuLabel = t("sidebar.directories.actions");

  useEffect(() => {
    if (!open) {
      return;
    }
    // 打开即聚焦首个可用项：禁用项（忙碌中的新建）不能吃掉焦点。
    menuRef.current
      ?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not([disabled])')
      .item(0)
      ?.focus();
  }, [open]);

  function closeMenu(focusTrigger: boolean) {
    setOpen(false);
    if (focusTrigger) {
      triggerRef.current?.focus();
    }
  }

  function moveFocus(delta: number | "first" | "last") {
    const items = Array.from(
      menuRef.current?.querySelectorAll<HTMLButtonElement>(
        '[role="menuitem"]:not([disabled])',
      ) ?? [],
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

  const itemClassName =
    "flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left text-xs text-foreground transition hover:bg-surface-soft disabled:opacity-50";

  return (
    <div
      className="relative"
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
        aria-haspopup="menu"
        aria-label={menuLabel}
        title={menuLabel}
        onClick={() => setOpen((current) => !current)}
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
          aria-label={menuLabel}
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
          <button
            type="button"
            role="menuitem"
            tabIndex={-1}
            disabled={creating}
            title={t("sidebar.directories.newChat")}
            onClick={() => {
              onCreate();
              closeMenu(true);
            }}
            className={itemClassName}
          >
            {creating ? (
              <LoaderCircleIcon size={12} className="animate-spin" />
            ) : null}
            {t("sidebar.directories.newChat")}
          </button>
          <button
            type="button"
            role="menuitem"
            tabIndex={-1}
            onClick={() => {
              onRename();
              closeMenu(true);
            }}
            className={itemClassName}
          >
            {t("sidebar.directories.rename")}
          </button>
          <button
            type="button"
            role="menuitem"
            tabIndex={-1}
            onClick={() => {
              onRemove();
              closeMenu(true);
            }}
            className={`${itemClassName} text-muted-foreground hover:text-accent-orange`}
          >
            {t("sidebar.directories.deleteTitle")}
          </button>
        </div>
      ) : null}
    </div>
  );
}
