// 自动化任务新建对话框「运行规则」分区：规则类型、时间/时区、
// 星期与 Cron 表达式。

import { CalendarClockIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Select } from "@/components/ui/select";
import { scheduleKindLabelKey } from "@/components/workspace/schedule-panel-shared";
import { cn } from "@/lib/utils";
import type { RuntimeScheduleKind } from "@/types/runtime";

import { CronAssist } from "./cron-assist";
import { Field, FormSection } from "./form-dialog-field-primitives";
import {
  COMMON_TIME_ZONES,
  CUSTOM_ZONE_VALUE,
  fieldA11y,
  fieldClassName,
  type SectionFieldsProps,
} from "./form-dialog-field-support";
import { createKinds } from "./form-state-types";
import { WeekdayPicker } from "./weekday-picker";

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