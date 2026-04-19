import { type ReactNode } from "react";

import { cn } from "@/lib/utils";

type SettingsChoiceCardProps = {
  active: boolean;
  children: ReactNode;
  onClick: () => void;
  disabled?: boolean;
  className?: string;
};

export function SettingsChoiceCard({
  active,
  children,
  onClick,
  disabled = false,
  className,
}: SettingsChoiceCardProps) {
  return (
    <button
      type="button"
      aria-pressed={active}
      disabled={disabled}
      onClick={onClick}
      className={cn(
        "rounded-[0.9rem] border p-3.5 text-left transition",
        active
          ? "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)] shadow-[0_0_0_1px_var(--accent-primary-border)]"
          : "border-[var(--border)] bg-[var(--surface-softer)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
        disabled ? "cursor-not-allowed opacity-60" : null,
        className,
      )}
    >
      {children}
    </button>
  );
}
