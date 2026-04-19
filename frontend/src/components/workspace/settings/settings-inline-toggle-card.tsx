import { type ReactNode } from "react";

import { cn } from "@/lib/utils";

type SettingsInlineToggleCardProps = {
  checked: boolean;
  label: ReactNode;
  onCheckedChange: (checked: boolean) => void;
  description?: ReactNode;
  className?: string;
  labelClassName?: string;
};

export function SettingsInlineToggleCard({
  checked,
  label,
  onCheckedChange,
  description,
  className,
  labelClassName,
}: SettingsInlineToggleCardProps) {
  return (
    <div
      className={cn(
        "rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2.5",
        className,
      )}
    >
      <label
        className={cn(
          "flex items-center justify-between gap-4 text-sm text-[var(--foreground)]",
          labelClassName,
        )}
      >
        <div>
          <div className="font-medium">{label}</div>
          {description ? (
            <div className="mt-1 text-xs text-[var(--muted-foreground)]">
              {description}
            </div>
          ) : null}
        </div>
        <input
          type="checkbox"
          className="h-4 w-4 shrink-0 accent-[var(--accent-primary)]"
          checked={checked}
          onChange={(event) => onCheckedChange(event.target.checked)}
        />
      </label>
    </div>
  );
}
