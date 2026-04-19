import { type ReactNode } from "react";

import { cn } from "@/lib/utils";

type SettingsToggleCardProps = {
  checked: boolean;
  description: ReactNode;
  title: ReactNode;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
  className?: string;
  contentClassName?: string;
  icon?: ReactNode;
  iconWrapperClassName?: string;
};

export function SettingsToggleCard({
  checked,
  description,
  title,
  onChange,
  disabled = false,
  className,
  contentClassName,
  icon,
  iconWrapperClassName,
}: SettingsToggleCardProps) {
  return (
    <label
      className={cn(
        "flex items-start gap-3 rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3.5 transition",
        disabled
          ? "cursor-not-allowed opacity-60"
          : "cursor-pointer hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
        className,
      )}
    >
      {icon ? (
        <span
          className={cn(
            "mt-0.5 inline-flex size-8 shrink-0 items-center justify-center rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-solid)] text-[var(--accent-primary)]",
            iconWrapperClassName,
          )}
        >
          {icon}
        </span>
      ) : null}
      <span className={cn("min-w-0 flex-1", contentClassName)}>
        <span className="flex items-center justify-between gap-3">
          <span className="text-sm font-semibold text-[var(--foreground)]">
            {title}
          </span>
          <input
            type="checkbox"
            checked={checked}
            disabled={disabled}
            onChange={(event) => onChange(event.target.checked)}
            className="h-4 w-4 shrink-0 accent-[var(--accent-primary)]"
          />
        </span>
        <span className="mt-2 block text-sm leading-6 text-[var(--muted-foreground)]">
          {description}
        </span>
      </span>
    </label>
  );
}
