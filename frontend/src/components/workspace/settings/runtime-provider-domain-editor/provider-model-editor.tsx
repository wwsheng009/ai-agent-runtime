/**
 * 模型能力编辑器（列表 + 详情面板）——参照 aicli micro web client 的模型编辑器。
 *
 *   * 左侧列表：过滤、逐行摘要 chip、手动添加（支持一次粘贴多个）、移除；
 *   * 右侧面板：单模型能力详情（ProviderModelDetailPanel）；
 *   * 移除带撤销，避免误点 × 丢掉已填配置；
 *   * 组件受控：所有变更通过 onChange 回写父级草稿，自身只保留选择 / 过滤 /
 *     提示等瞬态 UI 状态。
 */

import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

import { editorControlClassName } from "../editor-control-class";
import {
  emptyProviderModelDraft,
  normalizeProviderModelIDs,
  type ProviderModelDraft,
  splitProviderModelIDInput,
} from "./model-capability-draft";
import {
  formatTokenCount,
  summarizeProviderModelDraft,
  type ProviderModelDraftSummary,
} from "./model-capability-summary";
import { ProviderModelDetailPanel } from "./provider-model-detail-panel";

type ProviderModelEditorProps = {
  defaultModel?: string;
  disabled?: boolean;
  drafts: Record<string, ProviderModelDraft>;
  emptyHint?: string;
  models: string[];
  onChange: (next: {
    drafts: Record<string, ProviderModelDraft>;
    models: string[];
  }) => void;
};

type RemovedModelRecord = {
  draft: ProviderModelDraft | undefined;
  index: number;
  model: string;
};

