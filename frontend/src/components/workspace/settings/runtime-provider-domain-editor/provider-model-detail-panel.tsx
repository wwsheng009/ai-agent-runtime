/**
 * 单模型能力详情面板（参照 aicli micro web client 的模型编辑器右侧面板）。
 *
 * 面板内的每个字段都对应 config.yaml 的
 * `providers.items.<name>.model_capabilities.<model>.*`：
 *   * 空值 = 未声明，保存时从配置移除该字段；
 *   * 布尔字段只有勾选才会写入；
 *   * 未识别字段在草稿 extraFields 里原样保留。
 */

import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Select } from "@/components/ui/select";

import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import { type ProviderModelDraft } from "./model-capability-draft";
import {
  PROVIDER_MODALITY_VOCAB,
  toggleProviderModality,
} from "./model-capability-summary";

type ProviderModelDetailPanelProps = {
  draft: ProviderModelDraft;
  model: string;
  onChange: (draft: ProviderModelDraft) => void;
};

export function ProviderModelDetailPanel({
  draft,
  model,
  onChange,
}: ProviderModelDetailPanelProps) {
  const { t } = useTranslation("runtimeConfig");

  const update = (patch: Partial<ProviderModelDraft>) =>
    onChange({ ...draft, ...patch });

  return (
    <div className="rounded-card border border-border bg-surface-softer">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-3 py-2.5">
        <div className="min-w-0">
          <div className="truncate font-mono text-sm font-semibold text-foreground" title={model}>
            {model}
          </div>
          <div className="mt-0.5 text-xs text-muted-foreground">
            {t("editor.providers.models.editor.panelHint", {
              key: `providers.items.<name>.model_capabilities.${model}`,
            })}
          </div>
        </div>
        <Badge>{t("editor.providers.models.editor.singleModelBadge")}</Badge>
      </div>

      <div className="space-y-3 p-3">
        <section className="space-y-2">
          <SectionTitle
            text={t("editor.providers.models.editor.sections.reasoning")}
          />
          <div className="grid gap-2 md:grid-cols-2">
            <ConfigFormField
              label={t("editor.providers.models.editor.fields.reasoningModel")}
              description={t("editor.providers.models.editor.fields.reasoningModelHint")}
            >
              <BooleanToggle
                checked={draft.reasoningModel}
                label={t(
                  "editor.providers.models.editor.fields.reasoningModelToggle",
                )}
                onChange={(checked) => update({ reasoningModel: checked })}
              />
            </ConfigFormField>
            <ConfigFormField
              label={t(
                "editor.providers.models.editor.fields.replayReasoningContent",
              )}
              description={t(
                "editor.providers.models.editor.fields.replayReasoningContentHint",
              )}
            >
              <Select
                ariaLabel={t(
                  "editor.providers.models.editor.fields.replayReasoningContent",
                )}
                value={
                  draft.replayReasoningContent === null
                    ? "inherit"
                    : draft.replayReasoningContent
                      ? "force"
                      : "block"
                }
                onChange={(value) =>
                  update({
                    replayReasoningContent:
                      value === "inherit" ? null : value === "force",
                  })
                }
                options={[
                  {
                    value: "inherit",
                    label: t(
                      "editor.providers.models.editor.fields.replayInherit",
                    ),
                  },
                  {
                    value: "force",
                    label: t("editor.providers.models.editor.fields.replayForce"),
                  },
                  {
                    value: "block",
                    label: t("editor.providers.models.editor.fields.replayBlock"),
                  },
                ]}
                className="w-full"
                triggerClassName={editorControlClassName}
                optionClassName="text-sm"
              />
            </ConfigFormField>
            <ConfigFormField
              label={t("editor.providers.models.editor.fields.reasoningEfforts")}
              description={t(
                "editor.providers.models.editor.fields.reasoningEffortsHint",
              )}
            >
              <input
                className={editorControlClassName}
                value={draft.reasoningEffortsText}
                onChange={(event) =>
                  update({ reasoningEffortsText: event.target.value })
                }
                placeholder={t(
                  "editor.providers.models.editor.fields.reasoningEffortsPlaceholder",
                )}
                spellCheck={false}
              />
            </ConfigFormField>
            <ConfigFormField
              label={t(
                "editor.providers.models.editor.fields.defaultReasoningEffort",
              )}
            >
              <input
                className={editorControlClassName}
                value={draft.defaultReasoningEffort}
                onChange={(event) =>
                  update({ defaultReasoningEffort: event.target.value })
                }
                placeholder={t(
                  "editor.providers.models.editor.fields.defaultReasoningEffortPlaceholder",
                )}
                spellCheck={false}
              />
            </ConfigFormField>
            <ConfigFormField
              label={t(
                "editor.providers.models.editor.fields.reasoningEffortBudgets",
              )}
              description={t(
                "editor.providers.models.editor.fields.reasoningEffortBudgetsHint",
              )}
            >
              <textarea
                className={`${editorControlClassName} min-h-24 resize-y font-mono`}
                value={draft.reasoningEffortBudgetsText}
                onChange={(event) =>
                  update({ reasoningEffortBudgetsText: event.target.value })
                }
                placeholder={t(
                  "editor.providers.models.editor.fields.reasoningEffortBudgetsPlaceholder",
                )}
                spellCheck={false}
              />
            </ConfigFormField>
            <ConfigFormField
              label={t(
                "editor.providers.models.editor.fields.compactReasoningEffort",
              )}
              description={t(
                "editor.providers.models.editor.fields.compactReasoningEffortHint",
              )}
            >
              <input
                className={editorControlClassName}
                value={draft.compactReasoningEffort}
                onChange={(event) =>
                  update({ compactReasoningEffort: event.target.value })
                }
                placeholder={t(
                  "editor.providers.models.editor.fields.compactReasoningEffortPlaceholder",
                )}
                spellCheck={false}
              />
            </ConfigFormField>
          </div>
        </section>

        <section className="space-y-2">
          <SectionTitle text={t("editor.providers.models.editor.sections.context")} />
          <div className="grid gap-2 md:grid-cols-2">
            <ConfigFormField
              label={t("editor.providers.models.editor.fields.maxContextTokens")}
              description={t(
                "editor.providers.models.editor.fields.maxContextTokensHint",
              )}
            >
              <input
                type="number"
                min={0}
                className={editorControlClassName}
                value={draft.maxContextTokensText}
                onChange={(event) =>
                  update({ maxContextTokensText: event.target.value })
                }
                placeholder="128000"
              />
            </ConfigFormField>
            <ConfigFormField
              label={t("editor.providers.models.editor.fields.maxTokens")}
              description={t("editor.providers.models.editor.fields.maxTokensHint")}
            >
              <input
                type="number"
                min={0}
                className={editorControlClassName}
                value={draft.maxTokensText}
                onChange={(event) => update({ maxTokensText: event.target.value })}
                placeholder="8192"
              />
            </ConfigFormField>
          </div>
        </section>

        <section className="space-y-2">
          <SectionTitle text={t("editor.providers.models.editor.sections.compact")} />
          <div className="grid gap-2 md:grid-cols-2">
            <ConfigFormField
              label={t(
                "editor.providers.models.editor.fields.autoCompactRatio",
              )}
              description={t(
                "editor.providers.models.editor.fields.autoCompactRatioHint",
              )}
            >
              <input
                type="number"
                min={0}
                step="any"
                className={editorControlClassName}
                value={draft.autoCompactRatioText}
                onChange={(event) =>
                  update({ autoCompactRatioText: event.target.value })
                }
                placeholder="0.75"
              />
            </ConfigFormField>
            <ConfigFormField
              label={t(
                "editor.providers.models.editor.fields.autoCompactTokenLimit",
              )}
              description={t(
                "editor.providers.models.editor.fields.autoCompactTokenLimitHint",
              )}
            >
              <input
                type="number"
                min={0}
                className={editorControlClassName}
                value={draft.autoCompactTokenLimitText}
                onChange={(event) =>
                  update({ autoCompactTokenLimitText: event.target.value })
                }
                placeholder="200000"
              />
            </ConfigFormField>
            <ConfigFormField
              label={t("editor.providers.models.editor.fields.autoCompactMode")}
              description={t(
                "editor.providers.models.editor.fields.autoCompactModeHint",
              )}
            >
              <input
                className={editorControlClassName}
                value={draft.autoCompactMode}
                onChange={(event) =>
                  update({ autoCompactMode: event.target.value })
                }
                placeholder={t(
                  "editor.providers.models.editor.fields.autoCompactModePlaceholder",
                )}
                spellCheck={false}
              />
            </ConfigFormField>
            <ConfigFormField
              label={t(
                "editor.providers.models.editor.fields.supportsRemoteCompact",
              )}
              description={t(
                "editor.providers.models.editor.fields.supportsRemoteCompactHint",
              )}
            >
              <BooleanToggle
                checked={draft.supportsRemoteCompact}
                label={t(
                  "editor.providers.models.editor.fields.supportsRemoteCompactToggle",
                )}
                onChange={(checked) =>
                  update({ supportsRemoteCompact: checked })
                }
              />
            </ConfigFormField>
          </div>
        </section>

        <section className="space-y-2">
          <SectionTitle
            text={t("editor.providers.models.editor.sections.modalities")}
          />
          <ModalityPicker
            draft={draft}
            onChange={(modalities) => update({ inputModalities: modalities })}
          />
        </section>

        <section className="space-y-2">
          <SectionTitle
            text={t("editor.providers.models.editor.sections.nativeTools")}
          />
          <div className="flex flex-wrap gap-3 rounded-card border border-border bg-surface p-3">
            <BooleanToggle
              checked={draft.imageGeneration}
              label={t(
                "editor.providers.models.editor.fields.imageGeneration",
              )}
              onChange={(checked) => update({ imageGeneration: checked })}
            />
            <BooleanToggle
              checked={draft.imagesGenerationsApi}
              label={t(
                "editor.providers.models.editor.fields.imagesGenerationsApi",
              )}
              onChange={(checked) => update({ imagesGenerationsApi: checked })}
            />
          </div>
          {draft.imageGeneration ? (
            <p className="text-xs text-analytics-warning">
              {t("editor.providers.models.editor.modality.imageGenWarning")}
            </p>
          ) : null}
        </section>

        <p className="text-xs text-muted-foreground">
          {t("editor.providers.models.editor.clearFieldHint")}
        </p>
      </div>
    </div>
  );
}

