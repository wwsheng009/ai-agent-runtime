import { XIcon } from "lucide-react";
import { lazy, Suspense, useEffect } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

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
  const { t } = useTranslation("workspace");

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
      className="fixed inset-0 z-[120] flex items-center justify-center bg-dialog-backdrop px-3 py-4 backdrop-blur-sm"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onClose();
        }
      }}
    >
      <div className="flex max-h-[calc(100vh-1.5rem)] w-full max-w-7xl flex-col overflow-hidden rounded-panel border border-border [background:var(--dialog-bg)] shadow-[0_12px_36px_rgba(0,0,0,0.22)]">
        <div className="flex items-start justify-between gap-3 border-b border-border px-3.5 py-3 sm:px-4">
          <div>
            <div className="app-text-11 uppercase tracking-[0.16em] text-accent-secondary">
              {t("panels.teamsDispatch.dialog.eyebrow")}
            </div>
            <h2 className="mt-1 text-lg font-semibold tracking-[-0.03em] text-foreground">
              {t("panels.teamsDispatch.dialog.title")}
            </h2>
            <p className="mt-1 max-w-2xl text-sm leading-6 text-muted-foreground">
              {t("panels.teamsDispatch.dialog.description")}
            </p>
          </div>
          <Button
            variant="ghost"
            size="icon"
            onClick={onClose}
            aria-label={t("panels.teamsDispatch.dialog.closeAriaLabel")}
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
  const { t } = useTranslation("workspace");

  return (
    <div className="rounded-panel border border-border bg-surface-softer px-3.5 py-2.5 text-sm text-muted-foreground">
      {t("panels.teamsDispatch.dialog.loading")}
    </div>
  );
}
