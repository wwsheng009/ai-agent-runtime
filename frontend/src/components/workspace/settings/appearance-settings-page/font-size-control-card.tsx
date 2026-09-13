// 由 components/workspace/settings/appearance-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { MinusIcon, PlusIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { FONT_SIZE_LIMITS, formatFontSizePx } from "@/core/settings";
import { cn } from "@/lib/utils";

import { editorControlClassName } from "../editor-control-class";
import { SettingsPanelCard } from "../settings-panel-card";

type FontSizeControlCardProps = {
  defaultValue: number;
  description: string;
  title: string;
  value: number;
  onChange: (nextValue: number) => void;
};

function clampFontSizeValue(value: number) {
  return Math.min(
    FONT_SIZE_LIMITS.max,
    Math.max(FONT_SIZE_LIMITS.min, Math.round(value)),
  );
}

export function FontSizeControlCard({
  defaultValue,
  description,
  title,
  value,
  onChange,
}: FontSizeControlCardProps) {
  const { t } = useTranslation("settings");
  const { t: tCommon } = useTranslation("common");
  const decrementDisabled = value <= FONT_SIZE_LIMITS.min;
  const incrementDisabled = value >= FONT_SIZE_LIMITS.max;

  function updateValue(nextValue: number) {
    onChange(clampFontSizeValue(nextValue));
  }

  return (
    <SettingsPanelCard
      title={<span className="text-base">{title}</span>}
      description={description}
      descriptionClassName="text-base"
      headerAside={
        <div className="rounded-control border border-border bg-black/10 px-2 py-0.5 font-mono app-text-11 text-foreground">
          {formatFontSizePx(value)}
        </div>
      }
      bodyClassName="grid gap-3"
    >
      <div className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2">
        <button
          type="button"
          disabled={decrementDisabled}
          onClick={() => updateValue(value - FONT_SIZE_LIMITS.step)}
          className={cn(
            "inline-flex h-9 w-9 items-center justify-center rounded-field border transition",
            decrementDisabled
              ? "cursor-not-allowed border-border bg-surface-solid text-muted-foreground opacity-50"
              : "border-border bg-surface-solid text-foreground hover:border-border-strong hover:bg-surface-soft",
          )}
        >
          <MinusIcon size={16} />
        </button>
        <input
          type="range"
          min={FONT_SIZE_LIMITS.min}
          max={FONT_SIZE_LIMITS.max}
          step={FONT_SIZE_LIMITS.step}
          value={value}
          onChange={(event) => updateValue(Number(event.target.value))}
          className="h-2 w-full accent-accent-primary"
        />
        <button
          type="button"
          disabled={incrementDisabled}
          onClick={() => updateValue(value + FONT_SIZE_LIMITS.step)}
          className={cn(
            "inline-flex h-9 w-9 items-center justify-center rounded-field border transition",
            incrementDisabled
              ? "cursor-not-allowed border-border bg-surface-solid text-muted-foreground opacity-50"
              : "border-border bg-surface-solid text-foreground hover:border-border-strong hover:bg-surface-soft",
          )}
        >
          <PlusIcon size={16} />
        </button>
      </div>

      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end">
        <label className="block">
          <span className="text-xs uppercase tracking-[0.16em] text-muted-foreground">
            {t("appearance.customPixels")}
          </span>
          <span className="mt-2 flex items-center gap-3">
            <input
              type="number"
              min={FONT_SIZE_LIMITS.min}
              max={FONT_SIZE_LIMITS.max}
              step={FONT_SIZE_LIMITS.step}
              value={value}
              onChange={(event) => {
                const nextValue = Number(event.target.value);
                if (Number.isFinite(nextValue)) {
                  updateValue(nextValue);
                }
              }}
              className={editorControlClassName}
            />
            <span className="shrink-0 text-base text-muted-foreground">
              px
            </span>
          </span>
        </label>

        <button
          type="button"
          onClick={() => updateValue(defaultValue)}
          disabled={value === defaultValue}
          className={cn(
            "rounded-field border px-3 py-2 text-base transition",
            value === defaultValue
              ? "cursor-not-allowed border-border bg-surface-solid text-muted-foreground opacity-50"
              : "border-border bg-surface-solid text-foreground hover:border-border-strong hover:bg-surface-soft",
          )}
        >
          {tCommon("actions.reset")} {formatFontSizePx(defaultValue)}
        </button>
      </div>

      <div className="text-xs leading-5 text-muted-foreground">
        {t("appearance.sizeHint", {
          min: String(FONT_SIZE_LIMITS.min),
          max: String(FONT_SIZE_LIMITS.max),
        })}
      </div>
    </SettingsPanelCard>
  );
}
