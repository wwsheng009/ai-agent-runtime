// 左侧栏收起态（桌面 xl+）：整列只剩图标入口 —— 展开、新建聊天、三个分区、搜索、设置。
// 分区图标点击后展开侧栏并保证该段处于展开态；移动抽屉不渲染本件（由 workspace-sidebar 控制）。

import {
  CompassIcon,
  FolderIcon,
  MessagesSquareIcon,
  MessageSquarePlusIcon,
  PanelLeftOpenIcon,
  SearchIcon,
  Settings2Icon,
  type LucideIcon,
} from "lucide-react";
import { type TFunction } from "i18next";

import { Button } from "@/components/ui/button";
import { NEW_THREAD_ID } from "@/hooks/workspace/use-workspace-thread-selection";
import { cn } from "@/lib/utils";

import { type SidebarSectionId } from "./types";

type WorkspaceSidebarCollapsedRailProps = {
  onCollapsedChange?: (collapsed: boolean) => void;
  onOpenSessionSearch?: () => void;
  onOpenSettings?: () => void;
  onSelectSection: (section: SidebarSectionId) => void;
  onSelectThread: (threadId: string) => void;
  selectedThreadId: string;
  t: TFunction<"workspace">;
};

/** 图标列里的分区入口：顺序与展开态四段装配一致（目录树 → 本地聊天 → 运行时概览）。 */
const SECTION_ENTRIES: ReadonlyArray<{
  id: SidebarSectionId;
  icon: LucideIcon;
  iconClassName: string;
}> = [
  { id: "directories", icon: FolderIcon, iconClassName: "text-accent-primary" },
  { id: "chats", icon: MessagesSquareIcon, iconClassName: "text-accent-primary" },
  { id: "runtime", icon: CompassIcon, iconClassName: "text-accent-secondary" },
];

export function WorkspaceSidebarCollapsedRail({
  onCollapsedChange,
  onOpenSessionSearch,
  onOpenSettings,
  onSelectSection,
  onSelectThread,
  selectedThreadId,
  t,
}: WorkspaceSidebarCollapsedRailProps) {
  return (
    <div
      className="hidden min-h-0 flex-1 flex-col items-center gap-1 px-2 py-3 xl:flex"
      data-testid="sidebar-collapsed-rail"
    >
      <Button
        aria-label={t("sidebar.expand")}
        className="mb-1"
        onClick={() => onCollapsedChange?.(false)}
        size="icon"
        title={t("sidebar.expand")}
        variant="ghost"
      >
        <PanelLeftOpenIcon size={16} />
      </Button>
      <Button
        aria-label={t("sidebar.startNewChat")}
        className={cn(
          selectedThreadId === NEW_THREAD_ID &&
            "bg-accent-primary-soft text-foreground",
        )}
        onClick={() => onSelectThread(NEW_THREAD_ID)}
        size="icon"
        title={t("sidebar.startNewChat")}
        variant="ghost"
      >
        <MessageSquarePlusIcon className="text-accent-primary" size={16} />
      </Button>

      <div className="my-1 h-px w-6 shrink-0 bg-border" />

      {SECTION_ENTRIES.map(({ id, icon: Icon, iconClassName }) => {
        const label = t(`sidebar.sections.${id}`);
        return (
          <Button
            key={id}
            aria-label={label}
            onClick={() => onSelectSection(id)}
            size="icon"
            title={label}
            variant="ghost"
          >
            <Icon className={iconClassName} size={16} />
          </Button>
        );
      })}

      <div className="min-h-0 flex-1" />

      {onOpenSessionSearch ? (
        <Button
          aria-label={t("panels.sessionSearch.trigger")}
          onClick={onOpenSessionSearch}
          size="icon"
          title={t("panels.sessionSearch.triggerHint")}
          variant="ghost"
        >
          <SearchIcon size={16} />
        </Button>
      ) : null}
      {onOpenSettings ? (
        <Button
          aria-label={t("sidebar.openSettings")}
          onClick={onOpenSettings}
          size="icon"
          title={t("sidebar.openSettings")}
          variant="ghost"
        >
          <Settings2Icon size={16} />
        </Button>
      ) : null}
    </div>
  );
}
