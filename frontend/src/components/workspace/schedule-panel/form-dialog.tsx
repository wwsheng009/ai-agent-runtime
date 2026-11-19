// P3：自动化任务（Schedule）新建/编辑对话框。
//
// 新建与编辑复用同一套分组字段和弹层生命周期。字段按「任务内容 / 执行上下文 /
// 运行规则」组织，避免任务列表被长表单挤压，并统一焦点、键盘和错误反馈。

import {
  AlertCircleIcon,
  CalendarClockIcon,
  LoaderCircleIcon,
  XIcon,
} from "lucide-react";
import {
  useCallback,
  useEffect,
  useId,
  useRef,
  type Dispatch,
  type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type SetStateAction,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";

import {
  ScheduleContentFields,
  ScheduleContextFields,
  ScheduleRuleFields,
} from "./form-dialog-fields";
import type { CreateFormState, ScheduleFormErrors } from "./form-state";

const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled]):not([type='hidden'])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "[tabindex]:not([tabindex='-1'])",
].join(",");

export type ScheduleFormDialogMode = "create" | "edit";

export type ScheduleFormDialogProps = {
  createForm: CreateFormState;
  busy: boolean;
  error: string | null;
  fieldErrors?: ScheduleFormErrors;
  mode: ScheduleFormDialogMode;
  onClearError: () => void;
  onClose: () => void;
  onSubmit: () => void;
  setCreateForm: Dispatch<SetStateAction<CreateFormState | null>>;
};

export function ScheduleFormDialog({
  busy,
  createForm,
  error,
  fieldErrors = {},
  mode,
  onClearError,
  onClose,
  onSubmit,
  setCreateForm,
}: ScheduleFormDialogProps) {
  const { t } = useTranslation("workspace");
  const titleId = useId();
  const descriptionId = useId();
  const panelRef = useRef<HTMLDivElement>(null);
  const titleInputRef = useRef<HTMLInputElement>(null);
  const errorRef = useRef<HTMLDivElement>(null);
  const openerRef = useRef<HTMLElement | null>(null);
  const lastErrorRef = useRef<string | null>(null);

  useDialogLifecycle(true, onClose);

  useEffect(() => {
    openerRef.current =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    titleInputRef.current?.focus();
    return () => {
      openerRef.current?.focus();
    };
  }, []);

  useEffect(() => {
    if (error && error !== lastErrorRef.current) {
      errorRef.current?.focus();
    }
    lastErrorRef.current = error;
  }, [error]);

  const patchForm = useCallback(
    (patch: Partial<CreateFormState>) => {
      onClearError();
      setCreateForm((form) => (form ? { ...form, ...patch } : form));
    },
    [onClearError, setCreateForm],
  );

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
        if (active === first || !panel.contains(active)) {
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

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy) {
      return;
    }
    onSubmit();
  };

  const editing = mode === "edit";
  const titleLabel = editing
    ? t("panels.schedule.editTitle")
    : t("panels.schedule.create");
  const description = editing
    ? t("panels.schedule.editDescription")
    : t("panels.schedule.createTitle");
  const closeLabel = editing
    ? t("panels.schedule.closeEdit")
    : t("panels.schedule.closeCreate");
  const footerHint = editing
    ? t("panels.schedule.editFooterHint")
    : t("panels.schedule.createFooterHint");
  const busyLabel = editing
    ? t("panels.schedule.saving")
    : t("panels.schedule.creating");
  const submitLabel = editing
    ? t("panels.schedule.save")
    : t("panels.schedule.createSubmit");

  if (typeof document === "undefined") {
    return null;
  }

  return createPortal(
    <DialogOverlay className="z-[120] backdrop-blur-sm" onDismiss={onClose}>
      <DialogPanel
        aria-describedby={descriptionId}
        aria-labelledby={titleId}
        aria-modal="true"
        className="max-h-[calc(100dvh-2rem)] max-w-3xl"
        data-mode={mode}
        data-testid={`schedule-${mode}-dialog`}
        elevation="lg"
        onKeyDown={handlePanelKeyDown}
        ref={panelRef}
        role="dialog"
      >
        <form
          className="flex min-h-0 flex-1 flex-col"
          noValidate
          onSubmit={handleSubmit}
        >
          <header className="flex items-start gap-3 border-b border-border bg-surface-softer px-4 py-4 sm:px-5">
            <span className="grid size-10 shrink-0 place-items-center rounded-panel border border-accent-primary-border bg-accent-primary-soft text-accent-primary shadow-[0_8px_24px_var(--accent-primary-shadow)]">
              <CalendarClockIcon aria-hidden="true" size={18} />
            </span>
            <div className="min-w-0 flex-1">
              <p className="app-text-11 uppercase tracking-[0.16em] text-accent-primary">
                {t("panels.schedule.title")}
              </p>
              <h2
                className="mt-1 text-lg font-semibold tracking-[-0.03em] text-foreground"
                id={titleId}
              >
                {titleLabel}
              </h2>
              <p
                className="mt-1 max-w-xl text-sm leading-6 text-muted-foreground"
                id={descriptionId}
              >
                {description}
              </p>
            </div>
            <Button
              aria-label={closeLabel}
              disabled={busy}
              onClick={onClose}
              size="icon"
              variant="ghost"
            >
              <XIcon aria-hidden="true" size={16} />
            </Button>
          </header>

          <div className="app-scrollbar min-h-0 flex-1 overflow-y-auto px-4 py-4 sm:px-5">
            {error ? (
              <div
                className="mb-4 flex items-start gap-2 rounded-field border border-accent-orange/30 bg-accent-orange/10 px-3 py-2.5 text-sm leading-5 text-accent-orange focus-visible:ring-2 focus-visible:ring-ring"
                data-testid={`schedule-${mode}-error`}
                ref={errorRef}
                role="alert"
                tabIndex={-1}
              >
                <AlertCircleIcon
                  aria-hidden="true"
                  className="mt-0.5 size-4 shrink-0"
                />
                <span>{error}</span>
              </div>
            ) : null}

            <div className="space-y-4">
              <ScheduleContentFields
                createForm={createForm}
                errors={fieldErrors}
                patchForm={patchForm}
                titleInputRef={titleInputRef}
              />
              <ScheduleContextFields
                createForm={createForm}
                errors={fieldErrors}
                patchForm={patchForm}
              />
              <ScheduleRuleFields
                createForm={createForm}
                errors={fieldErrors}
                patchForm={patchForm}
              />
            </div>
          </div>

          <footer className="flex flex-col-reverse gap-3 border-t border-border bg-surface-softer px-4 py-3 sm:flex-row sm:items-center sm:justify-between sm:px-5">
            <p className="text-[11px] leading-5 text-muted-foreground">
              {footerHint}
            </p>
            <div className="flex items-center justify-end gap-2">
              <Button
                disabled={busy}
                onClick={onClose}
                size="sm"
                variant="ghost"
              >
                {t("panels.schedule.cancel")}
              </Button>
              <Button disabled={busy} size="sm" type="submit">
                {busy ? (
                  <LoaderCircleIcon
                    aria-hidden="true"
                    className="size-4 animate-spin"
                  />
                ) : null}
                {busy ? busyLabel : submitLabel}
              </Button>
            </div>
          </footer>
        </form>
      </DialogPanel>
    </DialogOverlay>,
    document.body,
  );
}