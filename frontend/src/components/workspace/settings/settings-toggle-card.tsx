import { type ReactNode } from "react";

import { CheckboxInput } from "@/components/ui/checkbox";
import { PanelIcon } from "@/components/ui/panel-icon";
import { surfaceCardVariants } from "@/components/ui/surface-card";
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
        "flex items-start gap-3 transition",
        surfaceCardVariants({
          surface: "softer",
          state: disabled ? "disabled" : "hoverable",
        }),
        disabled ? null : "cursor-pointer",
        className,
      )}
    >
      {icon ? (
        <PanelIcon className={cn("mt-0.5 shrink-0", iconWrapperClassName)}>
          {icon}
        </PanelIcon>
      ) : null}
      <span className={cn("min-w-0 flex-1", contentClassName)}>
        <span className="flex items-center justify-between gap-3">
          <span className="text-sm font-semibold text-foreground">
            {title}
          </span>
          <CheckboxInput
            shrink
            checked={checked}
            disabled={disabled}
            onChange={(event) => onChange(event.target.checked)}
          />
        </span>
        <span className="mt-2 block text-sm leading-6 text-muted-foreground">
          {description}
        </span>
      </span>
    </label>
  );
}
