import { XIcon } from "lucide-react";
import { lazy, Suspense, useEffect } from "react";
import { createPortal } from "react-dom";

import { Button } from "@/components/ui/button";
import {
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
} from "@/lib/runtime-api";

const RuntimeTeams = lazy(() =>
  import("@/components/workspace/runtime-teams").then((module) => ({
    default: module.RuntimeTeams,
  })),
);

type RuntimeTeamsDialogProps = {
  error: string | null;
  isLoading: boolean;
  isRefreshing?: boolean;
  onClose: () => void;
  onRefresh?: () => void;
  open: boolean;
  summaries: RuntimeTeamSummaryEntry[];
  teams: RuntimeTeamRecord[];
};

export function RuntimeTeamsDialog({
  error,
  isLoading,
  isRefreshing,
  onClose,
  onRefresh,
  open,
  summaries,
  teams,
}: RuntimeTeamsDialogProps) {
  useEffect(() => {
    if (!open || typeof document === "undefined") {
      return;
    }

    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    return () => {
      document.body.style.overflow = previousOverflow;
    };
  }, [open]);

  useEffect(() => {
    if (!open) {
      return;
    }

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [onClose, open]);

  if (!open) {
    return null;
  }

  if (typeof document === "undefined") {
    return null;
  }

  return createPortal(
    <div
      className="fixed inset-0 z-[120] flex items-center justify-center bg-[var(--dialog-backdrop)] px-3 py-4 backdrop-blur-sm"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onClose();
        }
      }}
    >
      <div className="flex max-h-[calc(100vh-1.5rem)] w-full max-w-7xl flex-col overflow-hidden rounded-[0.9rem] border border-[var(--border)] [background:var(--dialog-bg)] shadow-[0_12px_36px_rgba(0,0,0,0.22)]">
        <div className="flex items-start justify-between gap-3 border-b border-[var(--border)] px-3.5 py-3 sm:px-4">
          <div>
            <div className="app-text-11 uppercase tracking-[0.16em] text-[var(--accent-secondary)]">
              Runtime teams
            </div>
            <h2 className="mt-1 text-lg font-semibold tracking-[-0.03em] text-[var(--foreground)]">
              团队详情
            </h2>
            <p className="mt-1 max-w-2xl text-sm leading-6 text-[var(--muted-foreground)]">
              这里集中显示调度、团队状态、任务、事件、邮箱和路径声明，避免把左侧栏变成长表单。
            </p>
          </div>
          <Button
            variant="ghost"
            size="icon"
            onClick={onClose}
            aria-label="关闭运行团队详情"
          >
            <XIcon size={16} />
          </Button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto px-3.5 py-3.5 sm:px-4">
          <Suspense fallback={<RuntimeTeamsDialogContentFallback />}>
            <RuntimeTeams
              className="mt-0"
              error={error}
              isLoading={isLoading}
              isRefreshing={isRefreshing}
              onRefresh={onRefresh}
              showHeader={false}
              summaries={summaries}
              teams={teams}
            />
          </Suspense>
        </div>
      </div>
    </div>,
    document.body,
  );
}

function RuntimeTeamsDialogContentFallback() {
  return (
    <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3.5 py-2.5 text-sm text-[var(--muted-foreground)]">
      正在加载 runtime teams 内容…
    </div>
  );
}
