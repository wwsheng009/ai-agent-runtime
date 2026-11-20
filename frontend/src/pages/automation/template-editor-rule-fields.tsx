import { CalendarClockIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Select } from "@/components/ui/select";
import { CronAssist } from "@/components/workspace/schedule-panel/cron-assist";
import { WeekdayPicker } from "@/components/workspace/schedule-panel/weekday-picker";
import { scheduleKindLabelKey } from "@/components/workspace/schedule-panel-shared";
import { cn } from "@/lib/utils";
import type { RuntimeScheduleKind } from "@/types/runtime";

import {
  templateErrorText,
  templateFieldA11y,
  templateFieldClassName,
  templateFieldErrorId,
} from "./template-editor-controls";
import { TemplateField, TemplateSection } from "./template-editor-fields";
import {
  localTimeZone,
  templateKinds,
  type AutomationTemplateForm,
  type AutomationTemplateFormErrors,
} from "./templates";

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

export function TemplateRuleFields({
  errors,
  form,
  onChange,
}: {
  errors: AutomationTemplateFormErrors;
  form: AutomationTemplateForm;
  onChange: (patch: Partial<AutomationTemplateForm>) => void;
}) {
  const { t } = useTranslation("workspace");
  const [zoneCustom, setZoneCustom] = useState(false);
  const zoneIsCustom =
    zoneCustom ||
    !(COMMON_TIME_ZONES as readonly string[]).includes(form.timeZone);

  return (
    <TemplateSection
      description={t("panels.schedule.sectionRuleHint")}
      helpLabel={t("panels.schedule.helpLabel", {
        field: t("panels.schedule.sectionRule"),
      })}
      icon={CalendarClockIcon}
      title={t("panels.schedule.templates.sections.rule")}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <TemplateField
          className="sm:col-span-2"
          label={t("panels.schedule.kindLabel")}
          required
        >
          <Select
            ariaLabel={t("panels.schedule.kindLabel")}
            className="w-full"
            onChange={(kind) =>
              onChange({
                kind: kind as RuntimeScheduleKind,
                atTime: "",
                clockTime: "",
                ...(form.timeZone.trim()
                  ? {}
                  : { timeZone: localTimeZone() }),
              })
            }
            options={templateKinds.map((kind) => ({
              value: kind,
              label: t(scheduleKindLabelKey(kind)),
            }))}
            triggerClassName={templateFieldClassName}
            triggerTestId="automation-template-kind"
            value={form.kind}
          />
        </TemplateField>

        {form.kind === "after" || form.kind === "every" ? (
          <TemplateField
            className="sm:col-span-2"
            error={templateErrorText(t, errors.amount)}
            errorId={templateFieldErrorId("amount")}
            label={t(
              form.kind === "after"
                ? "panels.schedule.minutesAfter"
                : "panels.schedule.minutesEvery",
            )}
            required
          >
            <input
              className={templateFieldClassName}
              data-testid="automation-template-amount"
              inputMode="numeric"
              min={form.kind === "every" ? 1 : 0.1}
              onChange={(event) => onChange({ amount: event.target.value })}
              required
              step="any"
              type="number"
              value={form.amount}
              {...templateFieldA11y("amount", errors)}
            />
          </TemplateField>
        ) : null}

        {form.kind === "at" ? (
          <TemplateField
            className="sm:col-span-2"
            error={templateErrorText(t, errors.atTime)}
            errorId={templateFieldErrorId("atTime")}
            label={t("panels.schedule.atLabel")}
            required
          >
            <input
              className={templateFieldClassName}
              data-testid="automation-template-at-time"
              onChange={(event) => onChange({ atTime: event.target.value })}
              required
              step={1}
              type="datetime-local"
              value={form.atTime}
              {...templateFieldA11y("atTime", errors)}
            />
          </TemplateField>
        ) : null}

        {form.kind === "daily" || form.kind === "weekly" ? (
          <TemplateField
            error={templateErrorText(t, errors.clockTime)}
            errorId={templateFieldErrorId("clockTime")}
            label={t("panels.schedule.timeLabel")}
            required
          >
            <input
              className={templateFieldClassName}
              data-testid="automation-template-clock-time"
              inputMode="numeric"
              onChange={(event) => onChange({ clockTime: event.target.value })}
              placeholder={t("panels.schedule.timePlaceholder")}
              required
              value={form.clockTime}
              {...templateFieldA11y("clockTime", errors)}
            />
          </TemplateField>
        ) : null}

        {form.kind === "daily" ||
        form.kind === "weekly" ||
        form.kind === "cron" ? (
          <TemplateField
            className={
              form.kind === "daily" ? undefined : "sm:col-span-2"
            }
            error={templateErrorText(t, errors.timeZone)}
            errorId={templateFieldErrorId("timeZone")}
            help={t("panels.schedule.help.timeZone")}
            helpLabel={t("panels.schedule.helpLabel", {
              field: t("panels.schedule.zoneLabel"),
            })}
            label={t("panels.schedule.zoneLabel")}
            required
          >
            <Select
              ariaDescribedBy={
                errors.timeZone
                  ? templateFieldErrorId("timeZone")
                  : undefined
              }
              ariaInvalid={Boolean(errors.timeZone)}
              ariaLabel={t("panels.schedule.zonePresetLabel")}
              className="w-full"
              onChange={(zone) => {
                if (zone === CUSTOM_ZONE_VALUE) {
                  setZoneCustom(true);
                  return;
                }
                setZoneCustom(false);
                onChange({ timeZone: zone });
              }}
              options={[
                ...COMMON_TIME_ZONES.map((zone) => ({
                  value: zone,
                  label: zone,
                })),
                {
                  value: CUSTOM_ZONE_VALUE,
                  label: t("panels.schedule.zoneCustomOption"),
                },
              ]}
              triggerClassName={templateFieldClassName}
              triggerTestId="automation-template-time-zone-preset"
              value={zoneIsCustom ? CUSTOM_ZONE_VALUE : form.timeZone}
            />
            {zoneIsCustom ? (
              <input
                className={cn(templateFieldClassName, "mt-1.5")}
                data-testid="automation-template-time-zone"
                onChange={(event) => onChange({ timeZone: event.target.value })}
                placeholder={t("panels.schedule.zonePlaceholder")}
                required
                spellCheck={false}
                value={form.timeZone}
                {...templateFieldA11y("timeZone", errors)}
              />
            ) : null}
          </TemplateField>
        ) : null}

        {form.kind === "weekly" ? (
          <div className="sm:col-span-2">
            <WeekdayPicker
              describedBy={
                errors.weekdays
                  ? templateFieldErrorId("weekdays")
                  : undefined
              }
              invalid={Boolean(errors.weekdays)}
              onToggle={(day) =>
                onChange({
                  weekdays: form.weekdays.includes(day)
                    ? form.weekdays.filter((value) => value !== day)
                    : [...form.weekdays, day],
                })
              }
              selected={form.weekdays}
            />
            {errors.weekdays ? (
              <p
                className="mt-1 text-xs text-destructive"
                id={templateFieldErrorId("weekdays")}
                role="alert"
              >
                {t("panels.schedule.validation.requiredWeekday")}
              </p>
            ) : null}
          </div>
        ) : null}

        {form.kind === "cron" ? (
          <TemplateField
            action={
              <CronAssist
                onPick={(expression) => onChange({ cron: expression })}
                value={form.cron}
              />
            }
            className="sm:col-span-2"
            error={templateErrorText(t, errors.cron)}
            errorId={templateFieldErrorId("cron")}
            help={t("panels.schedule.help.cron")}
            helpLabel={t("panels.schedule.helpLabel", {
              field: t("panels.schedule.cronLabel"),
            })}
            label={t("panels.schedule.cronLabel")}
            required
          >
            <input
              className={templateFieldClassName}
              data-testid="automation-template-cron"
              onChange={(event) => onChange({ cron: event.target.value })}
              placeholder="*/5 * * * *"
              required
              spellCheck={false}
              value={form.cron}
              {...templateFieldA11y("cron", errors)}
            />
          </TemplateField>
        ) : null}
      </div>
    </TemplateSection>
  );
}
