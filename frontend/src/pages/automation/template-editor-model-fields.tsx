import { InfoIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Select } from "@/components/ui/select";
import type { RuntimeModelProviderRecord } from "@/types/runtime";

import {
  templateFieldClassName,
  withCurrentSelectOption,
} from "./template-editor-controls";
import { TemplateField } from "./template-editor-fields";
import type { AutomationTemplateForm } from "./templates";

export function TemplateModelFields({
  failed,
  form,
  loading,
  onChange,
  providers,
}: {
  failed: boolean;
  form: AutomationTemplateForm;
  loading: boolean;
  onChange: (patch: Partial<AutomationTemplateForm>) => void;
  providers: RuntimeModelProviderRecord[];
}) {
  const { t } = useTranslation("workspace");
  const modelListUnavailable = failed || (!loading && providers.length === 0);
  const providerOptions = withCurrentSelectOption(
    [
      { value: "", label: t("panels.schedule.providerDefault") },
      ...providers.map((provider) => ({
        value: provider.name,
        label: provider.name,
      })),
    ],
    form.provider,
  );
  const selectedProvider = providers.find(
    (provider) => provider.name === form.provider,
  );
  const modelOptions = withCurrentSelectOption(
    [
      { value: "", label: t("panels.schedule.modelDefault") },
      ...(selectedProvider?.models ?? []).map((model) => ({
        value: model,
        label: model,
      })),
    ],
    form.model,
  );
  const availableReasoningEfforts =
    selectedProvider?.model_capabilities?.[form.model]?.reasoning_efforts ?? [];
  const reasoningOptions = withCurrentSelectOption(
    [
      {
        value: "",
        label: t("panels.schedule.reasoningEffortDefault"),
      },
      ...availableReasoningEfforts.map((effort) => ({
        value: effort,
        label: effort,
      })),
    ],
    form.reasoningEffort,
  );
  const showReasoning =
    availableReasoningEfforts.length > 0 || form.reasoningEffort.length > 0;

  const handleProviderChange = (providerName: string) => {
    const provider = providers.find((item) => item.name === providerName);
    const model = provider?.models.includes(form.model) ? form.model : "";
    const reasoning =
      provider?.model_capabilities?.[model]?.reasoning_efforts ?? [];
    onChange({
      provider: providerName,
      model,
      reasoningEffort: reasoning.includes(form.reasoningEffort)
        ? form.reasoningEffort
        : "",
    });
  };

  const handleModelChange = (model: string) => {
    const reasoning =
      selectedProvider?.model_capabilities?.[model]?.reasoning_efforts ?? [];
    onChange({
      model,
      reasoningEffort: reasoning.includes(form.reasoningEffort)
        ? form.reasoningEffort
        : "",
    });
  };

  if (modelListUnavailable) {
    return (
      <>
        <TemplateField
          help={t("panels.schedule.modelHint")}
          helpLabel={t("panels.schedule.helpLabel", {
            field: t("panels.schedule.providerLabel"),
          })}
          label={t("panels.schedule.providerLabel")}
        >
          <input
            className={templateFieldClassName}
            data-testid="automation-template-provider"
            onChange={(event) => onChange({ provider: event.target.value })}
            placeholder={t("panels.schedule.providerDefault")}
            spellCheck={false}
            value={form.provider}
          />
        </TemplateField>
        <TemplateField
          help={t("panels.schedule.modelHint")}
          helpLabel={t("panels.schedule.helpLabel", {
            field: t("panels.schedule.modelLabel"),
          })}
          label={t("panels.schedule.modelLabel")}
        >
          <input
            className={templateFieldClassName}
            data-testid="automation-template-model"
            onChange={(event) => onChange({ model: event.target.value })}
            placeholder={t("panels.schedule.modelDefault")}
            spellCheck={false}
            value={form.model}
          />
        </TemplateField>
        <TemplateField
          className="sm:col-span-2"
          label={t("panels.schedule.reasoningEffortLabel")}
        >
          <input
            className={templateFieldClassName}
            data-testid="automation-template-reasoning-effort"
            onChange={(event) =>
              onChange({ reasoningEffort: event.target.value })
            }
            placeholder={t("panels.schedule.reasoningEffortDefault")}
            spellCheck={false}
            value={form.reasoningEffort}
          />
        </TemplateField>
        <p
          className="flex items-start gap-2 rounded-field border border-dashed border-border bg-surface-solid px-3 py-2 text-xs leading-5 text-muted-foreground sm:col-span-2"
          role="status"
        >
          <InfoIcon
            aria-hidden="true"
            className="mt-0.5 size-3.5 shrink-0 text-accent-secondary"
          />
          <span>
            {failed
              ? t("panels.schedule.templates.optionCatalogLoadFailed")
              : t("panels.schedule.templates.optionCatalogEmpty")}
          </span>
        </p>
      </>
    );
  }

  return (
    <>
      <TemplateField
        help={t("panels.schedule.modelHint")}
        helpLabel={t("panels.schedule.helpLabel", {
          field: t("panels.schedule.providerLabel"),
        })}
        label={t("panels.schedule.providerLabel")}
      >
        <Select
          ariaLabel={t("panels.schedule.providerLabel")}
          disabled={loading}
          onChange={handleProviderChange}
          options={providerOptions}
          triggerClassName={templateFieldClassName}
          triggerTestId="automation-template-provider"
          value={form.provider}
        />
      </TemplateField>
      <TemplateField
        help={t("panels.schedule.modelHint")}
        helpLabel={t("panels.schedule.helpLabel", {
          field: t("panels.schedule.modelLabel"),
        })}
        label={t("panels.schedule.modelLabel")}
      >
        <Select
          ariaLabel={t("panels.schedule.modelLabel")}
          disabled={loading || !form.provider}
          onChange={handleModelChange}
          options={modelOptions}
          triggerClassName={templateFieldClassName}
          triggerTestId="automation-template-model"
          value={form.model}
        />
      </TemplateField>
      {showReasoning ? (
        <TemplateField
          className="sm:col-span-2"
          help={t("panels.schedule.modelHint")}
          helpLabel={t("panels.schedule.helpLabel", {
            field: t("panels.schedule.reasoningEffortLabel"),
          })}
          label={t("panels.schedule.reasoningEffortLabel")}
        >
          <Select
            ariaLabel={t("panels.schedule.reasoningEffortLabel")}
            onChange={(reasoningEffort) => onChange({ reasoningEffort })}
            options={reasoningOptions}
            triggerClassName={templateFieldClassName}
            triggerTestId="automation-template-reasoning-effort"
            value={form.reasoningEffort}
          />
        </TemplateField>
      ) : null}
    </>
  );
}
