import { type ReactNode } from "react";

import { cn } from "@/lib/utils";

type PanelIconProps = {
  children: ReactNode;
  className?: string;
};

// P0-3：设置域图标徽标的唯一实现（原 settings-panel-icon 与其在 toggle-card /
// workspace-settings-page 中的内联副本统一到此）。
export function PanelIcon({ children, className }: PanelIconProps) {
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
