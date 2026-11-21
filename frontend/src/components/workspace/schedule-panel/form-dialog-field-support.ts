// Shared class names, option tables and accessibility helpers for the schedule
// form dialog fields. Kept free of React components so it can be shared by the
// content/context/rule field modules without react-refresh boundary issues.

import {
  fieldControlVariants,
  fieldTextareaVariants,
} from "@/components/ui/field-variants";
import { cn } from "@/lib/utils";
import type { RuntimeScheduleTargetKind } from "@/types/runtime";

import type {
  CreateFormState,
  ScheduleFormErrors,
  ScheduleFormField,
} from "./form-state-types";

export const fieldClassName = fieldControlVariants({ size: "md" });
// 多行框统一压低最小高度：变体自带的 min-h-24（96px）对两个短内容字段太高，
// 空框会白占两行位置，把底部按钮顶到需要滚动才能看到。内容多了仍可手动拉伸。
export const textareaClassName = cn(
  fieldTextareaVariants({ size: "sm" }),
  "min-h-[4.25rem]",
);
// 执行步骤多数情况留空，给到接近单行输入的高度即可。
export const compactTextareaClassName = cn(
  fieldTextareaVariants({ size: "sm" }),
  "min-h-[3.25rem]",
);

export const TARGET_OPTIONS = [
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

/** 常用时区：覆盖国内与主流出海/协作场景，其余时区仍可在下方手填。 */
export const COMMON_TIME_ZONES = [
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

export const CUSTOM_ZONE_VALUE = "__custom__";

export type PatchForm = (patch: Partial<CreateFormState>) => void;

export type SectionFieldsProps = {
  createForm: CreateFormState;
  errors: ScheduleFormErrors;
  patchForm: PatchForm;
};

export function fieldA11y(
  field: ScheduleFormField,
  errors: ScheduleFormErrors,
) {
  return {
    "aria-describedby": errors[field] ? `schedule-field-error-${field}` : undefined,
    "aria-invalid": errors[field] ? true : undefined,
  };
}