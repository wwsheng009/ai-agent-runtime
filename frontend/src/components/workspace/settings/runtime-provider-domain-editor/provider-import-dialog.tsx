/**
 * 「自动导入」弹窗：与 aicli micro web client 的 `#config-auto-import-btn` 对齐，
 * 用「名称 + Base URL (+ API Key)」一步生成 provider：后端负责协议探测、
 * 模型列表拉取与 api_path / forward_url / default_model / support_types 推导。
 *
 * 与 micro client 的差异（有意为之）：runtime server 的 auto-import 接口不落盘，
 * 因此这里把响应折算成完整草稿后交给既有保存链路（onImport），保存后仍需按
 * 编辑器惯例统一写回 `config.yaml`。
 */

import { useState } from "react";
import { useTranslation } from "react-i18next";

import { autoImportRuntimeProvider } from "@/api/runtime";

import { ConfigDomainDialog } from "../config-domain-dialog";
import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import {
  buildProviderDraftFromAutoImport,
  providerImportAutoProtocol,
  validateProviderImportInput,
} from "../provider-ops-utils";
import { SettingsDialogFooter } from "../settings-dialog-footer";
import { SettingsNoticeCard } from "../settings-notice-card";

import { describeAccountError, providerProtocolOptions } from "./draft-utils";

const importProtocolOptions = [
  { value: providerImportAutoProtocol, label: "" },
  ...providerProtocolOptions.map((option) => ({ ...option })),
];

function createImportFormState() {
  return {
    name: "",
    baseUrl: "",
    apiKey: "",
    protocol: providerImportAutoProtocol,
    defaultModel: "",
    modelsPath: "",
    setAsDefault: false,
  };
}

export type ProviderImportResultSummary = {
  name: string;
  protocol: string;
  modelCount: number;
  warnings: string[];
};

type ProviderImportDialogProps = {
  defaultProvider: string;
  onClose: () => void;
  onImport: (result: ProviderImportResultSummary, draft: ReturnType<typeof buildProviderDraftFromAutoImport>) => string | null;
  open: boolean;
};

