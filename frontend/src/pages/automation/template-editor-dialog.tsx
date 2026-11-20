import {
  AlertCircleIcon,
  FileTextIcon,
  LoaderCircleIcon,
  SparklesIcon,
  XIcon,
} from "lucide-react";
import {
  useCallback,
  useEffect,
  useId,
  useRef,
  type FormEvent,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import { useFocusTrap } from "@/components/ui/use-focus-trap";

import { TemplateContextFields } from "./template-editor-context-fields";
import {
  templateErrorText,
  templateFieldA11y,
  templateFieldClassName,
  templateFieldErrorId,
  templateTextareaClassName,
} from "./template-editor-controls";
import { TemplateField, TemplateSection } from "./template-editor-fields";
import { TemplateRuleFields } from "./template-editor-rule-fields";
import type {
  AutomationTemplateForm,
  AutomationTemplateFormErrors,
} from "./templates";

export type TemplateEditorDialogProps = {
  form: AutomationTemplateForm;
  mode: "create" | "edit";
  busy: boolean;
  error: string | null;
  fieldErrors: AutomationTemplateFormErrors;
  onChange: (patch: Partial<AutomationTemplateForm>) => void;
  onClearError: () => void;
  onClose: () => void;
  onSubmit: () => void;
};

export function TemplateEditorDialog({
  form,
  mode,
  busy,
  error,
  fieldErrors,
  onChange,
  onClearError,
  onClose,
  onSubmit,
}: TemplateEditorDialogProps) {
  const { t } = useTranslation("workspace");
  const titleId = useId();
  const titleRef = useRef<HTMLInputElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const summaryRef = useRef<HTMLDivElement>(null);
  const openerRef = useRef<HTMLElement | null>(null);
  const lastSummaryRef = useRef("");

  useDialogLifecycle(true, onClose);
  useFocusTrap(panelRef, true);

  useEffect(() => {
    openerRef.current =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    titleRef.current?.focus();
    return () => {
      openerRef.current?.focus();
    };
  }, []);

  const fieldErrorSignature = Object.entries(fieldErrors)
    .filter(([, value]) => Boolean(value))
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([field, value]) => `${field}:${value}`)
    .join("|");
  const hasValidationSummary = Boolean(error || fieldErrorSignature);

  useEffect(() => {
    const signature = error ? `error:${error}` : fieldErrorSignature;
    if (signature && signature !== lastSummaryRef.current) {
      summaryRef.current?.focus();
    }
    lastSummaryRef.current = signature;
  }, [error, fieldErrorSignature]);

  const patchForm = useCallback(
    (patch: Partial<AutomationTemplateForm>) => {
      onClearError();
      onChange(patch);
    },
    [onChange, onClearError],
  );

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!busy) onSubmit();
  };

  if (typeof document === "undefined") return null;

  return createPortal(
    <DialogOverlay className="z-[125] backdrop-blur-sm" onDismiss={onClose}>
      <DialogPanel
        aria-labelledby={titleId}
        aria-modal="true"
        className="max-h-[calc(100dvh-2rem)] max-w-4xl"
        data-mode={mode}
        data-testid="automation-template-dialog"
        elevation="lg"
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
              <SparklesIcon aria-hidden="true" size={18} />
            </span>
            <div className="min-w-0 flex-1">
              <h2
                className="text-[15px] font-semibold tracking-[-0.02em]"
                id={titleId}
              >
                {mode === "create"
                  ? t("panels.schedule.templates.createTitle")
                  : t("panels.schedule.templates.editTitle")}
              </h2>
              <p className="mt-1 text-xs leading-5 text-muted-foreground">
                {t("panels.schedule.templates.editorDescription")}
              </p>
            </div>
            <Button
              aria-label={t("panels.schedule.templates.closeEditor")}
              disabled={busy}
              onClick={onClose}
              size="icon"
              variant="ghost"
            >
              <XIcon aria-hidden="true" size={16} />
            </Button>
          </header>

          <div className="app-scrollbar min-h-0 flex-1 space-y-4 overflow-y-auto px-4 py-4 sm:px-5">
            {hasValidationSummary ? (
              <div
                className="flex items-start gap-2 rounded-field border border-accent-orange/30 bg-accent-orange/10 px-3 py-2.5 text-sm leading-5 text-accent-orange focus-visible:ring-2 focus-visible:ring-ring"
                data-testid="automation-template-error-summary"
                ref={summaryRef}
                role="alert"
                tabIndex={-1}
              >
                <AlertCircleIcon
                  aria-hidden="true"
                  className="mt-0.5 size-4 shrink-0"
                />
                <span>
                  {error ??
                    t("panels.schedule.templates.validation.summary")}
                </span>
              </div>
            ) : null}

            <TemplateSection
              icon={SparklesIcon}
              title={t("panels.schedule.templates.sections.identity")}
            >
              <div className="grid gap-3 sm:grid-cols-2">
                <TemplateField
                  error={templateErrorText(t, fieldErrors.title)}
                  errorId={templateFieldErrorId("title")}
                  label={t("panels.schedule.templates.titleLabel")}
                  required
                >
                  <input
                    className={templateFieldClassName}
                    data-testid="automation-template-title"
                    onChange={(event) =>
                      patchForm({ title: event.target.value })
                    }
                    ref={titleRef}
                    value={form.title}
                    {...templateFieldA11y("title", fieldErrors)}
                  />
                </TemplateField>
                <TemplateField
                  error={templateErrorText(t, fieldErrors.taskTitle)}
                  errorId={templateFieldErrorId("taskTitle")}
                  label={t("panels.schedule.templates.taskTitleLabel")}
                  required
                >
                  <input
                    className={templateFieldClassName}
                    data-testid="automation-template-task-title"
                    onChange={(event) =>
                      patchForm({ taskTitle: event.target.value })
                    }
                    value={form.taskTitle}
                    {...templateFieldA11y("taskTitle", fieldErrors)}
                  />
                </TemplateField>
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <TemplateField label={t("panels.schedule.templates.categoryLabel")}>
                  <input
                    className={templateFieldClassName}
                    data-testid="automation-template-category"
                    onChange={(event) =>
                      patchForm({ category: event.target.value })
                    }
                    placeholder={t(
                      "panels.schedule.templates.categoryPlaceholder",
                    )}
                    value={form.category}
                  />
                </TemplateField>
                <TemplateField
                  label={t("panels.schedule.templates.descriptionLabel")}
                >
                  <input
                    className={templateFieldClassName}
                    data-testid="automation-template-description"
                    onChange={(event) =>
                      patchForm({ description: event.target.value })
                    }
                    value={form.description}
                  />
                </TemplateField>
              </div>
            </TemplateSection>

            <TemplateSection
              description={t("panels.schedule.sectionContentHint")}
              helpLabel={t("panels.schedule.helpLabel", {
                field: t("panels.schedule.sectionContent"),
              })}
              icon={FileTextIcon}
              title={t("panels.schedule.templates.sections.content")}
            >
              <TemplateField
                error={templateErrorText(t, fieldErrors.prompt)}
                errorId={templateFieldErrorId("prompt")}
                label={t("panels.schedule.promptLabel")}
                required
              >
                <textarea
                  className={templateTextareaClassName}
                  data-testid="automation-template-prompt"
                  onChange={(event) =>
                    patchForm({ prompt: event.target.value })
                  }
                  rows={3}
                  value={form.prompt}
                  {...templateFieldA11y("prompt", fieldErrors)}
                />
              </TemplateField>
            </TemplateSection>

            <TemplateContextFields
              errors={fieldErrors}
              form={form}
              onChange={patchForm}
            />

            <TemplateRuleFields
              errors={fieldErrors}
              form={form}
              onChange={patchForm}
            />
          </div>

          <footer className="flex justify-end gap-2 border-t border-border bg-surface-softer px-4 py-3 sm:px-5">
            <Button disabled={busy} onClick={onClose} size="sm" variant="ghost">
              {t("panels.schedule.cancel")}
            </Button>
            <Button disabled={busy} size="sm" type="submit">
              {busy ? (
                <LoaderCircleIcon
                  aria-hidden="true"
                  className="size-4 animate-spin"
                />
              ) : null}
              {busy
                ? t("panels.schedule.templates.saving")
                : mode === "create"
                  ? t("panels.schedule.templates.create")
                  : t("panels.schedule.templates.save")}
            </Button>
          </footer>
        </form>
      </DialogPanel>
    </DialogOverlay>,
    document.body,
  );
}
