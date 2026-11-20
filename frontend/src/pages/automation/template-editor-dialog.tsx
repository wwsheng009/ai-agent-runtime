import { AlertCircleIcon, LoaderCircleIcon, XIcon } from "lucide-react";
import {
  useCallback,
  useEffect,
  useId,
  useRef,
  type FormEvent,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import {
  fieldControlVariants,
  fieldTextareaVariants,
} from "@/components/ui/field-variants";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import type {
  RuntimeScheduleKind,
  RuntimeScheduleTargetKind,
} from "@/types/runtime";

import {
  templateKinds,
  templateTargetKinds,
  type AutomationTemplateForm,
  type AutomationTemplateFormErrors,
} from "./templates";

const inputClassName = fieldControlVariants({ size: "md" });
const textareaClassName = fieldTextareaVariants({ size: "sm" });

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

function Field({
  children,
  error,
  label,
  required = false,
}: {
  children: ReactNode;
  error?: string;
  label: string;
  required?: boolean;
}) {
  return (
    <label className="grid gap-1.5">
      <span className="text-xs font-medium text-foreground">
        {label}
        {required ? <span className="ml-1 text-accent-orange">*</span> : null}
      </span>
      {children}
      {error ? <span className="text-xs text-destructive">{error}</span> : null}
    </label>
  );
}

function Section({ children, title }: { children: ReactNode; title: string }) {
  return (
    <section className="rounded-card border border-border bg-surface-softer p-3.5 sm:p-4">
      <h3 className="mb-3 text-sm font-semibold text-foreground">{title}</h3>
      <div className="grid gap-3">{children}</div>
    </section>
  );
}

function kindLabel(
  t: ReturnType<typeof useTranslation<"workspace">>["t"],
  kind: RuntimeScheduleKind,
) {
  return t(`panels.schedule.kind.${kind}` as never);
}

function targetLabel(
  t: ReturnType<typeof useTranslation<"workspace">>["t"],
  kind: RuntimeScheduleTargetKind,
) {
  return t(`panels.schedule.target.${kind}` as never);
}

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

  useDialogLifecycle(true, onClose);

  useEffect(() => {
    titleRef.current?.focus();
  }, []);

  const patch = useCallback(
    <K extends keyof AutomationTemplateForm>(
      key: K,
      value: AutomationTemplateForm[K],
    ) => {
      onClearError();
      onChange({ [key]: value } as Partial<AutomationTemplateForm>);
    },
    [onChange, onClearError],
  );

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!busy) onSubmit();
  };

  const errorText = (key: string | undefined) => {
    if (!key) return undefined;
    if (key === "required")
      return t("panels.schedule.templates.validation.required");
    if (key === "positive")
      return t("panels.schedule.templates.validation.positive");
    if (key === "clock") return t("panels.schedule.templates.validation.clock");
    return t("panels.schedule.templates.validation.invalid");
  };

  if (typeof document === "undefined") return null;

  return createPortal(
    <DialogOverlay className="z-[125] backdrop-blur-sm" onDismiss={onClose}>
      <DialogPanel
        aria-labelledby={titleId}
        aria-modal="true"
        className="max-h-[calc(100dvh-2rem)] max-w-4xl"
        data-testid="automation-template-dialog"
        role="dialog"
      >
        <form
          className="flex min-h-0 flex-1 flex-col"
          noValidate
          onSubmit={handleSubmit}
        >
          <header className="flex items-start gap-3 border-b border-border bg-surface-softer px-4 py-4 sm:px-5">
            <div className="min-w-0 flex-1">
              <h2 className="text-[15px] font-semibold" id={titleId}>
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
            {error ? (
              <div
                className="flex items-start gap-2 rounded-field border border-accent-orange/30 bg-accent-orange/10 px-3 py-2.5 text-sm text-accent-orange"
                role="alert"
              >
                <AlertCircleIcon
                  aria-hidden="true"
                  className="mt-0.5 size-4 shrink-0"
                />
                <span>{error}</span>
              </div>
            ) : null}

            <Section title={t("panels.schedule.templates.sections.identity")}>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field
                  error={errorText(fieldErrors.title)}
                  label={t("panels.schedule.templates.titleLabel")}
                  required
                >
                  <input
                    className={inputClassName}
                    data-testid="automation-template-title"
                    onChange={(event) => patch("title", event.target.value)}
                    ref={titleRef}
                    value={form.title}
                  />
                </Field>
                <Field
                  error={errorText(fieldErrors.taskTitle)}
                  label={t("panels.schedule.templates.taskTitleLabel")}
                  required
                >
                  <input
                    className={inputClassName}
                    onChange={(event) => patch("taskTitle", event.target.value)}
                    value={form.taskTitle}
                  />
                </Field>
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label={t("panels.schedule.templates.categoryLabel")}>
                  <input
                    className={inputClassName}
                    onChange={(event) => patch("category", event.target.value)}
                    placeholder={t(
                      "panels.schedule.templates.categoryPlaceholder",
                    )}
                    value={form.category}
                  />
                </Field>
                <Field label={t("panels.schedule.templates.descriptionLabel")}>
                  <input
                    className={inputClassName}
                    onChange={(event) =>
                      patch("description", event.target.value)
                    }
                    value={form.description}
                  />
                </Field>
              </div>
            </Section>

            <Section title={t("panels.schedule.templates.sections.content")}>
              <Field
                error={errorText(fieldErrors.prompt)}
                label={t("panels.schedule.promptLabel")}
                required
              >
                <textarea
                  className={textareaClassName}
                  onChange={(event) => patch("prompt", event.target.value)}
                  rows={3}
                  value={form.prompt}
                />
              </Field>
              <Field
                error={errorText(fieldErrors.goal)}
                label={t("panels.schedule.goalLabel")}
              >
                <textarea
                  className={textareaClassName}
                  onChange={(event) => patch("goal", event.target.value)}
                  rows={2}
                  value={form.goal}
                />
              </Field>
              <Field label={t("panels.schedule.planLabel")}>
                <textarea
                  className={textareaClassName}
                  onChange={(event) => patch("plan", event.target.value)}
                  placeholder={t("panels.schedule.planPlaceholder")}
                  rows={3}
                  value={form.plan}
                />
              </Field>
            </Section>

            <Section title={t("panels.schedule.templates.sections.context")}>
              <Field label={t("panels.schedule.targetLabel")}>
                <select
                  className={inputClassName}
                  onChange={(event) =>
                    patch(
                      "targetKind",
                      event.target.value as RuntimeScheduleTargetKind,
                    )
                  }
                  value={form.targetKind}
                >
                  {templateTargetKinds.map((kind) => (
                    <option key={kind} value={kind}>
                      {targetLabel(t, kind)}
                    </option>
                  ))}
                </select>
              </Field>
              {form.targetKind === "workspace" ? (
                <Field
                  error={errorText(fieldErrors.targetWorkspace)}
                  label={t("panels.schedule.workspaceLabel")}
                  required
                >
                  <input
                    className={inputClassName}
                    onChange={(event) =>
                      patch("targetWorkspace", event.target.value)
                    }
                    value={form.targetWorkspace}
                  />
                </Field>
              ) : null}
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label={t("panels.schedule.profileLabel")}>
                  <input
                    className={inputClassName}
                    onChange={(event) =>
                      patch("targetProfile", event.target.value)
                    }
                    placeholder={t("panels.schedule.profilePlaceholder")}
                    value={form.targetProfile}
                  />
                </Field>
                <Field label={t("panels.schedule.providerLabel")}>
                  <input
                    className={inputClassName}
                    onChange={(event) => patch("provider", event.target.value)}
                    placeholder={t("panels.schedule.providerDefault")}
                    value={form.provider}
                  />
                </Field>
                <Field label={t("panels.schedule.modelLabel")}>
                  <input
                    className={inputClassName}
                    onChange={(event) => patch("model", event.target.value)}
                    placeholder={t("panels.schedule.modelDefault")}
                    value={form.model}
                  />
                </Field>
                <Field label={t("panels.schedule.reasoningEffortLabel")}>
                  <input
                    className={inputClassName}
                    onChange={(event) =>
                      patch("reasoningEffort", event.target.value)
                    }
                    placeholder={t("panels.schedule.reasoningEffortDefault")}
                    value={form.reasoningEffort}
                  />
                </Field>
              </div>
            </Section>

            <Section title={t("panels.schedule.templates.sections.rule")}>
              <Field label={t("panels.schedule.kindLabel")}>
                <select
                  className={inputClassName}
                  onChange={(event) =>
                    patch("kind", event.target.value as RuntimeScheduleKind)
                  }
                  value={form.kind}
                >
                  {templateKinds.map((kind) => (
                    <option key={kind} value={kind}>
                      {kindLabel(t, kind)}
                    </option>
                  ))}
                </select>
              </Field>
              {form.kind === "after" || form.kind === "every" ? (
                <Field
                  error={errorText(fieldErrors.amount)}
                  label={
                    form.kind === "after"
                      ? t("panels.schedule.minutesAfter")
                      : t("panels.schedule.minutesEvery")
                  }
                  required
                >
                  <input
                    className={inputClassName}
                    min={form.kind === "every" ? 1 : 0.01}
                    onChange={(event) => patch("amount", event.target.value)}
                    type="number"
                    value={form.amount}
                  />
                </Field>
              ) : null}
              {form.kind === "at" ? (
                <Field
                  error={errorText(fieldErrors.atTime)}
                  label={t("panels.schedule.atLabel")}
                  required
                >
                  <input
                    className={inputClassName}
                    onChange={(event) => patch("atTime", event.target.value)}
                    type="datetime-local"
                    value={form.atTime}
                  />
                </Field>
              ) : null}
              {form.kind === "daily" || form.kind === "weekly" ? (
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field
                    error={errorText(fieldErrors.clockTime)}
                    label={t("panels.schedule.timeLabel")}
                    required
                  >
                    <input
                      className={inputClassName}
                      onChange={(event) =>
                        patch("clockTime", event.target.value)
                      }
                      placeholder={t("panels.schedule.timePlaceholder")}
                      value={form.clockTime}
                    />
                  </Field>
                  <Field
                    error={errorText(fieldErrors.timeZone)}
                    label={t("panels.schedule.zoneLabel")}
                    required
                  >
                    <input
                      className={inputClassName}
                      onChange={(event) =>
                        patch("timeZone", event.target.value)
                      }
                      placeholder={t("panels.schedule.zonePlaceholder")}
                      value={form.timeZone}
                    />
                  </Field>
                </div>
              ) : null}
              {form.kind === "weekly" ? (
                <Field
                  error={errorText(fieldErrors.weekdays)}
                  label={t("panels.schedule.weekdayLabel")}
                  required
                >
                  <div className="flex flex-wrap gap-2">
                    {Array.from({ length: 7 }, (_, index) => index + 1).map(
                      (day) => {
                        const checked = form.weekdays.includes(day);
                        return (
                          <label
                            className="flex items-center gap-1.5 rounded-field border border-border px-2 py-1.5 text-xs"
                            key={day}
                          >
                            <input
                              checked={checked}
                              onChange={(event) =>
                                patch(
                                  "weekdays",
                                  event.target.checked
                                    ? [...form.weekdays, day]
                                    : form.weekdays.filter(
                                        (item) => item !== day,
                                      ),
                                )
                              }
                              type="checkbox"
                            />
                            {t(`panels.schedule.weekday.${day}` as never)}
                          </label>
                        );
                      },
                    )}
                  </div>
                </Field>
              ) : null}
              {form.kind === "cron" ? (
                <>
                  <Field
                    error={errorText(fieldErrors.cron)}
                    label={t("panels.schedule.cronLabel")}
                    required
                  >
                    <input
                      className={inputClassName}
                      onChange={(event) => patch("cron", event.target.value)}
                      value={form.cron}
                    />
                  </Field>
                  <Field
                    error={errorText(fieldErrors.timeZone)}
                    label={t("panels.schedule.zoneLabel")}
                    required
                  >
                    <input
                      className={inputClassName}
                      onChange={(event) =>
                        patch("timeZone", event.target.value)
                      }
                      placeholder={t("panels.schedule.zonePlaceholder")}
                      value={form.timeZone}
                    />
                  </Field>
                </>
              ) : null}
            </Section>
          </div>

          <footer className="flex justify-end gap-2 border-t border-border bg-surface-softer px-4 py-3 sm:px-5">
            <Button disabled={busy} onClick={onClose} variant="ghost">
              {t("panels.schedule.cancel")}
            </Button>
            <Button disabled={busy} type="submit">
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
