// P3：weekly 规则的星期多选（1..7，ISO 周序）。
// 由 components/workspace/schedule-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";

export type WeekdayPickerProps = {
  selected: number[];
  onToggle: (day: number) => void;
};

export function WeekdayPicker({ onToggle, selected }: WeekdayPickerProps) {
  const { t } = useTranslation("workspace");
  return (
    <div className="flex flex-wrap items-center gap-1">
      {([1, 2, 3, 4, 5, 6, 7] as const).map((day) => (
        <button
          aria-pressed={selected.includes(day)}
          className={cn(
            "rounded border border-border px-1.5 py-0.5 text-xs",
            selected.includes(day) && "border-accent-primary-border text-accent-primary",
          )}
          key={day}
          onClick={() => onToggle(day)}
          type="button"
        >
          {t(`panels.schedule.weekday.${day}`)}
        </button>
      ))}
    </div>
  );
}
