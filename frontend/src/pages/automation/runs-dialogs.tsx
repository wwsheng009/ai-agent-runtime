import {
  AlertCircleIcon,
  CheckCircle2Icon,
  Clock3Icon,
  FileTextIcon,
  XIcon,
} from "lucide-react";
import type { TFunction } from "i18next";
import {
  useCallback,
  useEffect,
  useId,
  useRef,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import { formatScheduleTimestamp } from "@/components/workspace/schedule-panel-shared";
import type { RuntimeScheduleJobRun } from "@/types/runtime";

const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled]):not([type='hidden'])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "[tabindex]:not([tabindex='-1'])",
].join(",");

function useRunsDialogFocus(open: boolean, onClose: () => void) {
  const panelRef = useRef<HTMLDivElement>(null);
  const openerRef = useRef<HTMLElement | null>(null);

  useDialogLifecycle(open, onClose);

  useEffect(() => {
    if (!open) {
      return;
    }
    openerRef.current =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    panelRef.current?.focus();
    return () => {
      openerRef.current?.focus();
    };
  }, [open]);

  const handlePanelKeyDown = useCallback(
    (event: ReactKeyboardEvent<HTMLDivElement>) => {
      if (event.key !== "Tab") {
        return;
      }
      const panel = panelRef.current;
      if (!panel) {
        return;
      }
      const focusable = Array.from(
        panel.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR),
      );
      if (focusable.length === 0) {
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      const active = document.activeElement;
      if (event.shiftKey) {
        if (active === panel || active === first || !panel.contains(active)) {
          event.preventDefault();
          last.focus();
        }
        return;
      }
      if (active === last || !panel.contains(active)) {
        event.preventDefault();
        first.focus();
      }
    },
    [],
  );

  return { handlePanelKeyDown, panelRef };
}

function runErrorMessage(
  run: RuntimeScheduleJobRun,
  t: TFunction<"workspace">,
): string {
  if (run.errorCode === "headless_executor_unavailable") {
    return t("panels.schedule.errorCode.headless_executor_unavailable");
  }
  return run.error || t("panels.schedule.runFailure");
}

export type RunDetailsDialogProps = {
  duration: string | null;
  onClose: () => void;
  run: RuntimeScheduleJobRun | null;
  taskTitle: string;
};

export function RunDetailsDialog({
  duration,
  onClose,
  run,
  taskTitle,
}: RunDetailsDialogProps) {
  const { t } = useTranslation("workspace");
  const titleId = useId();
  const open = run !== null;
  const { handlePanelKeyDown, panelRef } = useRunsDialogFocus(open, onClose);

  if (!run || typeof document === "undefined") {
    return null;
  }

  const hasError = Boolean(run.error || run.errorCode);
  return createPortal(
    <DialogOverlay className="z-[120] backdrop-blur-sm" onDismiss={onClose}>
      <DialogPanel
        aria-labelledby={titleId}
        aria-modal="true"
        className="max-h-[calc(100dvh-2rem)] max-w-2xl"
        data-testid="automation-run-details-dialog"
        elevation="lg"
        onKeyDown={handlePanelKeyDown}
        ref={panelRef}
        role="dialog"
        tabIndex={-1}
      >
        <header className="flex items-start gap-3 border-b border-border bg-surface-softer px-4 py-3.5 sm:px-5">
          <span
            className={
              hasError
                ? "grid size-9 shrink-0 place-items-center rounded-panel border border-destructive/30 bg-destructive/10 text-destructive"
                : "grid size-9 shrink-0 place-items-center rounded-panel border border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300"
            }
          >
            {hasError ? (
              <AlertCircleIcon aria-hidden="true" size={17} />
            ) : (
              <CheckCircle2Icon aria-hidden="true" size={17} />
            )}
          </span>
          <div className="min-w-0 flex-1">
            <h2
              className="truncate text-[15px] font-semibold tracking-[-0.02em] text-foreground"
              id={titleId}
            >
              {t("panels.schedule.runs.detailsTitle")}
            </h2>
            <p className="mt-0.5 truncate text-xs text-muted-foreground">
              {taskTitle} · #{run.id}
            </p>
          </div>
          <Button
            aria-label={t("panels.schedule.runs.closeDetails")}
            onClick={onClose}
            size="icon"
            title={t("panels.schedule.runs.closeDetails")}
            variant="ghost"
          >
            <XIcon aria-hidden="true" size={16} />
          </Button>
        </header>

        <div className="app-scrollbar min-h-0 flex-1 overflow-y-auto px-4 py-4 sm:px-5">
          <dl className="grid gap-3 rounded-panel border border-border bg-surface-softer p-3 text-xs sm:grid-cols-2">
            <div>
              <dt className="text-muted-foreground">
                {t("panels.schedule.runs.task")}
              </dt>
              <dd className="mt-1 break-words font-medium text-foreground">
                {taskTitle}
              </dd>
            </div>
            <div>
              <dt className="text-muted-foreground">
                {t("panels.schedule.runs.runId")}
              </dt>
              <dd className="mt-1 font-mono text-foreground">#{run.id}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">
                {t("panels.schedule.runs.started")}
              </dt>
              <dd className="mt-1 text-foreground">
                {formatScheduleTimestamp(run.startedAt)}
              </dd>
            </div>
            <div>
              <dt className="text-muted-foreground">
                {t("panels.schedule.runs.finished")}
              </dt>
              <dd className="mt-1 text-foreground">
                {formatScheduleTimestamp(run.finishedAt)}
              </dd>
            </div>
            {duration ? (
              <div>
                <dt className="text-muted-foreground">
                  {t("panels.schedule.runs.duration")}
                </dt>
                <dd className="mt-1 text-foreground">{duration}</dd>
              </div>
            ) : null}
            {run.sessionId ? (
              <div className="min-w-0">
                <dt className="text-muted-foreground">
                  {t("panels.schedule.runs.session")}
                </dt>
                <dd className="mt-1 break-words font-mono text-foreground">
                  {run.sessionId}
                </dd>
              </div>
            ) : null}
          </dl>

          <section className="mt-4">
            <h3 className="flex items-center gap-2 text-xs font-semibold text-foreground">
              <FileTextIcon aria-hidden="true" size={14} />
              {t("panels.schedule.runOutput")}
            </h3>
            {run.output ? (
              <pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap [overflow-wrap:anywhere] rounded-control border border-border bg-surface-solid p-3 font-sans text-xs leading-5 text-muted-foreground">
                {run.output}
              </pre>
            ) : (
              <p className="mt-2 rounded-control border border-dashed border-border px-3 py-3 text-xs text-muted-foreground">
                {t("panels.schedule.runs.noOutput")}
              </p>
            )}
          </section>

          {hasError ? (
            <section className="mt-4 rounded-control border border-destructive/30 bg-destructive/5 p-3 text-xs text-destructive">
              <h3 className="flex items-center gap-2 font-semibold">
                <AlertCircleIcon aria-hidden="true" size={14} />
                {t("panels.schedule.runError")}
              </h3>
              <p className="mt-1 leading-5">{runErrorMessage(run, t)}</p>
              {run.errorCode ? (
                <p className="mt-2 font-mono text-[11px] opacity-80">
                  {run.errorCode}
                </p>
              ) : null}
            </section>
          ) : null}
        </div>
      </DialogPanel>
    </DialogOverlay>,
    document.body,
  );
}