function SectionTitle({ text }: { text: string }) {
  return (
    <div className="flex items-center gap-2">
      <span className="app-text-11 font-semibold uppercase tracking-[0.14em] text-accent-secondary">
        {text}
      </span>
      <span className="h-px flex-1 bg-border" />
    </div>
  );
}

function BooleanToggle({
  checked,
  label,
  onChange,
}: {
  checked: boolean;
  label: string;
  onChange: (checked: boolean) => void;
}) {
  return (
    <label className="flex items-center gap-2 text-sm text-foreground">
      <input
        type="checkbox"
        className="h-4 w-4 accent-accent-primary"
        checked={checked}
        onChange={(event) => onChange(event.target.checked)}
      />
      {label}
    </label>
  );
}

function ModalityPicker({
  draft,
  onChange,
}: {
  draft: ProviderModelDraft;
  onChange: (modalities: string[]) => void;
}) {
  const { t } = useTranslation("runtimeConfig");
  const known = PROVIDER_MODALITY_VOCAB.filter((entry) =>
    draft.inputModalities.includes(entry.code),
  );
  const unknown = draft.inputModalities.filter(
    (value) => !PROVIDER_MODALITY_VOCAB.some((entry) => entry.code === value),
  );

  const note =
    draft.inputModalities.length === 0
      ? t("editor.providers.models.editor.modality.noteInherit")
      : t("editor.providers.models.editor.modality.noteHonored");

  return (
    <div className="rounded-card border border-border bg-surface p-3">
      <div className="flex flex-wrap gap-1.5">
        {PROVIDER_MODALITY_VOCAB.map((entry) => {
          const active = draft.inputModalities.includes(entry.code);
          return (
            <button
              key={entry.code}
              type="button"
              aria-pressed={active}
              title={
                entry.honored
                  ? t("editor.providers.models.editor.modality.honoredHint")
                  : t("editor.providers.models.editor.modality.inertHint")
              }
              className={modalityChipClassName(active, !entry.honored)}
              onClick={() =>
                onChange(
                  toggleProviderModality(
                    draft.inputModalities,
                    entry.code,
                    !active,
                  ),
                )
              }
            >
              {entry.label}
            </button>
          );
        })}
        {unknown.map((value) => (
          <button
            key={value}
            type="button"
            aria-pressed
            title={t("editor.providers.models.editor.modality.unknownHint")}
            className={modalityChipClassName(true, true)}
            onClick={() =>
              onChange(
                toggleProviderModality(draft.inputModalities, value, false),
              )
            }
          >
            {value} ×
          </button>
        ))}
      </div>
      <p className="mt-2 text-xs text-muted-foreground">{note}</p>
      {known.some((entry) => !entry.honored) ? (
        <p className="mt-1 text-xs text-muted-foreground">
          {t("editor.providers.models.editor.modality.noteInert")}
        </p>
      ) : null}
    </div>
  );
}

function modalityChipClassName(active: boolean, inert: boolean): string {
  const base =
    "rounded-full border px-2 py-0.5 text-xs transition-colors";
  if (active && inert) {
    return `${base} border-analytics-warning-border bg-analytics-warning-soft text-analytics-warning`;
  }
  if (active) {
    return `${base} border-accent-primary-border bg-accent-primary-soft text-accent-primary`;
  }
  return `${base} border-border bg-surface-solid text-muted-foreground hover:text-foreground`;
}
