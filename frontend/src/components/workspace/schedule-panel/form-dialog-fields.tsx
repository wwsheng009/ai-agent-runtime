// 自动化任务新建对话框的字段分组。
//
// 按「任务内容 / 执行上下文 / 运行规则」拆成三个局部组件，避免弹层外壳和
// 条件字段堆在同一个文件里；所有写操作都通过同一个 patchForm 更新。

import {
  CalendarClockIcon,
  FileTextIcon,
  InfoIcon,
  TargetIcon,
  type LucideIcon,
} from "lucide-react";
import { useEffect, useId, useState, type ReactNode, type RefObject } from "react";
import { useTranslation } from "react-i18next";

import { listRuntimeSessions } from "@/api/runtime/sessions";
import {
  fieldControlVariants,
  fieldTextareaVariants,
} from "@/components/ui/field-variants";
import { Select } from "@/components/ui/select";
import { scheduleKindLabelKey } from "@/components/workspace/schedule-panel-shared";
import { cn } from "@/lib/utils";
import type {
  RuntimeSessionRecord,
  RuntimeScheduleKind,
  RuntimeScheduleTargetKind,
} from "@/types/runtime";

import { CronAssist } from "./cron-assist";
import { createKinds } from "./form-state";
import type {
  CreateFormState,
  ScheduleFormErrors,
  ScheduleFormField,
} from "./form-state";
import { HelpTipBody, HelpTipTrigger } from "./help-tip";
import { WeekdayPicker } from "./weekday-picker";

const fieldClassName = fieldControlVariants({ size: "md" });
const textareaClassName = fieldTextareaVariants({ size: "md" });

const TARGET_OPTIONS = [
  {
    kind: "session",
    labelKey: "panels.schedule.target.session",
    descriptionKey: "panels.schedule.targetDescription.session",
  },
  {
    kind: "headless",
    labelKey: "panels.schedule.target.headless",
    descriptionKey: "panels.schedule.targetDescription.headless",
  },
  {
    kind: "workspace",
    labelKey: "panels.schedule.target.workspace",
    descriptionKey: "panels.schedule.targetDescription.workspace",
  },
] as const satisfies ReadonlyArray<{
  descriptionKey: string;
  kind: RuntimeScheduleTargetKind;
  labelKey: string;
}>;

type PatchForm = (patch: Partial<CreateFormState>) => void;

type SectionFieldsProps = {
  createForm: CreateFormState;
  errors: ScheduleFormErrors;
  patchForm: PatchForm;
};

type ContentFieldsProps = SectionFieldsProps & {
  titleInputRef: RefObject<HTMLInputElement | null>;
};

type FormSectionProps = {
  children: ReactNode;
  description: string;
  icon: LucideIcon;
  title: string;
};

function FormSection({
  children,
  description,
  icon: Icon,
  title,
}: FormSectionProps) {
  const titleId = useId();
  return (
    <section
      aria-labelledby={titleId}
      className="rounded-card border border-border bg-surface-softer p-3.5 sm:p-4"
    >
      <div className="mb-3 flex items-start gap-2.5">
        <span className="mt-0.5 grid size-7 shrink-0 place-items-center rounded-field border border-border bg-surface-solid text-accent-secondary">
          <Icon aria-hidden="true" size={14} />
        </span>
        <div className="min-w-0">
          <h3
            className="text-sm font-semibold tracking-[-0.01em] text-foreground"
            id={titleId}
          >
            {title}
          </h3>
          <p className="mt-0.5 text-xs leading-5 text-muted-foreground">
            {description}
          </p>
        </div>
      </div>
      {children}
    </section>
  );
}

function FieldLabel({
  children,
  required = false,
}: {
  children: ReactNode;
  required?: boolean;
}) {
  return (
    <span className="flex items-center gap-1 text-xs font-medium text-foreground">
      {children}
      {required ? (
        <span aria-hidden="true" className="text-accent-orange">
          *
        </span>
      ) : null}
    </span>
  );
}

