// P3：weekly 规则的星期多选（1..7，ISO 周序）。
// 由 components/workspace/schedule-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";

export type WeekdayPickerProps = {
  describedBy?: string;
  invalid?: boolean;
  selected: number[];
  onToggle: (day: number) => void;
};

export function WeekdayPicker({
  describedBy,
  invalid = false,
  onToggle,
  selected,
}: WeekdayPickerProps) {
  const { t } = useTranslation("workspace");
  return (
    <div
      aria-label={t("panels.schedule.weekdayLabel")}
      aria-describedby={describedBy}
      aria-invalid={invalid || undefined}
      className="flex flex-wrap items-center gap-1.5"
      data-testid="schedule-weekdays"
      role="group"
    >
      {([1, 2, 3, 4, 5, 6, 7] as const).map((day) => (
        <button
          aria-label={`${t("panels.schedule.weekdayLabel")} · ${t(
            `panels.schedule.weekday.${day}`,
          )}`}
          aria-pressed={selected.includes(day)}
          className={cn(
            "min-w-8 rounded-field border px-2.5 py-1.5 text-xs font-medium transition focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            selected.includes(day)
              ? "border-accent-primary-border bg-accent-primary-soft text-accent-primary"
              : "border-border bg-surface-solid text-muted-foreground hover:border-border-strong hover:text-foreground",
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