export function ProviderImportDialog({
  defaultProvider,
  onClose,
  onImport,
  open,
}: ProviderImportDialogProps) {
  const { t } = useTranslation("runtimeConfig");
  const [form, setForm] = useState(createImportFormState);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function updateField<K extends keyof ReturnType<typeof createImportFormState>>(
    key: K,
    value: ReturnType<typeof createImportFormState>[K],
  ) {
    setForm((current) => ({ ...current, [key]: value }));
  }

  function handleClose() {
    if (busy) {
      return;
    }
    setForm(createImportFormState());
    setError(null);
    onClose();
  }

  async function handleSubmit() {
    const resolved = validateProviderImportInput(form);
    if (!resolved) {
      setError(t("editor.providers.import.requiresNameAndBaseUrl"));
      return;
    }

    setBusy(true);
    setError(null);
    try {
      const result = await autoImportRuntimeProvider({
        name: resolved.name,
        base_url: resolved.baseUrl,
        api_key: form.apiKey.trim() || undefined,
        protocol:
          form.protocol === providerImportAutoProtocol ? undefined : form.protocol,
        models_path: form.modelsPath.trim() || undefined,
        default_model: form.defaultModel.trim() || undefined,
      });
      const draft = buildProviderDraftFromAutoImport(
        result,
        resolved.name,
        defaultProvider,
      );
      draft.setAsDefault = form.setAsDefault;
      // 保留用户输入的 key：后端不回填请求侧秘密，但保存链路需要它才能写入配置。
      draft.apiKey = form.apiKey.trim();
      const saveError = onImport(
        {
          name: draft.name,
          protocol: result.protocol || "",
          modelCount: draft.supportedModelsText
            ? draft.supportedModelsText.split("\n").filter(Boolean).length
            : 0,
          warnings: result.warnings ?? [],
        },
        draft,
      );
      if (saveError) {
        setError(saveError);
        return;
      }
      setForm(createImportFormState());
      onClose();
    } catch (importError) {
      setError(
        describeAccountError(
          importError,
          t("editor.providers.import.failed"),
        ),
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <ConfigDomainDialog
      open={open}
      onClose={handleClose}
      title={t("editor.providers.import.title")}
      description={t("editor.providers.import.description")}
      widthClassName="max-w-2xl"
      footer={
        <SettingsDialogFooter
          buttonSize="sm"
          note={t("editor.providers.import.note")}
          confirmLabel={
            busy
              ? t("editor.providers.import.submitting")
              : t("editor.providers.import.submit")
          }
          onCancel={handleClose}
          onConfirm={() => void handleSubmit()}
        />
      }
    >
      <div className="space-y-3">
        {error ? (
          <SettingsNoticeCard tone="warning-soft">{error}</SettingsNoticeCard>
        ) : null}

        <div className="grid gap-3 xl:grid-cols-2">
          <ConfigFormField
            label={t("editor.providers.fields.name")}
            description={t("editor.providers.import.nameDescription")}
          >
            <input
              className={editorControlClassName}
              value={form.name}
              onChange={(event) => updateField("name", event.target.value)}
              placeholder={t("editor.providers.import.namePlaceholder")}
              disabled={busy}
            />
          </ConfigFormField>
          <ConfigFormField
            label={t("editor.providers.fields.protocol")}
            description={t("editor.providers.import.protocolDescription")}
          >
            <select
              className={editorControlClassName}
              aria-label={t("editor.providers.import.protocolAria")}
              value={form.protocol}
              onChange={(event) => updateField("protocol", event.target.value)}
              disabled={busy}
            >
              {importProtocolOptions.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.value === providerImportAutoProtocol
                    ? t("editor.providers.import.protocolAuto")
                    : option.label}
                </option>
              ))}
            </select>
          </ConfigFormField>
          <ConfigFormField
            label={t("editor.providers.fields.baseUrl")}
            description={t("editor.providers.import.baseUrlDescription")}
          >
            <input
              className={editorControlClassName}
              value={form.baseUrl}
              onChange={(event) => updateField("baseUrl", event.target.value)}
              placeholder={t("editor.providers.fields.baseUrlPlaceholder")}
              disabled={busy}
            />
          </ConfigFormField>
          <ConfigFormField
            label={t("editor.providers.fields.apiKey")}
            description={t("editor.providers.import.apiKeyDescription")}
          >
            <input
              className={editorControlClassName}
              type="password"
              value={form.apiKey}
              onChange={(event) => updateField("apiKey", event.target.value)}
              placeholder={t("editor.providers.import.apiKeyPlaceholder")}
              disabled={busy}
            />
          </ConfigFormField>
          <ConfigFormField
            label={t("editor.providers.fields.defaultModel")}
            description={t("editor.providers.import.defaultModelDescription")}
          >
            <input
              className={editorControlClassName}
              value={form.defaultModel}
              onChange={(event) => updateField("defaultModel", event.target.value)}
              placeholder={t("editor.providers.import.defaultModelPlaceholder")}
              disabled={busy}
            />
          </ConfigFormField>
          <ConfigFormField
            label={t("editor.providers.import.modelsPath")}
            description={t("editor.providers.import.modelsPathDescription")}
          >
            <input
              className={editorControlClassName}
              value={form.modelsPath}
              onChange={(event) => updateField("modelsPath", event.target.value)}
              placeholder={t("editor.providers.import.modelsPathPlaceholder")}
              disabled={busy}
            />
          </ConfigFormField>
        </div>

        <label className="flex items-center gap-2 text-sm text-foreground">
          <input
            type="checkbox"
            className="h-4 w-4 accent-accent-primary"
            checked={form.setAsDefault}
            onChange={(event) => updateField("setAsDefault", event.target.checked)}
            disabled={busy}
          />
          {t("editor.providers.fields.setAsDefault")}
        </label>
      </div>
    </ConfigDomainDialog>
  );
}