export function ProviderModelEditor({
  defaultModel = "",
  disabled = false,
  drafts,
  emptyHint,
  models,
  onChange,
}: ProviderModelEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [selectedModel, setSelectedModel] = useState<string | null>(null);
  const [filter, setFilter] = useState("");
  const [addText, setAddText] = useState("");
  const [notice, setNotice] = useState<string | null>(null);
  const [lastRemoved, setLastRemoved] = useState<RemovedModelRecord | null>(
    null,
  );

  const orderedModels = useMemo(() => normalizeProviderModelIDs(models), [models]);
  const visibleModels = useMemo(() => {
    const keyword = filter.trim().toLowerCase();
    if (!keyword) {
      return orderedModels;
    }
    return orderedModels.filter((model) =>
      model.toLowerCase().includes(keyword),
    );
  }, [filter, orderedModels]);

  const activeModel =
    selectedModel && orderedModels.includes(selectedModel)
      ? selectedModel
      : null;

  function commit(nextModels: string[], nextDrafts: Record<string, ProviderModelDraft>) {
    onChange({ models: nextModels, drafts: nextDrafts });
  }

  function handleAdd() {
    const parsed = splitProviderModelIDInput(addText);
    if (parsed.length === 0) {
      return;
    }
    const added: string[] = [];
    const duplicates: string[] = [];
    for (const model of parsed) {
      if (orderedModels.includes(model)) {
        duplicates.push(model);
      } else {
        added.push(model);
      }
    }
    if (added.length === 0) {
      setNotice(
        t("editor.providers.models.editor.addDuplicate", {
          models: duplicates.join(", "),
        }),
      );
      return;
    }
    const nextModels = [...orderedModels, ...added];
    commit(nextModels, drafts);
    setAddText("");
    setSelectedModel(added[0] ?? null);
    setLastRemoved(null);
    setNotice(
      duplicates.length > 0
        ? `${t("editor.providers.models.editor.added", { models: added.join(", ") })} ${t(
            "editor.providers.models.editor.addSkipped",
            { models: duplicates.join(", ") },
          )}`
        : t("editor.providers.models.editor.added", { models: added.join(", ") }),
    );
  }

  function handleRemove(model: string) {
    if (model === defaultModel) {
      return;
    }
    const index = orderedModels.indexOf(model);
    if (index < 0) {
      return;
    }
    const nextDrafts = { ...drafts };
    const removedDraft = nextDrafts[model];
    delete nextDrafts[model];
    setLastRemoved({ model, draft: removedDraft, index });
    if (selectedModel === model) {
      setSelectedModel(null);
    }
    commit(
      orderedModels.filter((entry) => entry !== model),
      nextDrafts,
    );
    const configuredSuffix =
      removedDraft && hasConfiguredCapability(removedDraft)
        ? t("editor.providers.models.editor.removedConfigured")
        : "";
    setNotice(
      t("editor.providers.models.editor.removed", {
        model,
        configured: configuredSuffix,
      }),
    );
  }

  function handleUndoRemove() {
    const record = lastRemoved;
    if (!record) {
      return;
    }
    setLastRemoved(null);
    if (orderedModels.includes(record.model)) {
      setNotice(
        t("editor.providers.models.editor.undoMissing", { model: record.model }),
      );
      return;
    }
    const nextModels = [...orderedModels];
    nextModels.splice(Math.min(record.index, nextModels.length), 0, record.model);
    const nextDrafts = { ...drafts };
    if (record.draft) {
      nextDrafts[record.model] = record.draft;
    }
    commit(nextModels, nextDrafts);
    setNotice(
      t("editor.providers.models.editor.restored", { model: record.model }),
    );
  }

  function handleDraftChange(model: string, draft: ProviderModelDraft) {
    commit(orderedModels, { ...drafts, [model]: draft });
  }

  const activeDraft = activeModel ? drafts[activeModel] : undefined;

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-xs text-muted-foreground">
          {orderedModels.length > 0
            ? visibleModels.length === orderedModels.length
              ? t("editor.providers.models.editor.modelCountAll", {
                  total: String(orderedModels.length),
                })
              : t("editor.providers.models.editor.modelCount", {
                  total: String(orderedModels.length),
                  visible: String(visibleModels.length),
                })
            : ""}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <input
            className={`${editorControlClassName} h-8 w-44 text-xs`}
            value={filter}
            onChange={(event) => setFilter(event.target.value)}
            placeholder={t("editor.providers.models.editor.filterPlaceholder")}
          />
        </div>
      </div>

      <div className="flex flex-wrap items-stretch gap-2">
        <input
          className={`${editorControlClassName} h-8 min-w-56 flex-1 text-xs`}
          value={addText}
          disabled={disabled}
          onChange={(event) => setAddText(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              handleAdd();
            }
          }}
          placeholder={t("editor.providers.models.editor.addPlaceholder")}
          spellCheck={false}
        />
        <Button
          size="sm"
          variant="secondary"
          disabled={disabled || addText.trim() === ""}
          onClick={handleAdd}
        >
          {t("editor.providers.models.editor.add")}
        </Button>
      </div>

      {notice ? (
        <div className="flex flex-wrap items-center gap-2 rounded-card border border-border bg-surface-softer px-2.5 py-1.5 text-xs text-muted-foreground">
          <span>{notice}</span>
          {lastRemoved ? (
            <Button size="sm" variant="ghost" onClick={handleUndoRemove}>
              {t("editor.providers.models.editor.undo")}
            </Button>
          ) : null}
        </div>
      ) : null}

      <div className="grid gap-2 lg:grid-cols-[minmax(15rem,20rem)_minmax(0,1fr)]">
        <div className="max-h-[30rem] space-y-1.5 overflow-y-auto rounded-card border border-border bg-surface-softer p-2">
          {orderedModels.length === 0 ? (
            <div className="px-1 py-4 text-center text-xs text-muted-foreground">
              {emptyHint ?? t("editor.providers.models.editor.empty")}
            </div>
          ) : visibleModels.length === 0 ? (
            <div className="px-1 py-4 text-center text-xs text-muted-foreground">
              {t("editor.providers.models.editor.emptyFiltered", {
                filter: filter.trim(),
              })}
            </div>
          ) : (
            visibleModels.map((model) => (
              <ModelListRow
                key={model}
                active={model === activeModel}
                configured={
                  drafts[model] ? hasConfiguredCapability(drafts[model]) : false
                }
                defaultModel={model === defaultModel}
                disabled={disabled}
                model={model}
                summary={summarizeProviderModelDraft(
                  drafts[model] ?? emptyProviderModelDraft(),
                )}
                onRemove={() => handleRemove(model)}
                onSelect={() => {
                  setNotice(null);
                  setSelectedModel(model === activeModel ? null : model);
                }}
              />
            ))
          )}
        </div>

        <div>
          {activeModel ? (
            <ProviderModelDetailPanel
              draft={activeDraft ?? emptyProviderModelDraft()}
              model={activeModel}
              onChange={(draft) => handleDraftChange(activeModel, draft)}
            />
          ) : (
            <div className="grid h-full min-h-40 place-items-center rounded-card border border-border bg-surface-softer p-4 text-xs text-muted-foreground">
              {t("editor.providers.models.editor.selectHint")}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function ModelListRow({
  active,
  configured,
  defaultModel,
  disabled,
  model,
  onRemove,
  onSelect,
  summary,
}: {
  active: boolean;
  configured: boolean;
  defaultModel: boolean;
  disabled: boolean;
  model: string;
  onRemove: () => void;
  onSelect: () => void;
  summary: ProviderModelDraftSummary;
}) {
  const { t } = useTranslation("runtimeConfig");
  return (
    <div
      className={cn(
        "flex items-stretch rounded-card border transition-colors",
        active
          ? "border-accent-primary-border bg-accent-primary-soft"
          : "border-border bg-surface-solid hover:border-accent-primary-border",
      )}
    >
      <button
        type="button"
        className="min-w-0 flex-1 px-2 py-1.5 text-left"
        aria-pressed={active}
        onClick={onSelect}
      >
        <span className="flex items-center gap-1.5">
          <span
            className="min-w-0 truncate font-mono text-xs text-foreground"
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
          className="grid w-7 place-items-center rounded-r-card text-muted-foreground transition hover:bg-surface-soft hover:text-foreground disabled:opacity-40"
          disabled={disabled}
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

function hasConfiguredCapability(draft: ProviderModelDraft): boolean {
  return (
    draft.reasoningModel ||
    draft.replayReasoningContent !== null ||
    draft.inputModalities.length > 0 ||
    draft.imageGeneration ||
    draft.imagesGenerationsApi ||
    draft.supportsRemoteCompact ||
    Object.keys(draft.extraFields).length > 0 ||
    draft.reasoningEffortsText.trim() !== "" ||
    draft.reasoningEffortBudgetsText.trim() !== "" ||
    draft.defaultReasoningEffort.trim() !== "" ||
    draft.compactReasoningEffort.trim() !== "" ||
    draft.maxContextTokensText.trim() !== "" ||
    draft.maxTokensText.trim() !== "" ||
    draft.autoCompactRatioText.trim() !== "" ||
    draft.autoCompactTokenLimitText.trim() !== "" ||
    draft.autoCompactMode.trim() !== ""
  );
}

