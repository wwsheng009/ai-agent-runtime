import { type ReactNode } from "react";

import { surfaceCardVariants } from "@/components/ui/surface-card";
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
        "text-left transition",
        surfaceCardVariants({ surface: active ? "accent" : "softer" }),
        active ? null : surfaceCardVariants({ state: "hoverable" }),
        disabled ? surfaceCardVariants({ state: "disabled" }) : null,
        className,
      )}
    >
      {children}
    </button>
  );
}
