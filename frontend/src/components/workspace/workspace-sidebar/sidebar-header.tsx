// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { MessageSquarePlusIcon, SearchIcon, Settings2Icon, SparklesIcon, XIcon } from "lucide-react";
import { Link } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { NEW_THREAD_ID } from "@/hooks/workspace/use-workspace-thread-selection";
import { cn } from "@/lib/utils";
import { type Dispatch, type SetStateAction } from "react";
import { type TFunction } from "i18next";

type WorkspaceSidebarHeaderProps = {
  isCompact: boolean;
  onCloseMobile?: () => void;
  onOpenSettings?: () => void;
  onRefreshRuntimeTeams?: () => void;
  onSelectThread: (threadId: string) => void;
  query: string;
  selectedThreadId: string;
  setQuery: Dispatch<SetStateAction<string>>;
  showSearch: boolean;
  t: TFunction<"workspace">;
};

export function WorkspaceSidebarHeader({
  isCompact,
  onCloseMobile,
  onOpenSettings,
  onRefreshRuntimeTeams,
  onSelectThread,
  query,
  selectedThreadId,
  setQuery,
  showSearch,
  t,
}: WorkspaceSidebarHeaderProps) {
  return (
    <div
      className={cn(
        "border-b border-[var(--border)]",
        isCompact ? "px-2.5 py-2.5" : "px-3 py-3",
      )}
    >
      <div className="flex items-center justify-between gap-3">
        <Link to="/" className="flex items-center gap-3" onClick={onCloseMobile}>
          <span className="grid size-8 place-items-center rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-soft)] text-xs font-semibold text-[var(--accent-primary)]">
            AR
          </span>
          <div>
            <div className="app-text-10 uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
              {t("sidebar.workspaceLabel")}
            </div>
            <div className="mt-0.5 text-sm font-semibold">{t("sidebar.appName")}</div>
          </div>
        </Link>
        <div className="flex items-center gap-1">
          <Button
            variant="ghost"
            size="icon"
            className="xl:hidden"
            onClick={onCloseMobile}
            aria-label={t("sidebar.closeNavigation")}
            title={t("sidebar.closeNavigation")}
          >
            <XIcon size={16} />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            onClick={onRefreshRuntimeTeams}
            disabled={!onRefreshRuntimeTeams}
            aria-label={t("sidebar.refreshRuntimeTeams")}
          >
            <SparklesIcon size={16} />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            onClick={onOpenSettings}
            aria-label={t("sidebar.openSettings")}
          >
            <Settings2Icon size={16} />
          </Button>
        </div>
      </div>

      <button
        type="button"
        onClick={() => onSelectThread(NEW_THREAD_ID)}
        className={cn(
          "mt-3 flex w-full items-center justify-center gap-2 rounded-[0.85rem] border px-3 text-base font-medium transition",
          isCompact ? "py-2" : "py-2.5",
          selectedThreadId === NEW_THREAD_ID
            ? "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)] text-[var(--foreground)]"
            : "border-[var(--border)] bg-[var(--surface-softer)] text-[var(--foreground)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
        )}
      >
        <MessageSquarePlusIcon
          size={16}
          className="text-[var(--accent-primary)]"
        />
        {t("sidebar.startNewChat")}
      </button>

      {showSearch ? (
        <div
          className={cn(
            "mt-2.5 rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3",
            isCompact ? "py-2" : "py-2.5",
          )}
        >
          <div className="flex items-center gap-2.5 text-sm text-[var(--muted-foreground)]">
            <SearchIcon size={15} />
            <input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t("sidebar.searchPlaceholder")}
              aria-label={t("sidebar.searchPlaceholder")}
              className="w-full bg-transparent outline-none"
            />
          </div>
        </div>
      ) : null}
    </div>
  );
}
