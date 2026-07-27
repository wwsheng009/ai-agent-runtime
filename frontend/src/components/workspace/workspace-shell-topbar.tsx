import {
  ChartNoAxesCombinedIcon,
  DatabaseIcon,
  MessageSquarePlusIcon,
  PanelLeftOpenIcon,
  PanelRightCloseIcon,
  PanelRightOpenIcon,
  Settings2Icon,
  TerminalSquareIcon,
} from "lucide-react";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { buttonVariants } from "@/components/ui/button-variants";
import { Button } from "@/components/ui/button";
import { type Thread } from "@/data/mock";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

type WorkspaceShellTopbarProps = {
  artifactRailOpen: boolean;
  density: "comfortable" | "compact";
  isNewThread?: boolean;
  liveTeamCount: number;
  onOpenSidebar: () => void;
  onOpenSettings: () => void;
  onToggleArtifactRail: () => void;
  selectedThread: Thread;
  threadStatusLabel: string;
  transportLabel: string;
  threadSubtitle: string;
};

export function WorkspaceShellTopbar({
  artifactRailOpen,
  density,
  isNewThread = false,
  liveTeamCount,
  onOpenSidebar,
  onOpenSettings,
  onToggleArtifactRail,
  selectedThread,
  threadSubtitle,
  threadStatusLabel,
  transportLabel,
}: WorkspaceShellTopbarProps) {
  const isCompact = density === "compact";
  const { t } = useTranslation("workspace");

  return (
    <header className="absolute inset-x-0 top-0 z-30 flex justify-center px-3 pt-1.5 sm:px-4">
      <div
        className={cn(
          "flex w-full max-w-[72rem] items-center gap-1 rounded-[0.9rem] border border-[var(--border)] bg-[var(--workspace-topbar-bg)] shadow-[0_8px_24px_rgba(0,0,0,0.14)] backdrop-blur-lg sm:gap-2",
          isCompact ? "h-10 px-3" : "h-11 px-3.5",
        )}
      >
        <Button
          variant="ghost"
          size="icon"
          className="shrink-0 xl:hidden"
          onClick={onOpenSidebar}
          aria-label={t("topbar.openSidebar")}
          title={t("topbar.openSidebar")}
        >
          <PanelLeftOpenIcon size={16} />
        </Button>
        {!isNewThread ? (
          <Link
            to="/workspace/chats/new"
            className={cn(
              buttonVariants({ variant: "ghost", size: "icon" }),
              "shrink-0 xl:hidden",
            )}
            aria-label={t("topbar.newChat")}
            title={t("topbar.newChat")}
          >
            <MessageSquarePlusIcon size={15} />
          </Link>
        ) : null}
        <div className="min-w-0 flex-1">
          <div className="truncate app-text-13 font-semibold tracking-[-0.02em]">
            {isNewThread ? t("topbar.newThreadTitle") : selectedThread.title}
          </div>
          {!isNewThread ? (
            <div className="hidden truncate app-text-10 text-[var(--muted-foreground)] sm:block">
              {threadSubtitle}
            </div>
          ) : null}
        </div>
        {!isNewThread ? (
          <div className="hidden items-center gap-2.5 md:flex">
            <Badge>{threadStatusLabel}</Badge>
          <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            {transportLabel}
          </div>
          {liveTeamCount > 0 ? (
            <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              {t("sidebar.active", { count: liveTeamCount })}
            </div>
          ) : null}
          </div>
        ) : null}
        <Link
          to="/logs"
          className={cn(buttonVariants({ variant: "ghost", size: "icon" }), "shrink-0")}
          aria-label={t("topbar.logs")}
          title={t("topbar.logs")}
        >
          <TerminalSquareIcon size={16} />
        </Link>
        <Link
          to="/usage"
          className={cn(buttonVariants({ variant: "ghost", size: "icon" }), "shrink-0")}
          aria-label={t("topbar.usage")}
          title={t("topbar.usage")}
        >
          <ChartNoAxesCombinedIcon size={16} />
        </Link>
        <Link
          to="/runtime/config"
          className={cn(buttonVariants({ variant: "ghost", size: "icon" }), "shrink-0")}
          aria-label={t("topbar.runtime")}
          title={t("topbar.runtime")}
        >
          <DatabaseIcon size={16} />
        </Link>
        <Button
          variant="ghost"
          size="sm"
          className="shrink-0"
          onClick={onOpenSettings}
          aria-label={t("topbar.settings")}
          title={t("topbar.settings")}
        >
          <Settings2Icon size={16} />
        </Button>
        {!isNewThread ? (
          <Button
            variant="ghost"
            size="sm"
            className="shrink-0"
            onClick={onToggleArtifactRail}
            aria-label={artifactRailOpen ? t("topbar.hideFiles") : t("topbar.showFiles")}
            title={artifactRailOpen ? t("topbar.hideFiles") : t("topbar.showFiles")}
          >
            {artifactRailOpen ? (
              <PanelRightCloseIcon size={16} />
            ) : (
              <PanelRightOpenIcon size={16} />
            )}
          </Button>
        ) : null}
      </div>
    </header>
  );
}
