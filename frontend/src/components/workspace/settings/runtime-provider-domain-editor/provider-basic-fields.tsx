import { type Dispatch, type SetStateAction } from "react";
import { useTranslation } from "react-i18next";

import { Select } from "@/components/ui/select";

import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import { type ProviderDraftInput } from "../runtime-provider-domain-form-utils";

import { providerProtocolOptions } from "./draft-utils";

type ProviderBasicFieldsProps = {
  draft: ProviderDraftInput;
  setDraft: Dispatch<SetStateAction<ProviderDraftInput>>;
};

export function ProviderBasicFields({ draft, setDraft }: ProviderBasicFieldsProps) {
  const { t } = useTranslation("runtimeConfig");
  return (
    <>
          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField
              label={t("editor.providers.fields.name")}
              description={t("editor.providers.fields.nameDescription")}
            >
              <input
                className={editorControlClassName}
                value={draft.name}
                onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value }))}
                placeholder={t("editor.providers.fields.namePlaceholder")}
              />
            </ConfigFormField>
            <ConfigFormField
              label={t("editor.providers.fields.protocol")}
              description={t("editor.providers.fields.protocolDescription")}
            >
              <Select
                ariaLabel={t("editor.providers.fields.protocolAria")}
                value={draft.protocol}
                onChange={(value) =>
                  setDraft((current) => ({ ...current, protocol: value }))
                }
                options={providerProtocolOptions}
                className="w-full"
                triggerClassName={editorControlClassName}
                optionClassName="text-sm"
              />
            </ConfigFormField>
            <ConfigFormField label={t("editor.providers.fields.baseUrl")}>
              <input
                className={editorControlClassName}
                value={draft.baseUrl}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, baseUrl: event.target.value }))
                }
                placeholder={t("editor.providers.fields.baseUrlPlaceholder")}
              />
            </ConfigFormField>
            <ConfigFormField label={t("editor.providers.fields.defaultModel")}>
              <input
                className={editorControlClassName}
                value={draft.defaultModel}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, defaultModel: event.target.value }))
                }
                placeholder={t("editor.providers.fields.defaultModelPlaceholder")}
              />
            </ConfigFormField>
            <ConfigFormField label={t("editor.providers.fields.apiPath")}>
              <input
                className={editorControlClassName}
                value={draft.apiPath}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, apiPath: event.target.value }))
                }
                placeholder={t("editor.providers.fields.apiPathPlaceholder")}
              />
            </ConfigFormField>
            <ConfigFormField label={t("editor.providers.fields.forwardUrl")}>
              <input
                className={editorControlClassName}
                value={draft.forwardUrl}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, forwardUrl: event.target.value }))
                }
                placeholder={t("editor.providers.fields.forwardUrlPlaceholder")}
              />
            </ConfigFormField>
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
          </div>

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField
              label={t("editor.providers.fields.supportedModels")}
              description={t("editor.providers.fields.supportedModelsDescription")}
            >
              <textarea
                className={`${editorControlClassName} min-h-36 resize-y font-mono`}
                value={draft.supportedModelsText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    supportedModelsText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label={t("editor.providers.fields.supportTypes")}
              description={t("editor.providers.fields.supportTypesDescription")}
            >
              <textarea
                className={`${editorControlClassName} min-h-36 resize-y font-mono`}
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
            label={t("editor.providers.fields.apiKey")}
            description={t("editor.providers.fields.apiKeyDescription")}
          >
            <textarea
              className={`${editorControlClassName} min-h-28 resize-y font-mono`}
              value={draft.apiKey}
              onChange={(event) =>
                setDraft((current) => ({ ...current, apiKey: event.target.value }))
              }
            />
          </ConfigFormField>
    </>
  );
}
