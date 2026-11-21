// 自动化任务新建对话框「任务内容」分区：标题与提示词。

import { FileTextIcon } from "lucide-react";
import type { RefObject } from "react";
import { useTranslation } from "react-i18next";

import { Field, FormSection } from "./form-dialog-field-primitives";
import {
  fieldA11y,
  fieldClassName,
  textareaClassName,
  type SectionFieldsProps,
} from "./form-dialog-field-support";

type ContentFieldsProps = SectionFieldsProps & {
  titleInputRef: RefObject<HTMLInputElement | null>;
};

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
            rows={3}
            value={createForm.prompt}
            {...fieldA11y("prompt", errors)}
          />
        </Field>
      </div>
    </FormSection>
  );
}