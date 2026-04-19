import { type ReactNode } from "react";

import { cn } from "@/lib/utils";

type SettingsPanelIconProps = {
  children: ReactNode;
  className?: string;
};

export function SettingsPanelIcon({
  children,
  className,
}: SettingsPanelIconProps) {
  return (
    <span
      className={cn(
        "inline-flex size-8 items-center justify-center rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-solid)] text-[var(--accent-primary)]",
        className,
      )}
    >
      {children}
    </span>
  );
}
