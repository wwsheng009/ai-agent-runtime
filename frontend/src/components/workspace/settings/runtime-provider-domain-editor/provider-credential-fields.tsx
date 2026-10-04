import { type Dispatch, type SetStateAction } from "react";
import { useTranslation } from "react-i18next";

import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import { type ProviderDraftInput } from "../runtime-provider-domain-form-utils";

type ProviderCredentialFieldsProps = {
  draft: ProviderDraftInput;
  setDraft: Dispatch<SetStateAction<ProviderDraftInput>>;
};

/** 「连接与凭据」Tab 的凭据部分：API Key / 模板占位符。 */
export function ProviderCredentialFields({
  draft,
  setDraft,
}: ProviderCredentialFieldsProps) {
  const { t } = useTranslation("runtimeConfig");
  return (
    <ConfigFormField
      label={t("editor.providers.fields.apiKey")}
      description={t("editor.providers.fields.apiKeyDescription")}
    >
      <input
        autoComplete="off"
        className={editorControlClassName}
        spellCheck={false}
        type="password"
        value={draft.apiKey}
        onChange={(event) =>
          setDraft((current) => ({ ...current, apiKey: event.target.value }))
        }
      />
    </ConfigFormField>
  );
}
