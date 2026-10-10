/**
 * 模型能力编辑器（列表 + 详情面板）——参照 aicli micro web client 的模型编辑器。
 *
 *   * 左侧列表：过滤、逐行摘要 chip、手动添加（支持一次粘贴多个）、移除；
 *   * 列表支持键盘导航：↑/↓/Home/End 移动焦点、Enter 打开（roving tabindex）；
 *   * 右侧面板：单模型能力详情（ProviderModelDetailPanel）；
 *   * 移除带撤销，避免误点 × 丢掉已填配置；
 *   * 组件受控：所有变更通过 onChange 回写父级草稿，自身只保留选择 / 过滤 /
 *     提示等瞬态 UI 状态。
 */

import { type KeyboardEvent, useMemo, useRef, useState } from "react";
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
import { summarizeProviderModelDraft } from "./model-capability-summary";
import { ProviderModelDetailPanel } from "./provider-model-detail-panel";
import { ModelListRow } from "./provider-model-list-row";

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
  // 列表键盘导航的 roving tabindex：只有「当前行」在 Tab 顺序里，方向键换行。
  const [rovingModel, setRovingModel] = useState<string | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);

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

  const configuredCount = useMemo(
    () =>
      orderedModels.filter((model) => {
        const draft = drafts[model];
        return draft ? hasConfiguredCapability(draft) : false;
      }).length,
    [drafts, orderedModels],
  );

  // 过滤后原 roving 行可能不可见：回退到当前选中行或首个可见行。
  const tabbableModel =
    rovingModel && visibleModels.includes(rovingModel)
      ? rovingModel
      : (activeModel ?? visibleModels[0] ?? null);

  function handleListKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const keys = ["ArrowDown", "ArrowUp", "Home", "End", "Enter"];
    if (!keys.includes(event.key)) {
      return;
    }
    const rows = listRef.current
      ? Array.from(
          listRef.current.querySelectorAll<HTMLElement>("[data-model-row]"),
        )
      : [];
    if (rows.length === 0) {
      return;
    }
    const currentRow = document.activeElement?.closest("[data-model-row]");
    const currentIndex = currentRow
      ? rows.indexOf(currentRow as HTMLElement)
      : -1;
    if (currentIndex < 0) {
      return;
    }
    if (event.key === "Enter") {
      // 只接管「选中按钮」上的 Enter：移除按钮保持原生 click 语义。
      if (
        !(
          event.target instanceof HTMLButtonElement &&
          event.target.dataset.modelSelect === "true"
        )
      ) {
        return;
      }
      event.preventDefault();
      event.target.click();
      return;
    }
    event.preventDefault();
    const nextIndex =
      event.key === "Home"
        ? 0
        : event.key === "End"
          ? rows.length - 1
          : event.key === "ArrowDown"
            ? Math.min(currentIndex + 1, rows.length - 1)
            : Math.max(currentIndex - 1, 0);
    const nextRow = rows[nextIndex];
    setRovingModel(nextRow.dataset.modelRow ?? null);
    nextRow
      .querySelector<HTMLButtonElement>("button[data-model-select]")
      ?.focus();
  }

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
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span>
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
          </span>
          {orderedModels.length > 0 ? (
            <span
              className={cn(
                "rounded-chip border px-1.5 py-0.5 app-text-10",
                configuredCount > 0
                  ? "border-accent-primary-border text-accent-primary"
                  : "border-border text-muted-foreground",
              )}
            >
              {t("editor.providers.models.editor.configuredCount", {
                configured: String(configuredCount),
                total: String(orderedModels.length),
              })}
            </span>
          ) : null}
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
        <div
          className="max-h-[30rem] space-y-1.5 overflow-y-auto rounded-card border border-border bg-surface-softer p-2"
          ref={listRef}
          onKeyDown={handleListKeyDown}
        >
          {orderedModels.length === 0 ? (
            <div className="px-1 py-6 text-center text-xs text-muted-foreground">
              {emptyHint ?? t("editor.providers.models.editor.empty")}
            </div>
          ) : visibleModels.length === 0 ? (
            <div className="flex flex-col items-center gap-1.5 px-1 py-5 text-center text-xs text-muted-foreground">
              <span>
                {t("editor.providers.models.editor.emptyFiltered", {
                  filter: filter.trim(),
                })}
              </span>
              <Button size="sm" variant="ghost" onClick={() => setFilter("")}>
                {t("editor.providers.models.editor.clearFilter")}
              </Button>
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
                tabIndex={model === tabbableModel ? 0 : -1}
                onFocus={() => setRovingModel(model)}
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

