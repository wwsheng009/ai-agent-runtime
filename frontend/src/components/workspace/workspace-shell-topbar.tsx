import {
  DatabaseIcon,
  HomeIcon,
  MessageSquarePlusIcon,
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
import { getThreadTopbarSubtitle } from "@/components/workspace/workspace-shell-shared";

type WorkspaceShellTopbarProps = {
  artifactRailOpen: boolean;
  density: "comfortable" | "compact";
  isNewThread?: boolean;
  liveTeamCount: number;
  onOpenSettings: () => void;
  onToggleArtifactRail: () => void;
  selectedThread: Thread;
  threadStatusLabel: string;
  transportLabel: string;
};

export function WorkspaceShellTopbar({
  artifactRailOpen,
  density,
  isNewThread = false,
  liveTeamCount,
  onOpenSettings,
  onToggleArtifactRail,
  selectedThread,
  threadStatusLabel,
  transportLabel,
}: WorkspaceShellTopbarProps) {
  const isCompact = density === "compact";

  return (
    <header className="absolute inset-x-0 top-0 z-30 flex justify-center px-3 pt-1.5 sm:px-4">
      <div
        className={cn(
          "flex w-full max-w-[72rem] items-center gap-2.5 rounded-[0.9rem] border border-[var(--border)] bg-[var(--workspace-topbar-bg)] shadow-[0_8px_24px_rgba(0,0,0,0.14)] backdrop-blur-lg",
          isCompact ? "h-10 px-3" : "h-11 px-3.5",
        )}
      >
        <div className="flex items-center gap-2 xl:hidden">
          <Link
            to="/"
            className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
            aria-label="Back to landing"
          >
            <HomeIcon size={14} />
            <span className="hidden sm:inline">Home</span>
          </Link>
          {!isNewThread ? (
            <Link
              to="/workspace/chats/new"
              className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
            >
              <MessageSquarePlusIcon size={14} />
              <span className="hidden sm:inline">New chat</span>
            </Link>
          ) : null}
        </div>
        <div className="min-w-0 flex-1">
          <div className="truncate app-text-13 font-semibold tracking-[-0.02em]">
            {isNewThread ? "New chat" : selectedThread.title}
          </div>
          <div className="truncate app-text-10 text-[var(--muted-foreground)]">
            {isNewThread
              ? "Start a thread, then let runtime state attach as work begins."
              : getThreadTopbarSubtitle(selectedThread, transportLabel)}
          </div>
        </div>
        <div className="hidden items-center gap-2.5 md:flex">
          <Badge>{threadStatusLabel}</Badge>
          <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            {transportLabel}
          </div>
          {liveTeamCount > 0 ? (
            <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              {liveTeamCount} live teams
            </div>
          ) : null}
        </div>
        <Link
          to="/logs"
          className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
        >
          <TerminalSquareIcon size={16} />
          <span className="hidden sm:inline">Logs</span>
        </Link>
        <Link
          to="/runtime/config"
          className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
        >
          <DatabaseIcon size={16} />
          <span className="hidden sm:inline">Runtime</span>
        </Link>
        <Button
          variant="ghost"
          size="sm"
          className="px-2.5"
          onClick={onOpenSettings}
          aria-label="Open settings"
        >
          <Settings2Icon size={16} />
          <span className="hidden sm:inline">Settings</span>
        </Button>
        {!isNewThread ? (
          <Button
            variant="ghost"
            size="sm"
            className="px-2.5"
            onClick={onToggleArtifactRail}
            aria-label={artifactRailOpen ? "Hide artifact rail" : "Show artifact rail"}
          >
            {artifactRailOpen ? (
              <PanelRightCloseIcon size={16} />
            ) : (
              <PanelRightOpenIcon size={16} />
            )}
            <span className="hidden sm:inline">
              {artifactRailOpen ? "Hide files" : "Show files"}
            </span>
          </Button>
        ) : null}
      </div>
    </header>
  );
}
