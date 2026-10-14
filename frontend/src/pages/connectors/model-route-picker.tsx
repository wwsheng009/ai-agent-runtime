// D6 连接器级模型路由选择器（docs/bots/09-multi-instance-binding.md §7.4.2）。
//
// 交互与聊天页 composer 的模型面板同构（复用 lib/composer/model-panel-model.ts
// 的纯逻辑：分段构建 / 换供应商后的「待重选」推进），但形态适配对话框内嵌：
//   * 一级 = provider / model / 推理档位 三行摘要，点行进二级候选列表；
//   * 换供应商 → 模型候选按新供应商过滤（仍被支持的保留），并自动下钻模型列表
//     引导重选；推理档位为固定枚举；
//   * 「(全局默认)」选项代表清空该项（回落宿主全局默认）。
// 候选来自 runtime 模型目录（宿主投影），不推断、不缓存到 localStorage。

import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import { useEffect, useId, useRef, useState } from "react";
import { CheckIcon, ChevronDownIcon, ChevronLeftIcon, ChevronRightIcon, XIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { ConnectorRoutePolicyView } from "@/types/runtime";
import {
  advanceAfterModelSelect,
  advanceAfterProviderSelect,
  advanceAfterReasoningSelect,
  buildComposerModelSections,
  type ComposerModelPanelPending,
  type ComposerModelPanelSection,
  type ComposerModelPanelSectionId,
  type ComposerModelPanelView,
} from "@/lib/composer/model-panel-model";
import { cn } from "@/lib/utils";

export interface ModelRoutePickerProps {
  /** 当前路由值（来自连接器 policy.route）。 */
  value: {
    provider?: string;
    model?: string;
    reasoning_effort?: string;
  };
  /** runtime 模型目录投影：providers 名单 + 每个 provider 的模型清单。 */
  catalog: {
    providers: readonly { name: string; models: readonly string[] }[];
    defaultReasoningEffort?: string;
  };
  onChange: (value: ConnectorRoutePolicyView) => void;
}

const REASONING_EFFORTS = ["low", "medium", "high", "xhigh"] as const;

/** 选中 provider 的模型清单（无 provider 或目录缺失 = 空）。 */
function modelsFor(
  catalog: ModelRoutePickerProps["catalog"],
  provider: string,
): readonly string[] {
  if (!provider) {
    return [];
  }
  const record = catalog.providers.find((item) => item.name === provider);
  return record ? record.models : [];
}

export function ModelRoutePicker({ value, catalog, onChange }: ModelRoutePickerProps) {
  const { t } = useTranslation("connectors");
  const [open, setOpen] = useState(false);
  const [view, setView] = useState<ComposerModelPanelView>({ level: "root" });
  const [pending, setPending] = useState<ComposerModelPanelPending>({
    model: false,
    reasoning: false,
  });
  const [providerQuery, setProviderQuery] = useState("");
  const [modelQuery, setModelQuery] = useState("");
  const panelId = useId();
  const panelRef = useRef<HTMLDivElement | null>(null);
  const rootRef = useRef<HTMLDivElement | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);

  const selectedProvider = value.provider ?? "";
  const selectedModel = value.model ?? "";
  const selectedReasoning = value.reasoning_effort ?? "";

  // 模型候选跟随当前 provider（换 provider 由推进逻辑引导重选）。
  const modelOptions = modelsFor(catalog, selectedProvider);

  function commit(next: ConnectorRoutePolicyView) {
    onChange(next);
  }

  function handleProviderSelect(provider: string) {
    const changed = provider !== selectedProvider;
    // 换供应商后旧 model 大概率不被新 provider 支持：清空并由推进逻辑下钻引导。
    const keepModel = changed ? "" : selectedModel;
    commit({
      provider,
      model: keepModel,
      reasoning_effort: selectedReasoning,
    });
    setProviderQuery("");
    setModelQuery("");
    const advance = advanceAfterProviderSelect({
      providerChanged: changed,
      hasModel: hasSection("model"),
      hasReasoning: hasSection("reasoning"),
    });
    if (advance.pending) {
      setPending(advance.pending);
    }
    setView(advance.view);
  }

  function handleModelSelect(model: string) {
    commit({
      provider: selectedProvider,
      model,
      reasoning_effort: selectedReasoning,
    });
    setModelQuery("");
    const advance = advanceAfterModelSelect({
      pendingModel: pending.model,
      hasReasoning: hasSection("reasoning"),
    });
    if (!advance) {
      return;
    }
    setPending(advance.pending);
    setView(advance.view);
  }

  function handleReasoningSelect(effort: string) {
    commit({
      provider: selectedProvider,
      model: selectedModel,
      reasoning_effort: effort,
    });
    const advance = advanceAfterReasoningSelect(pending.reasoning);
    if (!advance) {
      return;
    }
    setPending(advance.pending);
    setView(advance.view);
  }

  const reasoningOptions = REASONING_EFFORTS;
  const defaultEffortLabel =
    catalog.defaultReasoningEffort ||
    t("connectorDialog.routeDefaultEffortFallback");
  const reasoningLabel = selectedReasoning || defaultEffortLabel;
  const unselectedLabel = t("connectorDialog.routeUnselected");

  const sections: ComposerModelPanelSection[] = buildComposerModelSections({
    defaultEffortLabel,
    labels: {
      provider: t("connectorDialog.routeProvider"),
      model: t("connectorDialog.routeModel"),
      reasoning: t("connectorDialog.routeReasoning"),
    },
    modelOptions,
    modelQuery,
    onModelSelect: handleModelSelect,
    onProviderSelect: handleProviderSelect,
    onReasoningSelect: handleReasoningSelect,
    providerOptions: catalog.providers.map((item) => item.name),
    providerQuery,
    reasoningEffortOptions: reasoningOptions,
    reasoningValue: reasoningLabel,
    selectedModel,
    selectedProvider,
    selectedReasoningEffort: selectedReasoning,
    unselectedLabel,
  });

  const modelPending = pending.model && hasSection("model");
  const reasoningPending = pending.reasoning && hasSection("reasoning");
  const activeSection =
    view.level === "section"
      ? sections.find((section) => section.id === view.section)
      : undefined;

  // 摘要触发器：三项各取当前生效值（空项显示「全局默认」占位）。
  const summaryLabel = [
    selectedProvider || t("connectorDialog.routeGlobalDefault"),
    selectedModel || t("connectorDialog.routeGlobalDefault"),
    reasoningLabel,
  ].join(" · ");

  function hasSection(id: ComposerModelPanelSectionId) {
    return sections.some((section) => section.id === id);
  }

  function goBackToRoot() {
    setView({ level: "root" });
    setProviderQuery("");
    setModelQuery("");
  }

  function openPanel() {
    setView({ level: "root" });
    setProviderQuery("");
    setModelQuery("");
    setOpen(true);
  }

  function closePanel(restoreFocus = false) {
    setOpen(false);
    if (restoreFocus) {
      requestAnimationFrame(() => triggerRef.current?.focus());
    }
  }

  useEffect(() => {
    if (!open) {
      return;
    }
    function onPointerDown(event: PointerEvent) {
      const target = event.target;
      if (!(target instanceof Node)) {
        return;
      }
      if (!rootRef.current?.contains(target) && !panelRef.current?.contains(target)) {
        closePanel();
      }
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key !== "Escape") {
        return;
      }
      if (view.level === "section") {
        setView({ level: "root" });
        return;
      }
      closePanel(true);
    }
    window.addEventListener("pointerdown", onPointerDown);
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("pointerdown", onPointerDown);
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [open, view]);

  function handlePanelKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    const filterFocused =
      event.target instanceof Element &&
      event.target.hasAttribute("data-composer-model-panel-filter");
    if (event.key === "ArrowLeft" && activeSection && !filterFocused) {
      event.preventDefault();
      goBackToRoot();
      return;
    }
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") {
      return;
    }
    const selector = activeSection
      ? `[data-composer-model-panel-option="${activeSection.id}"]`
      : "[data-composer-model-panel-row]";
    const nodes = panelRef.current?.querySelectorAll<HTMLButtonElement>(selector);
    if (!nodes || nodes.length === 0) {
      return;
    }
    event.preventDefault();
    const values = Array.from(nodes);
    const currentIndex = values.indexOf(document.activeElement as HTMLButtonElement);
    const step = event.key === "ArrowDown" ? 1 : -1;
    const nextIndex =
      currentIndex < 0
        ? step === 1
          ? 0
          : values.length - 1
        : (currentIndex + step + values.length) % values.length;
    values[nextIndex]?.focus();
  }

  return (
    <div ref={rootRef} className="relative">
      <p className="mb-1 text-xs text-muted-foreground">{t("connectorDialog.routeTitle")}</p>
      <button
        ref={triggerRef}
        type="button"
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => (open ? closePanel(true) : openPanel())}
        title={summaryLabel}
        className="flex h-9 w-full items-center justify-between gap-2 rounded-control border border-border bg-surface-soft px-2.5 text-sm text-foreground outline-none transition hover:border-accent-primary"
      >
        <span className="min-w-0 truncate">{summaryLabel}</span>
        <ChevronDownIcon size={14} className="shrink-0 text-muted-foreground" />
      </button>

      {open ? (
        <div
          ref={panelRef}
          id={panelId}
          role="dialog"
          aria-label={t("connectorDialog.routeTitle")}
          data-connector-route-panel
          data-connector-route-panel-level={activeSection ? "section" : "root"}
          onKeyDown={handlePanelKeyDown}
          tabIndex={-1}
          className={cn(
            "absolute left-0 top-full z-50 mt-1 flex w-max min-w-[min(22rem,100%)] flex-col overflow-hidden",
            "rounded-card-lg border border-border bg-surface-overlay shadow-[0_10px_24px_rgba(0,0,0,0.24)] outline-none",
          )}
        >
          <header className="flex items-center gap-2 border-b border-border/60 px-3 py-2">
            {activeSection ? (
              <button
                type="button"
                aria-label={t("composer.modelPanel.back")}
                onClick={goBackToRoot}
                className="inline-flex size-5 shrink-0 items-center justify-center rounded-control text-muted-foreground transition hover:bg-surface-soft hover:text-foreground"
              >
                <ChevronLeftIcon size={12} aria-hidden="true" />
              </button>
            ) : null}
            <span
              className="app-text-9 uppercase tracking-[0.12em] text-muted-foreground"
            >
              {activeSection
                ? activeSection.label
                : t("connectorDialog.routeTitle")}
            </span>
            <button
              type="button"
              aria-label={t("composer.modelPanel.close")}
              onClick={() => closePanel(true)}
              className="ml-auto inline-flex size-5 shrink-0 items-center justify-center rounded-control text-muted-foreground transition hover:bg-surface-soft hover:text-foreground"
            >
              <XIcon size={12} aria-hidden="true" />
            </button>
          </header>

          {activeSection ? (
            <div className="flex min-h-0 flex-col">
              {activeSection.id === "provider" || activeSection.id === "model" ? (
                <div className="flex shrink-0 items-center gap-1.5 border-b border-border/60 px-2.5 py-2">
                  <input
                    autoComplete="off"
                    aria-label={t("connectorDialog.routeFilterPlaceholder")}
                    data-composer-model-panel-filter={activeSection.id}
                    placeholder={t("connectorDialog.routeFilterPlaceholder")}
                    type="text"
                    value={activeSection.id === "provider" ? providerQuery : modelQuery}
                    onChange={(event) => {
                      if (activeSection.id === "provider") {
                        setProviderQuery(event.currentTarget.value);
                      } else {
                        setModelQuery(event.currentTarget.value);
                      }
                    }}
                    className="min-w-0 flex-1 bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground"
                  />
                </div>
              ) : null}
              <div
                role="listbox"
                className="flex max-h-64 min-h-0 flex-col gap-0.5 overflow-y-auto p-1.5"
              >
                {activeSection.options.map((option) => (
                  <button
                    key={`${activeSection.id}\u0000${option.value}`}
                    type="button"
                    role="option"
                    aria-selected={option.selected}
                    onClick={option.onSelect}
                    title={option.label}
                    className={cn(
                      "flex w-full cursor-pointer items-center gap-2 rounded-control px-2.5 py-2 text-left leading-5 transition",
                      option.selected
                        ? "bg-surface-soft text-foreground"
                        : "text-muted-foreground hover:bg-surface-soft hover:text-foreground",
                    )}
                  >
                    <span className="flex size-4 shrink-0 items-center justify-center">
                      {option.selected ? <CheckIcon size={13} aria-hidden="true" /> : null}
                    </span>
                    <span className="truncate text-sm">{option.label}</span>
                  </button>
                ))}
              </div>
            </div>
          ) : (
            <div
              role="group"
              className="flex min-h-0 flex-col gap-0.5 p-1.5"
            >
              {sections.map((section) => {
                const sectionPending =
                  section.id === "model"
                    ? modelPending
                    : section.id === "reasoning"
                      ? reasoningPending
                      : false;
                return (
                  <button
                    key={section.id}
                    type="button"
                    aria-haspopup="listbox"
                    onClick={() => setView({ level: "section", section: section.id })}
                    className={cn(
                      "flex w-full cursor-pointer items-center gap-2 rounded-control px-2.5 py-2 text-left transition",
                      "text-muted-foreground hover:bg-surface-soft hover:text-foreground",
                    )}
                  >
                    <span className="shrink-0 app-text-9 uppercase tracking-[0.12em]">
                      {section.label}
                    </span>
                    <span className="min-w-0 flex-1 truncate text-sm text-foreground">
                      {section.value}
                    </span>
                    {sectionPending ? (
                      <span className="shrink-0 rounded-chip border border-amber-300/40 px-1.5 app-text-10 text-amber-700">
                        {t("connectorDialog.routePendingConfirm")}
                      </span>
                    ) : null}
                    <ChevronRightIcon
                      size={13}
                      aria-hidden="true"
                      className="shrink-0 text-muted-foreground"
                    />
                  </button>
                );
              })}
            </div>
          )}
        </div>
      ) : null}
      <p className="mt-1 text-xs text-muted-foreground">{t("dialog.routeHint")}</p>
    </div>
  );
}
