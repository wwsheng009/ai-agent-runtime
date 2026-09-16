// Phase 1（合并方案 §3.6）：目录组头的公共呈现件。
// 由 sessions-section.tsx 的跨组落点组头与 directories-section.tsx 的注册目录行合并而来：
// 「标签 / 计数 / 宿主缺失告警 / 跨组落点 / hover 动作槽」五种元素在这里一次成型，
// 两个分区（以及合并后的单段）共用，避免三处各写一套组头。

import {
  ChevronDownIcon,
  FolderIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { type DragEvent, type ReactNode } from "react";

import { cn } from "@/lib/utils";

export type SidebarDirectoryGroupHeaderDrop = {
  active: boolean;
  onDragLeave: (event: DragEvent<HTMLElement>) => void;
  onDragOver: (event: DragEvent<HTMLElement>) => void;
  onDrop: (event: DragEvent<HTMLElement>) => void;
};

export type WorkspaceSidebarDirectoryGroupHeaderProps = {
  /** hover 动作槽（新会话 / 重命名 / 移除）：仅注册目录传入，派生目录留空。 */
  actions?: ReactNode;
  /** 展开态；用于箭头朝向与 `aria-expanded`。 */
  isOpen: boolean;
  /** 目录显示名（派生目录用标签，未归属用本地化文案）。 */
  label: string;
  /** 宿主缺失告警文案；缺省不渲染告警。 */
  missingLabel?: string;
  onToggle: () => void;
  /** 注册目录（有注册表条目）用卡片态，派生目录用轻量态。 */
  registered: boolean;
  sessionCount: number;
  /** 跨组落点（仅「按目录」视图的会话组头传入）：事件与高亮一起给。 */
  drop?: SidebarDirectoryGroupHeaderDrop;
  /** 落点钩子（跨组拖拽回归用例按它定位组头）；缺省不渲染。 */
  testId?: string;
  /** 悬浮提示：完整路径；缺省回落 label。 */
  title?: string;
};

export function WorkspaceSidebarDirectoryGroupHeader({
  actions,
  drop,
  isOpen,
  label,
  missingLabel,
  onToggle,
  registered,
  sessionCount,
  testId,
  title,
}: WorkspaceSidebarDirectoryGroupHeaderProps) {
  return (
    <div
      className={cn(
        // 2026-09-15 样式优化：组头左内边距收到 4px（原 6px），目录行与会话行
        // 共用同一左边界并尽量贴齐侧栏左缘，把宽度让给目录标题。
        "group/directory-row flex w-full items-center gap-1 rounded-[0.72rem] px-1 py-1 transition",
        registered
          ? "border border-border bg-surface-softer hover:border-border-strong hover:bg-surface-soft"
          : "hover:bg-surface-softer",
      )}
    >
      <button
        type="button"
        title={title || label}
        aria-expanded={isOpen}
        onClick={onToggle}
        data-testid={testId}
        data-drop-active={drop ? drop.active : undefined}
        onDragLeave={drop?.onDragLeave}
        onDragOver={drop?.onDragOver}
        onDrop={drop?.onDrop}
        className={cn(
          "flex min-w-0 flex-1 items-center gap-2 rounded-[0.6rem] py-0.5 text-left transition",
          drop?.active
            ? "text-foreground"
            : registered
              ? "text-foreground"
              : "text-muted-foreground",
        )}
      >
        <FolderIcon
          size={13}
          className={cn(
            "shrink-0",
            registered ? "text-accent-primary" : "text-muted-foreground",
          )}
        />
        <span className="min-w-0 flex-1 truncate text-xs font-medium">
          {label}
        </span>
        {missingLabel ? (
          <span
            title={missingLabel}
            aria-label={missingLabel}
            className="shrink-0 text-accent-orange"
          >
            <TriangleAlertIcon size={12} />
          </span>
        ) : null}
        <span className="shrink-0 app-text-10 text-muted-foreground">
          {sessionCount}
        </span>
        <ChevronDownIcon
          size={13}
          className={cn(
            "shrink-0 text-muted-foreground transition-transform duration-200",
            isOpen ? "rotate-0" : "-rotate-90",
          )}
        />
      </button>
      {/*
        2026-09-16 样式优化：动作槽**恒定位**。未绑定目录（无注册表条目、无派生动作）
        过去整槽缺位，组头的计数徽标与折叠箭头因此比其它组头右移一段，行与行对不齐；
        这里把槽位宽度锁到一枚动作图标的宽度（12px 图标 + `p-1` = 1.25rem = `min-w-5`），
        有动作时仍按内容增长，不挤占目录标题。
      */}
      <div
        data-testid="sidebar-directory-actions-slot"
        className="flex min-w-5 shrink-0 items-center justify-end gap-0.5 opacity-0 transition group-hover/directory-row:opacity-100 focus-within:opacity-100"
      >
        {actions}
      </div>
    </div>
  );
}
