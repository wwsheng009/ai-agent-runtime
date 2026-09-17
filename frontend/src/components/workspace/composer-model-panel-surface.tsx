// composer 模型面板的弹层本体（从 composer-model-panel.tsx 抽出，P0-2 行数约束）。
//
// 分工：触发器、开关/定位/焦点归还留在 composer-model-panel.tsx；这里只画面板内容：
//   * 一级 = 三行摘要（有候选的段才出现），二级 = 当前这一段的候选列表；
//   * 「待确认」标签只挂在模型 / 推理两行上（换供应商后未显式确认的组合不算已定）；
//   * 交互语义（role=listbox/option、方向键、返回/关闭按钮）与抽出前逐字保持一致。
import type { KeyboardEvent as ReactKeyboardEvent, RefObject } from "react";
import { CheckIcon, ChevronLeftIcon, ChevronRightIcon, XIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { PopoverPosition } from "@/components/ui/popover-position";
import type {
  ComposerModelPanelSection,
  ComposerModelPanelSectionId,
} from "@/lib/composer/model-panel-model";
import { cn } from "@/lib/utils";

export type ComposerModelPanelSurfaceProps = {
  activeSection?: ComposerModelPanelSection;
  modelFilterEmpty: boolean;
  modelPending: boolean;
  modelQuery: string;
  reasoningPending: boolean;
  panelId: string;
  panelRef: RefObject<HTMLDivElement | null>;
  position: PopoverPosition;
  providerFilterEmpty: boolean;
  providerQuery: string;
  reselectHint: string;
  sections: readonly ComposerModelPanelSection[];
  onBackToRoot: () => void;
  onClose: () => void;
  onKeyDown: (event: ReactKeyboardEvent<HTMLDivElement>) => void;
  onModelQueryChange: (query: string) => void;
  onProviderQueryChange: (query: string) => void;
  onSelectSection: (section: ComposerModelPanelSectionId) => void;
};

/** 二级菜单：候选在切换后消失时退回一级，而不是渲染一个空列表。 */
export function ComposerModelPanelSurface({
  activeSection,
  modelFilterEmpty,
  modelPending,
  modelQuery,
  onBackToRoot,
  onClose,
  onKeyDown,
  onModelQueryChange,
  onProviderQueryChange,
  onSelectSection,
  panelId,
  panelRef,
  position,
  providerFilterEmpty,
  providerQuery,
  reasoningPending,
  reselectHint,
  sections,
}: ComposerModelPanelSurfaceProps) {
  const { t } = useTranslation("workspace");

  // 供应商 / 模型两段共用一套检索语义与标记，只有文案与查询状态按段区分。
  const filterSection =
    activeSection && (activeSection.id === "provider" || activeSection.id === "model")
      ? activeSection.id
      : null;
  const filterQuery = filterSection === "provider" ? providerQuery : modelQuery;
  const filterEmpty = filterSection === "provider" ? providerFilterEmpty : modelFilterEmpty;
  const filterPlaceholder =
    filterSection === "provider"
      ? t("composer.modelPanel.providerFilterPlaceholder")
      : t("composer.modelPanel.modelFilterPlaceholder");
  const filterEmptyLabel =
    filterSection === "provider"
      ? t("composer.modelPanel.providerFilterEmpty")
      : t("composer.modelPanel.modelFilterEmpty");

  function changeFilter(next: string) {
    if (filterSection === "provider") {
      onProviderQueryChange(next);
      return;
    }
    onModelQueryChange(next);
  }

  return (
    <div
      ref={panelRef}
      id={panelId}
      role="dialog"
      aria-label={t("composer.modelPanel.title")}
      data-composer-model-panel
      data-composer-model-panel-level={activeSection ? "section" : "root"}
      onKeyDown={onKeyDown}
      style={{ position: "fixed", ...position }}
      tabIndex={-1}
      className={cn(
        "z-[160] flex w-max max-w-[min(24rem,calc(100vw-1rem))] flex-col overflow-hidden",
        "rounded-card-lg border border-border bg-surface-overlay shadow-[0_10px_24px_rgba(0,0,0,0.24)] outline-none",
      )}
    >
      <header className="flex items-center gap-2 border-b border-border/60 px-3 py-2">
        {activeSection ? (
          <button
            type="button"
            aria-label={t("composer.modelPanel.back")}
            data-composer-model-panel-back
            onClick={onBackToRoot}
            title={t("composer.modelPanel.back")}
            className="inline-flex size-5 shrink-0 items-center justify-center rounded-control text-muted-foreground transition hover:bg-surface-soft hover:text-foreground"
          >
            <ChevronLeftIcon size={12} aria-hidden="true" />
          </button>
        ) : null}
        <span
          id={`${panelId}-heading`}
          data-composer-model-panel-title
          className="app-text-9 uppercase tracking-[0.12em] text-muted-foreground"
        >
          {activeSection ? activeSection.label : t("composer.modelPanel.title")}
        </span>
        <button
          type="button"
          aria-label={t("composer.modelPanel.close")}
          data-composer-model-panel-close
          onClick={onClose}
          title={t("composer.modelPanel.close")}
          className="ml-auto inline-flex size-5 shrink-0 items-center justify-center rounded-control text-muted-foreground transition hover:bg-surface-soft hover:text-foreground"
        >
          <XIcon size={12} aria-hidden="true" />
        </button>
      </header>
      {activeSection ? (
        <div
          data-composer-model-panel-section={activeSection.id}
          className="flex max-h-[inherit] min-h-0 flex-col"
        >
          {/* 候选可能很多：检索框固定在列表上方，不随列表一起滚走。 */}
          {filterSection ? (
            <div className="flex shrink-0 items-center gap-1.5 border-b border-border/60 px-2.5 py-2">
              <input
                autoComplete="off"
                aria-label={filterPlaceholder}
                data-composer-model-panel-filter={filterSection}
                placeholder={filterPlaceholder}
                type="text"
                value={filterQuery}
                onChange={(event) => {
                  changeFilter(event.currentTarget.value);
                }}
                className="min-w-0 flex-1 bg-transparent text-base text-foreground outline-none placeholder:text-muted-foreground"
              />
              {filterQuery ? (
                <button
                  type="button"
                  aria-label={t("composer.modelPanel.filterClear")}
                  data-composer-model-panel-filter-clear={filterSection}
                  title={t("composer.modelPanel.filterClear")}
                  onClick={() => {
                    changeFilter("");
                  }}
                  className="inline-flex size-4 shrink-0 items-center justify-center rounded-control text-muted-foreground transition hover:bg-surface-soft hover:text-foreground"
                >
                  <XIcon size={11} aria-hidden="true" />
                </button>
              ) : null}
            </div>
          ) : null}
          {filterSection && filterEmpty ? (
            <p
              data-composer-model-panel-filter-empty={filterSection}
              className="px-2.5 py-3 text-base text-muted-foreground"
            >
              {filterEmptyLabel}
            </p>
          ) : (
            <div
              role="listbox"
              aria-labelledby={`${panelId}-heading`}
              className="flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto p-1.5"
            >
              {activeSection.options.map((option) => (
                <button
                  key={`${activeSection.id}\u0000${option.value}`}
                  type="button"
                  role="option"
                  aria-selected={option.selected}
                  data-composer-model-panel-option={activeSection.id}
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
                  <span className="truncate text-base">{option.label}</span>
                </button>
              ))}
            </div>
          )}
        </div>
      ) : (
        <div
          role="group"
          aria-labelledby={`${panelId}-heading`}
          className="flex max-h-[inherit] min-h-0 flex-col gap-0.5 overflow-y-auto p-1.5"
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
                data-composer-model-panel-row={section.id}
                data-composer-model-panel-row-pending={sectionPending ? "true" : undefined}
                aria-haspopup="listbox"
                aria-label={`${section.label}: ${section.value}`}
                title={sectionPending ? `${section.value} · ${reselectHint}` : section.value}
                onClick={() => {
                  onSelectSection(section.id);
                }}
                onKeyDown={(event) => {
                  if (event.key !== "ArrowRight") {
                    return;
                  }
                  event.preventDefault();
                  event.stopPropagation();
                  onSelectSection(section.id);
                }}
                className={cn(
                  "flex w-full cursor-pointer items-center gap-2 rounded-control px-2.5 py-2 text-left transition",
                  "text-muted-foreground hover:bg-surface-soft hover:text-foreground",
                )}
              >
                <span className="shrink-0 app-text-9 uppercase tracking-[0.12em] text-muted-foreground">
                  {section.label}
                </span>
                <span className="min-w-0 flex-1 truncate text-base text-foreground">
                  {section.value}
                </span>
                {sectionPending ? (
                  <span className="shrink-0 rounded-chip border border-amber-300/40 px-1.5 app-text-10 text-amber-200">
                    {t("composer.modelPanel.pendingConfirm")}
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
  );
}