function Field({
  action,
  children,
  className,
  error,
  errorId,
  extra,
  help,
  helpLabel,
  label,
  required = false,
}: {
  /** 字段标题行右侧的动作入口，例如 Cron 可视化助手。 */
  action?: ReactNode;
  children: ReactNode;
  className?: string;
  error?: string;
  errorId?: string;
  /** 字段说明，展示在问号提示气泡中。 */
  help?: string;
  /** 提示按钮的无障碍名称。 */
  helpLabel?: string;
  /** 渲染在输入控件之外（仍在 label 外）的补充内容，例如 Cron 可视化区块。 */
  extra?: ReactNode;
  label: ReactNode;
  required?: boolean;
}) {
  const tipId = useId();
  const [helpOpen, setHelpOpen] = useState(false);
  const hasHelp = Boolean(help && helpLabel);

  return (
    <div className={cn("grid gap-1.5", className)}>
      <div className="flex items-start justify-between gap-2">
        <label className="grid min-w-0 flex-1 gap-1.5">
          <FieldLabel required={required}>{label}</FieldLabel>
          {children}
        </label>
        {action || hasHelp ? (
          <div className="mt-0.5 flex shrink-0 items-center gap-1">
            {action}
            {hasHelp ? (
              <HelpTipTrigger
                label={helpLabel as string}
                onOpenChange={setHelpOpen}
                open={helpOpen}
                tipId={tipId}
              />
            ) : null}
          </div>
        ) : null}
      </div>
      {hasHelp && helpOpen ? <HelpTipBody text={help as string} tipId={tipId} /> : null}
      {extra}
      {error ? (
        <span
          className="text-xs text-destructive"
          id={errorId}
          data-testid="schedule-field-error"
        >
          {error}
        </span>
      ) : null}
    </div>
  );
}

/** 常用时区：覆盖国内与主流出海/协作场景，其余时区仍可在下方手填。 */
const COMMON_TIME_ZONES = [
  "Asia/Shanghai",
  "Asia/Hong_Kong",
  "Asia/Taipei",
  "Asia/Tokyo",
  "Asia/Singapore",
  "Europe/London",
  "Europe/Berlin",
  "America/New_York",
  "America/Los_Angeles",
  "UTC",
] as const;

const CUSTOM_ZONE_VALUE = "__custom__";


function fieldA11y(field: ScheduleFormField, errors: ScheduleFormErrors) {
  return {
    "aria-describedby": errors[field] ? `schedule-field-error-${field}` : undefined,
    "aria-invalid": errors[field] ? true : undefined,
  };
}

export function ScheduleContentFields({
  createForm,
  errors,
  patchForm,
  titleInputRef,
}: ContentFieldsProps) {
  const { t } = useTranslation("workspace");
  return (
    <FormSection
      description={t("panels.schedule.sectionContentHint")}
      icon={FileTextIcon}
      title={t("panels.schedule.sectionContent")}
    >
      <div className="grid gap-3">
        <Field
          error={errors.title ? t("panels.schedule.validation.required") : undefined}
          errorId="schedule-field-error-title"
          label={t("panels.schedule.titleLabel")}
          required
        >
          <input
            className={fieldClassName}
            data-testid="schedule-create-title"
            onChange={(event) => patchForm({ title: event.target.value })}
            ref={titleInputRef}
            required
            value={createForm.title}
            {...fieldA11y("title", errors)}
          />
        </Field>
        <Field
          error={errors.prompt ? t("panels.schedule.validation.required") : undefined}
          errorId="schedule-field-error-prompt"
          label={t("panels.schedule.promptLabel")}
          required
        >
          <textarea
            className={textareaClassName}
            data-testid="schedule-create-prompt"
            onChange={(event) => patchForm({ prompt: event.target.value })}
            required
            rows={4}
            value={createForm.prompt}
            {...fieldA11y("prompt", errors)}
          />
        </Field>
      </div>
    </FormSection>
  );
}

