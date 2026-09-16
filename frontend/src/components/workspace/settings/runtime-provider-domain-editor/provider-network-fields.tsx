import { type Dispatch, type SetStateAction } from "react";
import { useTranslation } from "react-i18next";

import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import { type ProviderDraftInput } from "../runtime-provider-domain-form-utils";

type ProviderNetworkFieldsProps = {
  draft: ProviderDraftInput;
  setDraft: Dispatch<SetStateAction<ProviderDraftInput>>;
};

export function ProviderNetworkFields({
  draft,
  setDraft,
}: ProviderNetworkFieldsProps) {
  const { t } = useTranslation("runtimeConfig");
  return (
    <>
          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField
              label={t("editor.providers.fields.headersJson")}
              description={t("editor.providers.fields.headersDescription")}
            >
              <textarea
                className={`${editorControlClassName} min-h-40 resize-y font-mono`}
                value={draft.headersJson}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, headersJson: event.target.value }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label={t("editor.providers.fields.modelMappingsJson")}
              description={t("editor.providers.fields.modelMappingsDescription")}
            >
              <textarea
                className={`${editorControlClassName} min-h-40 resize-y font-mono`}
                value={draft.modelMappingsJson}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    modelMappingsJson: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
          </div>

          <div className="rounded-card border border-border bg-surface-softer p-3">
            <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
              <div>
                <div className="app-text-13 font-semibold text-foreground">
                  {t("editor.providers.proxy.title")}
                </div>
                <div className="mt-1 text-xs text-muted-foreground">
                  {t("editor.providers.proxy.description")}
                </div>
              </div>
              <label className="flex items-center gap-2 text-sm text-foreground">
                <input
                  type="checkbox"
                  className="h-4 w-4 accent-accent-primary"
                  checked={draft.proxyEnabled}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      proxyEnabled: event.target.checked,
                    }))
                  }
                />
                {t("editor.providers.proxy.enable")}
              </label>
            </div>

            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField
                label={t("editor.providers.proxy.http")}
                description={t("editor.providers.proxy.httpDescription")}
              >
                <input
                  className={editorControlClassName}
                  value={draft.proxyHttp}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      proxyHttp: event.target.value,
                    }))
                  }
                  placeholder={t("editor.providers.proxy.proxyPlaceholder")}
                />
              </ConfigFormField>
              <ConfigFormField
                label={t("editor.providers.proxy.https")}
                description={t("editor.providers.proxy.httpsDescription")}
              >
                <input
                  className={editorControlClassName}
                  value={draft.proxyHttps}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      proxyHttps: event.target.value,
                    }))
                  }
                  placeholder={t("editor.providers.proxy.proxyPlaceholder")}
                />
              </ConfigFormField>
            </div>

            <div className="mt-3">
              <ConfigFormField
                label={t("editor.providers.proxy.noProxy")}
                description={t("editor.providers.proxy.noProxyDescription")}
              >
                <textarea
                  className={`${editorControlClassName} min-h-24 resize-y font-mono`}
                  value={draft.proxyNoProxy}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      proxyNoProxy: event.target.value,
                    }))
                  }
                  placeholder={t("editor.providers.proxy.noProxyPlaceholder")}
                />
              </ConfigFormField>
            </div>
          </div>
    </>
  );
}
