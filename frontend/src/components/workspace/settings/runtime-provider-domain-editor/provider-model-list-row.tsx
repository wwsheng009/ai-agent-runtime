/**
 * 模型列表行与摘要 chip（从 provider-model-editor.tsx 抽出，控制文件行数门禁）。
 *
 * 行内语义保持原样：左侧 button（aria-pressed 表示选中）+ 右侧移除按钮；
 * 键盘导航由列表容器统一处理（roving tabindex），本文件只接收 tabIndex /
 * onFocus。未配置的模型名弱化为 muted，选中与默认模型仍用强调色。
 */

import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";

import {
  formatTokenCount,
  type ProviderModelDraftSummary,
} from "./model-capability-summary";

type ModelListRowProps = {
  active: boolean;
  configured: boolean;
  defaultModel: boolean;
  disabled: boolean;
  model: string;
  onFocus: () => void;
  onRemove: () => void;
  onSelect: () => void;
  summary: ProviderModelDraftSummary;
  tabIndex: number;
};

export function ModelListRow({
  active,
  configured,
  defaultModel,
  disabled,
  model,
  onFocus,
  onRemove,
  onSelect,
  summary,
  tabIndex,
}: ModelListRowProps) {
  const { t } = useTranslation("runtimeConfig");
  return (
    <div
      className={cn(
        "flex items-stretch rounded-card border transition-colors",
        active
          ? "border-accent-primary-border bg-accent-primary-soft"
          : "border-border bg-surface-solid hover:border-accent-primary-border",
      )}
      data-model-row={model}
    >
      <button
        type="button"
        className="min-w-0 flex-1 px-2 py-1.5 text-left outline-none focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-accent-primary-border"
        aria-pressed={active}
        data-model-select="true"
        tabIndex={tabIndex}
        onClick={onSelect}
        onFocus={onFocus}
      >
        <span className="flex items-center gap-1.5">
          <span
            className={cn(
              "min-w-0 truncate font-mono text-xs",
              configured ? "text-foreground" : "text-muted-foreground",
            )}
            title={model}
          >
            {model}
          </span>
          {defaultModel ? (
            <span className="shrink-0 rounded-chip border border-accent-primary-border px-1.5 py-0.5 app-text-10 text-accent-primary">
              {t("editor.providers.models.editor.defaultBadge")}
            </span>
          ) : configured ? null : (
            <span className="shrink-0 rounded-chip border border-border px-1.5 py-0.5 app-text-10 text-muted-foreground">
              {t("editor.providers.models.editor.unconfiguredBadge")}
            </span>
          )}
        </span>
        <span className="mt-1 flex flex-wrap gap-1">
          <SummaryChips summary={summary} />
        </span>
      </button>
      {defaultModel ? (
        <span
          className="grid w-7 place-items-center text-muted-foreground"
          title={t("editor.providers.models.editor.pinnedHint")}
        >
          ·
        </span>
      ) : (
        <button
          type="button"
          className="grid w-8 place-items-center rounded-r-card text-muted-foreground outline-none transition hover:bg-surface-soft hover:text-foreground focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-accent-primary-border disabled:opacity-40"
          disabled={disabled}
          tabIndex={tabIndex}
          title={t("editor.providers.models.editor.removeTitle", { model })}
          aria-label={t("editor.providers.models.editor.removeTitle", { model })}
          onClick={onRemove}
        >
          ×
        </button>
      )}
    </div>
  );
}

function SummaryChips({ summary }: { summary: ProviderModelDraftSummary }) {
  const { t } = useTranslation("runtimeConfig");
  const chips: Array<{ key: string; label: string; highlight?: boolean }> = [];
  if (summary.maxContextTokens > 0) {
    chips.push({
      key: "context",
      label: `ctx ${formatTokenCount(summary.maxContextTokens)}`,
    });
  }
  if (summary.maxTokens > 0) {
    chips.push({
      key: "max-tokens",
      label: `out ${formatTokenCount(summary.maxTokens)}`,
    });
  }
  if (summary.reasoningModel) {
    chips.push({ key: "reasoning", label: "reasoning", highlight: true });
  }
  if (summary.reasoningEffortCount > 0) {
    chips.push({
      key: "efforts",
      label: t("editor.providers.models.editor.chipEfforts", {
        count: summary.reasoningEffortCount,
      }),
    });
  }
  if (summary.defaultReasoningEffort) {
    chips.push({
      key: "default-effort",
      label: t("editor.providers.models.editor.chipDefaultEffort", {
        effort: summary.defaultReasoningEffort,
      }),
    });
  }
  if (summary.autoCompactTokenLimit > 0) {
    chips.push({
      key: "compact-limit",
      label: t("editor.providers.models.editor.chipCompact", {
        tokens: formatTokenCount(summary.autoCompactTokenLimit),
      }),
    });
  }
  if (summary.autoCompactMode) {
    chips.push({
      key: "compact-mode",
      label: summary.autoCompactMode,
    });
  }
  if (summary.supportsRemoteCompact) {
    chips.push({
      key: "remote-compact",
      label: t("editor.providers.models.editor.chipRemoteCompact"),
    });
  }
  if (summary.replayReasoningContent === true) {
    chips.push({
      key: "replay-on",
      label: t("editor.providers.models.editor.chipReplayOn"),
    });
  } else if (summary.replayReasoningContent === false) {
    chips.push({
      key: "replay-off",
      label: t("editor.providers.models.editor.chipReplayOff"),
    });
  }
  if (summary.inputModalities.length > 0) {
    chips.push({
      key: "modalities",
      label: summary.inputModalities.join("/"),
    });
  }
  if (summary.imageGeneration) {
    chips.push({
      key: "image-generation",
      label: t("editor.providers.models.editor.chipImageGeneration"),
      highlight: true,
    });
  }
  if (summary.imagesGenerationsApi) {
    chips.push({
      key: "images-api",
      label: t("editor.providers.models.editor.chipImagesApi"),
    });
  }
  if (chips.length === 0) {
    return null;
  }
  return (
    <>
      {chips.map((chip) => (
        <span
          key={chip.key}
          className={cn(
            "rounded-chip border px-1.5 py-0.5 app-text-10",
            chip.highlight
              ? "border-accent-primary-border bg-accent-primary-soft text-accent-primary"
              : "border-border bg-surface-softer text-muted-foreground",
          )}
        >
          {chip.label}
        </span>
      ))}
    </>
  );
}