export function ScheduleContextFields({
  createForm,
  errors,
  patchForm,
}: SectionFieldsProps) {
  const { t } = useTranslation("workspace");
  const [sessions, setSessions] = useState<RuntimeSessionRecord[]>([]);
  const [sessionsLoading, setSessionsLoading] = useState(false);
  const [sessionsLoadFailed, setSessionsLoadFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setSessionsLoading(true);
    setSessionsLoadFailed(false);
    void listRuntimeSessions()
      .then((response) => {
        if (!cancelled) {
          setSessions(response.sessions);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setSessionsLoadFailed(true);
        }
      })
      .finally(() => {
        if (!cancelled) {
          setSessionsLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const hasSelectedSession =
    createForm.sessionId.length > 0 &&
    !sessions.some((session) => session.id === createForm.sessionId);
  const sessionOptions = [
    {
      value: "",
      label: sessionsLoading
        ? t("panels.schedule.sessionLoading")
        : t("panels.schedule.sessionSelectPlaceholder"),
    },
    ...(hasSelectedSession
      ? [{ value: createForm.sessionId, label: createForm.sessionId }]
      : []),
    ...sessions.map((session) => ({
      value: session.id,
      label: session.metadata?.title?.trim()
        ? `${session.metadata.title.trim()} · ${session.id}`
        : session.id,
    })),
  ];

  return (
    <FormSection
      description={t("panels.schedule.sectionContextHint")}
      icon={TargetIcon}
      title={t("panels.schedule.sectionContext")}
    >
      <div className="grid gap-3">
        <div>
          <span className="text-xs font-medium text-foreground">
            {t("panels.schedule.targetLabel")}
          </span>
          <div
            aria-label={t("panels.schedule.targetLabel")}
            className="mt-1.5 grid gap-2 sm:grid-cols-3"
            role="radiogroup"
          >
            {TARGET_OPTIONS.map((option) => {
              const checked = createForm.targetKind === option.kind;
              return (
                <label
                  className={cn(
                    "flex cursor-pointer items-start gap-2.5 rounded-field border px-3 py-2.5 transition focus-within:ring-2 focus-within:ring-ring",
                    checked
                      ? "border-accent-primary-border bg-accent-primary-soft"
                      : "border-border bg-surface-solid hover:border-border-strong hover:bg-surface-soft",
                  )}
                  key={option.kind}
                >
                  <input
                    checked={checked}
                    className="sr-only"
                    data-testid={`schedule-target-${option.kind}`}
                    name="schedule-target-kind"
                    onChange={() => patchForm({ targetKind: option.kind })}
                    type="radio"
                    value={option.kind}
                  />
                  <span
                    aria-hidden="true"
                    className={cn(
                      "mt-0.5 grid size-4 shrink-0 place-items-center rounded-full border transition",
                      checked
                        ? "border-accent-primary"
                        : "border-border-strong",
                    )}
                  >
                    {checked ? (
                      <span className="size-2 rounded-full bg-accent-primary" />
                    ) : null}
                  </span>
                  <span className="min-w-0">
                    <span className="block text-xs font-semibold text-foreground">
                      {t(option.labelKey)}
                    </span>
                    <span className="mt-0.5 block text-[11px] leading-4 text-muted-foreground">
                      {t(option.descriptionKey)}
                    </span>
                  </span>
                </label>
              );
            })}
          </div>
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          {createForm.targetKind === "session" ? (
            <Field
              className="sm:col-span-2"
              error={errors.sessionId ? t("panels.schedule.validation.required") : undefined}
              errorId="schedule-field-error-sessionId"
              label={t("panels.schedule.sessionLabel")}
              required
            >
              <div className="grid gap-2">
                <Select
                  ariaLabel={t("panels.schedule.sessionSelectPlaceholder")}
                  className="w-full"
                  disabled={sessionsLoading}
                  onChange={(sessionId) => patchForm({ sessionId })}
                  options={sessionOptions}
                  size="md"
                  triggerClassName={fieldClassName}
                  triggerTestId="schedule-session-select"
                  value={createForm.sessionId}
                />
                <input
                  className={fieldClassName}
                  data-testid="schedule-session-id"
                  onChange={(event) =>
                    patchForm({ sessionId: event.target.value })
                  }
                  placeholder={t("panels.schedule.sessionPlaceholder")}
                  required
                  value={createForm.sessionId}
                  {...fieldA11y("sessionId", errors)}
                />
                {sessionsLoadFailed ? (
                  <span className="text-xs text-muted-foreground" role="status">
                    {t("panels.schedule.sessionLoadFailed")}
                  </span>
                ) : sessions.length === 0 && !sessionsLoading ? (
                  <span className="text-xs text-muted-foreground">
                    {t("panels.schedule.sessionListEmpty")}
                  </span>
                ) : (
                  <span className="text-xs text-muted-foreground">
                    {t("panels.schedule.sessionManualHint")}
                  </span>
                )}
              </div>
            </Field>
          ) : null}

          {createForm.targetKind === "workspace" ? (
            <Field
              className="sm:col-span-2"
              error={errors.targetWorkspace ? t("panels.schedule.validation.required") : undefined}
              errorId="schedule-field-error-targetWorkspace"
              label={t("panels.schedule.workspaceLabel")}
              required
            >
              <input
                className={fieldClassName}
                data-testid="schedule-target-workspace"
                onChange={(event) =>
                  patchForm({ targetWorkspace: event.target.value })
                }
                placeholder={t("panels.schedule.workspacePlaceholder")}
                required
                spellCheck={false}
                value={createForm.targetWorkspace}
                {...fieldA11y("targetWorkspace", errors)}
              />
            </Field>
          ) : null}

          {createForm.targetKind !== "session" ? (
            <Field label={t("panels.schedule.profileLabel")}>
              <input
                className={fieldClassName}
                data-testid="schedule-target-profile"
                onChange={(event) =>
                  patchForm({ targetProfile: event.target.value })
                }
                placeholder={t("panels.schedule.profilePlaceholder")}
                spellCheck={false}
                value={createForm.targetProfile}
              />
            </Field>
          ) : null}

          <Field
            className="sm:col-span-2"
            error={errors.goal ? t("panels.schedule.validation.required") : undefined}
            errorId="schedule-field-error-goal"
            help={t("panels.schedule.help.goal")}
            helpLabel={t("panels.schedule.helpLabel", {
              field: t("panels.schedule.goalLabel"),
            })}
            label={t("panels.schedule.goalLabel")}
            required={createForm.targetKind !== "session"}
          >
            <input
              className={fieldClassName}
              data-testid="schedule-goal"
              onChange={(event) => patchForm({ goal: event.target.value })}
              required={createForm.targetKind !== "session"}
              value={createForm.goal}
              {...fieldA11y("goal", errors)}
            />
          </Field>

          <Field
            className="sm:col-span-2"
            help={t("panels.schedule.help.plan")}
            helpLabel={t("panels.schedule.helpLabel", {
              field: t("panels.schedule.planLabel"),
            })}
            label={t("panels.schedule.planLabel")}
          >
            <textarea
              className={textareaClassName}
              data-testid="schedule-plan"
              onChange={(event) => patchForm({ plan: event.target.value })}
              placeholder={t("panels.schedule.planPlaceholder")}
              rows={3}
              value={createForm.plan}
            />
          </Field>
        </div>

        {createForm.targetKind !== "session" ? (
          <p className="flex items-start gap-2 rounded-field border border-dashed border-border bg-surface-solid px-3 py-2 text-xs leading-5 text-muted-foreground">
            <InfoIcon
              aria-hidden="true"
              className="mt-0.5 size-3.5 shrink-0 text-accent-secondary"
            />
            <span>{t("panels.schedule.targetHint")}</span>
          </p>
        ) : null}
      </div>
    </FormSection>
  );
}

export function ScheduleRuleFields({
  createForm,
  errors,
  patchForm,
}: SectionFieldsProps) {
  const { t } = useTranslation("workspace");
  // 是否处于「自定义时区」手输模式。默认收起输入框：常用时区走下拉，
  // 两个同值控件并排会让用户误以为要填两遍。
  const [zoneCustom, setZoneCustom] = useState(false);
  const zoneIsCustom =
    zoneCustom ||
    !(COMMON_TIME_ZONES as readonly string[]).includes(createForm.timeZone);

  return (
    <FormSection
      description={t("panels.schedule.sectionRuleHint")}
      icon={CalendarClockIcon}
      title={t("panels.schedule.sectionRule")}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field
          className="sm:col-span-2"
          label={t("panels.schedule.kindLabel")}
          required
        >
          <Select
            ariaLabel={t("panels.schedule.kindLabel")}
            className="w-full"
            onChange={(kind) =>
              patchForm({
                kind: kind as RuntimeScheduleKind,
                atTime: "",
                clockTime: "",
              })
            }
            options={createKinds.map((kind) => ({
              value: kind,
              label: t(scheduleKindLabelKey(kind)),
            }))}
            triggerClassName={fieldClassName}
            triggerTestId="schedule-kind"
            value={createForm.kind}
          />
        </Field>

        {createForm.kind === "after" || createForm.kind === "every" ? (
          <Field
            error={errors.amount ? t("panels.schedule.validation.positive") : undefined}
            errorId="schedule-field-error-amount"
            label={t(
              createForm.kind === "after"
                ? "panels.schedule.minutesAfter"
                : "panels.schedule.minutesEvery",
            )}
            required
          >
            <input
              className={fieldClassName}
              data-testid="schedule-amount"
              inputMode="numeric"
              min={createForm.kind === "after" ? 0.1 : 1}
              onChange={(event) => patchForm({ amount: event.target.value })}
              required
              step="any"
              type="number"
              value={createForm.amount}
              {...fieldA11y("amount", errors)}
            />
          </Field>
        ) : null}

        {createForm.kind === "at" ? (
          <Field
            className="sm:col-span-2"
            error={errors.atTime ? t("panels.schedule.validation.required") : undefined}
            errorId="schedule-field-error-atTime"
            label={t("panels.schedule.atLabel")}
            required
          >
            <input
              className={fieldClassName}
              data-testid="schedule-at-time"
              onChange={(event) => patchForm({ atTime: event.target.value })}
              required
              step={1}
              type="datetime-local"
              value={createForm.atTime}
              {...fieldA11y("atTime", errors)}
            />
          </Field>
        ) : null}

        {createForm.kind === "daily" || createForm.kind === "weekly" ? (
          <Field
            error={errors.clockTime ? t("panels.schedule.validation.clock") : undefined}
            errorId="schedule-field-error-clockTime"
            label={t("panels.schedule.timeLabel")}
            required
          >
            <input
              className={fieldClassName}
              data-testid="schedule-clock-time"
              inputMode="numeric"
              onChange={(event) =>
                patchForm({ clockTime: event.target.value })
              }
              placeholder={t("panels.schedule.timePlaceholder")}
              required
              value={createForm.clockTime}
              {...fieldA11y("clockTime", errors)}
            />
          </Field>
        ) : null}

        {createForm.kind === "daily" ||
        createForm.kind === "weekly" ||
        createForm.kind === "cron" ? (
          <Field
            className="sm:col-span-2"
            error={errors.timeZone ? t("panels.schedule.validation.required") : undefined}
            errorId="schedule-field-error-timeZone"
            help={t("panels.schedule.help.timeZone")}
            helpLabel={t("panels.schedule.helpLabel", { field: t("panels.schedule.zoneLabel") })}
            label={t("panels.schedule.zoneLabel")}
            required
          >
            <Select
              ariaLabel={t("panels.schedule.zonePresetLabel")}
              className="w-full"
              onChange={(zone) => {
                if (zone === CUSTOM_ZONE_VALUE) {
                  setZoneCustom(true);
                  return;
                }
                setZoneCustom(false);
                patchForm({ timeZone: zone });
              }}
              options={[
                ...COMMON_TIME_ZONES.map((zone) => ({ value: zone, label: zone })),
                {
                  value: CUSTOM_ZONE_VALUE,
                  label: t("panels.schedule.zoneCustomOption"),
                },
              ]}
              triggerClassName={fieldClassName}
              triggerTestId="schedule-time-zone-preset"
              value={
                zoneIsCustom ? CUSTOM_ZONE_VALUE : createForm.timeZone
              }
            />
            {zoneIsCustom ? (
              <input
                className={cn(fieldClassName, "mt-1.5")}
                data-testid="schedule-time-zone"
                onChange={(event) => patchForm({ timeZone: event.target.value })}
                placeholder={t("panels.schedule.zonePlaceholder")}
                required
                spellCheck={false}
                value={createForm.timeZone}
                {...fieldA11y("timeZone", errors)}
              />
            ) : null}
          </Field>
        ) : null}

        {createForm.kind === "weekly" ? (
          <div className="sm:col-span-2">
            <WeekdayPicker
              describedBy={
                errors.weekdays ? "schedule-field-error-weekdays" : undefined
              }
              invalid={Boolean(errors.weekdays)}
              onToggle={(day) =>
                patchForm({
                  weekdays: createForm.weekdays.includes(day)
                    ? createForm.weekdays.filter((value) => value !== day)
                    : [...createForm.weekdays, day],
                })
              }
              selected={createForm.weekdays}
            />
            {errors.weekdays ? (
              <p
                className="mt-1 text-xs text-destructive"
                id="schedule-field-error-weekdays"
                role="alert"
              >
                {t("panels.schedule.validation.requiredWeekday")}
              </p>
            ) : null}
          </div>
        ) : null}

        {createForm.kind === "cron" ? (
          <Field
            className="sm:col-span-2"
            error={errors.cron ? t("panels.schedule.validation.required") : undefined}
            errorId="schedule-field-error-cron"
            help={t("panels.schedule.help.cron")}
            helpLabel={t("panels.schedule.helpLabel", {
              field: t("panels.schedule.cronLabel"),
            })}
            label={t("panels.schedule.cronLabel")}
            required
            action={
              <CronAssist
                onPick={(expression) => patchForm({ cron: expression })}
                value={createForm.cron}
              />
            }
          >
            <input
              className={fieldClassName}
              data-testid="schedule-cron"
              onChange={(event) =>
                patchForm({ cron: event.target.value })
              }
              placeholder="*/5 * * * *"
              required
              spellCheck={false}
              value={createForm.cron}
              {...fieldA11y("cron", errors)}
            />
          </Field>
        ) : null}
      </div>
    </FormSection>
  );
}