import type { TFunction } from "i18next";

import {
  fieldControlVariants,
  fieldTextareaVariants,
} from "@/components/ui/field-variants";
import type { SelectOption } from "@/components/ui/select";
import { cn } from "@/lib/utils";

import type { AutomationTemplateFormErrors } from "./templates";

export const templateFieldClassName = fieldControlVariants({ size: "md" });
export const templateTextareaClassName = cn(
  fieldTextareaVariants({ size: "sm" }),
  "min-h-[4.25rem]",
);
export const templateCompactTextareaClassName = cn(
  fieldTextareaVariants({ size: "sm" }),
  "min-h-[3.25rem]",
);

export type TemplateFieldKey = keyof AutomationTemplateFormErrors;

/** Keep persisted values visible even when the live catalog no longer lists them. */
export function withCurrentSelectOption(
  options: SelectOption[],
  current: string,
): SelectOption[] {
  if (!current || options.some((option) => option.value === current)) {
    return options;
  }
  return [...options, { value: current, label: current }];
}

export function templateFieldErrorId(field: TemplateFieldKey): string {
  return `automation-template-error-${field}`;
}

/** Connect inline errors to their controls without duplicating ids per field. */
export function templateFieldA11y(
  field: TemplateFieldKey,
  errors: AutomationTemplateFormErrors,
) {
  return {
    "aria-describedby": errors[field]
      ? templateFieldErrorId(field)
      : undefined,
    "aria-invalid": errors[field] ? true : undefined,
  };
}

export function templateErrorText(
  t: TFunction<"workspace">,
  key: string | undefined,
): string | undefined {
  if (!key) return undefined;
  if (key === "required")
    return t("panels.schedule.templates.validation.required");
  if (key === "positive")
    return t("panels.schedule.templates.validation.positive");
  if (key === "clock")
    return t("panels.schedule.templates.validation.clock");
  return t("panels.schedule.templates.validation.invalid");
}
