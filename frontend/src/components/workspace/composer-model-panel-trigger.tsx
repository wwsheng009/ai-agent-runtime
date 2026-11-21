// composer 模型面板的触发器（从 composer-model-panel.tsx 抽出，P0-2 行数约束）。
// 开关 / 定位 / 焦点归还仍留在面板容器；这里只保留按钮结构与摘要展示。
import type {
  KeyboardEvent as ReactKeyboardEvent,
  MouseEvent as ReactMouseEvent,
  PointerEvent as ReactPointerEvent,
  RefObject,
} from "react";
import { ChevronDownIcon, SlidersHorizontalIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";

export type ComposerModelPanelTriggerProps = {
  disabled: boolean;
  disabledReason: string | null;
  hasPending: boolean;
  modelLabel: string;
  onClick: (event: ReactMouseEvent<HTMLButtonElement>) => void;
  onKeyDown: (event: ReactKeyboardEvent<HTMLButtonElement>) => void;
  onPointerDown: (event: ReactPointerEvent<HTMLButtonElement>) => void;
  open: boolean;
  panelId: string;
  providerLabel: string;
  reasoningSummaryLabel: string;
  reselectHint: string;
  triggerLabel: string;
  triggerRef: RefObject<HTMLButtonElement | null>;
};

export function ComposerModelPanelTrigger({
  disabled,
  disabledReason,
  hasPending,
  modelLabel,
  onClick,
  onKeyDown,
  onPointerDown,
  open,
  panelId,
  providerLabel,
  reasoningSummaryLabel,
  reselectHint,
  triggerLabel,
  triggerRef,
}: ComposerModelPanelTriggerProps) {
  const { t } = useTranslation("workspace");

  return (
    <button
      ref={triggerRef}
      type="button"
      aria-label={`${t("composer.modelPanel.trigger")}: ${triggerLabel}`}
      aria-haspopup="dialog"
      aria-expanded={open}
      aria-controls={open ? panelId : undefined}
      data-composer-model-panel-trigger
      disabled={disabled}
      title={
        disabled && disabledReason
          ? disabledReason
          : hasPending
            ? `${triggerLabel} · ${reselectHint}`
            : triggerLabel
      }
      onClick={onClick}
      onPointerDown={onPointerDown}
      onKeyDown={onKeyDown}
      className={cn(
        "inline-flex h-8 min-h-8 min-w-0 w-full max-w-none items-center gap-1.5 rounded-field border border-border",
        "bg-surface-soft px-2 py-0 text-left text-base leading-none text-muted-foreground outline-none transition",
        "normal-case tracking-normal",
        "hover:border-border-strong hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring",
        "active:scale-[0.98] active:border-border-strong active:bg-surface-soft-hover",
        "disabled:cursor-not-allowed disabled:opacity-60",
      )}
      data-composer-model-panel-open={open ? "true" : undefined}
    >
      <SlidersHorizontalIcon size={13} aria-hidden="true" className="shrink-0" />
      <span className="shrink-0 text-muted-foreground app-text-9 uppercase tracking-[0.08em]">
        {t("composer.model")}
      </span>
      <span
        className="flex min-w-0 flex-1 items-center gap-1 overflow-hidden whitespace-nowrap"
        data-composer-model-panel-summary
      >
        {modelLabel ? (
          <span
            className="min-w-0 flex-[1_1_auto] truncate"
            data-composer-model-value
            title={modelLabel}
          >
            {modelLabel}
          </span>
        ) : null}
        {modelLabel && reasoningSummaryLabel ? (
          <span aria-hidden="true" className="shrink-0">
            {" · "}
          </span>
        ) : null}
        {reasoningSummaryLabel ? (
          <span
            className="shrink-0"
            data-composer-model-reasoning
            title={reasoningSummaryLabel}
          >
            {reasoningSummaryLabel}
          </span>
        ) : null}
        {providerLabel ? (
          <>
            {modelLabel || reasoningSummaryLabel ? (
              <span aria-hidden="true" className="hidden shrink-0 2xl:inline">
                {" · "}
              </span>
            ) : null}
            <span
              className={cn(
                "min-w-0 max-w-[8rem] shrink-[2] truncate text-muted-foreground",
                modelLabel ? "hidden 2xl:inline" : "inline",
              )}
              data-composer-model-provider
              title={providerLabel}
            >
              {providerLabel}
            </span>
          </>
        ) : null}
      </span>
      {/* 面板关着也要能看出「换了供应商、还欠一次确认」。 */}
      {hasPending ? (
        <span
          data-composer-model-panel-pending
          aria-hidden="true"
          className="size-1.5 shrink-0 rounded-full bg-amber-300/70"
        />
      ) : null}
      <ChevronDownIcon
        size={12}
        aria-hidden="true"
        className={cn(
          "shrink-0 transition-transform duration-150",
          open ? "rotate-180" : "rotate-0",
        )}
      />
    </button>
  );
}