export type ClearRunsDialogProps = {
  allTasks: boolean;
  busy: boolean;
  error: boolean;
  onCancel: () => void;
  onConfirm: () => void;
  open: boolean;
  taskTitle: string;
};

export function ClearRunsDialog({
  allTasks,
  busy,
  error,
  onCancel,
  onConfirm,
  open,
  taskTitle,
}: ClearRunsDialogProps) {
  const { t } = useTranslation("workspace");
  const titleId = useId();
  const { handlePanelKeyDown, panelRef } = useRunsDialogFocus(open, onCancel);

  if (!open || typeof document === "undefined") {
    return null;
  }

  return createPortal(
    <DialogOverlay className="z-[130] backdrop-blur-sm" onDismiss={onCancel}>
      <DialogPanel
        aria-labelledby={titleId}
        aria-modal="true"
        className="max-w-md"
        data-testid="automation-clear-runs-dialog"
        elevation="lg"
        onKeyDown={handlePanelKeyDown}
        ref={panelRef}
        role="dialog"
        tabIndex={-1}
      >
        <div className="p-4 sm:p-5">
          <div className="flex items-start gap-3">
            <span className="grid size-9 shrink-0 place-items-center rounded-panel border border-accent-orange/30 bg-accent-orange/10 text-accent-orange">
              <Clock3Icon aria-hidden="true" size={17} />
            </span>
            <div className="min-w-0">
              <h2
                className="text-[15px] font-semibold tracking-[-0.02em] text-foreground"
                id={titleId}
              >
                {allTasks
                  ? t("panels.schedule.runs.confirmClearAllTitle")
                  : t("panels.schedule.runs.confirmClearTitle")}
              </h2>
              <p className="mt-2 text-sm leading-6 text-muted-foreground">
                {allTasks
                  ? t("panels.schedule.runs.confirmClearAllMessage")
                  : t("panels.schedule.runs.confirmClearMessage", {
                      taskTitle,
                    })}
              </p>
            </div>
          </div>
          {error ? (
            <p className="mt-3 rounded-field border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs leading-5 text-destructive" role="alert">
              {t("panels.schedule.runs.clearFailed")}
            </p>
          ) : null}
          <div className="mt-5 flex justify-end gap-2">
            <Button disabled={busy} onClick={onCancel} variant="ghost">
              {t("panels.schedule.cancel")}
            </Button>
            <Button
              disabled={busy}
              onClick={onConfirm}
              variant="destructive"
            >
              {busy
                ? t("panels.schedule.runs.clearing")
                : allTasks
                  ? t("panels.schedule.runs.confirmClearAll")
                  : t("panels.schedule.runs.confirmClear")}
            </Button>
          </div>
        </div>
      </DialogPanel>
    </DialogOverlay>,
    document.body,
  );
}
