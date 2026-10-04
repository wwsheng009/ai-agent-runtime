import { type Dispatch, type SetStateAction } from "react";
import { useTranslation } from "react-i18next";

import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import { type ProviderDraftInput } from "../runtime-provider-domain-form-utils";

type ProviderAdvancedFieldsProps = {
  draft: ProviderDraftInput;
  setDraft: Dispatch<SetStateAction<ProviderDraftInput>>;
};

/** 「高级」Tab：低频标量、支持类型与扩展字段 JSON。 */
export function ProviderAdvancedFields({
  draft,
  setDraft,
}: ProviderAdvancedFieldsProps) {
  const { t } = useTranslation("runtimeConfig");
  return (
    <>
      <div className="grid gap-3 xl:grid-cols-2">
        <ConfigFormField label={t("editor.providers.fields.timeout")}>
          <input
            className={editorControlClassName}
            value={draft.timeout}
            onChange={(event) =>
              setDraft((current) => ({ ...current, timeout: event.target.value }))
            }
            placeholder={t("editor.providers.fields.timeoutPlaceholder")}
          />
        </ConfigFormField>
        <ConfigFormField label={t("editor.providers.fields.truncationAdapter")}>
          <input
            className={editorControlClassName}
            value={draft.truncationAdapter}
            onChange={(event) =>
              setDraft((current) => ({
                ...current,
                truncationAdapter: event.target.value,
              }))
            }
            placeholder={t("editor.providers.fields.truncationAdapterPlaceholder")}
          />
        </ConfigFormField>
        <ConfigFormField
          label={t("editor.providers.fields.supportTypes")}
          description={t("editor.providers.fields.supportTypesDescription")}
        >
          <textarea
            className={`${editorControlClassName} min-h-24 resize-y font-mono`}
            value={draft.supportTypesText}
            onChange={(event) =>
              setDraft((current) => ({
                ...current,
                supportTypesText: event.target.value,
              }))
            }
          />
        </ConfigFormField>
      </div>

      <ConfigFormField
        label={t("editor.providers.fields.extraJson")}
        description={t("editor.providers.fields.extraJsonDescription")}
      >
        <textarea
          className={`${editorControlClassName} min-h-44 resize-y font-mono`}
          value={draft.extraJson}
          onChange={(event) =>
            setDraft((current) => ({ ...current, extraJson: event.target.value }))
          }
        />
      </ConfigFormField>
    </>
  );
}
