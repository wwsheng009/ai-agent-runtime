import { type ReactNode } from "react";

import { CheckboxInput } from "@/components/ui/checkbox";
import { surfaceCardVariants } from "@/components/ui/surface-card";
import { cn } from "@/lib/utils";

type SettingsInlineToggleCardProps = {
  checked: boolean;
  label: ReactNode;
  onCheckedChange: (checked: boolean) => void;
  description?: ReactNode;
  className?: string;
  labelClassName?: string;
  disabled?: boolean;
};

export function SettingsInlineToggleCard({
  checked,
  label,
  onCheckedChange,
  description,
  className,
  labelClassName,
  disabled = false,
}: SettingsInlineToggleCardProps) {
  return (
    <div
      className={cn(
        surfaceCardVariants({
          surface: "solid",
          radius: "sm",
          density: "compact",
          state: disabled ? "disabled" : "none",
        }),
        className,
      )}
    >
      <label
        className={cn(
          "flex items-center justify-between gap-4 text-sm text-foreground",
          labelClassName,
        )}
      >
        <div>
          <div className="font-medium">{label}</div>
          {description ? (
            <div className="mt-1 text-xs text-muted-foreground">
              {description}
            </div>
          ) : null}
        </div>
        <CheckboxInput
          shrink
          checked={checked}
          disabled={disabled}
          onChange={(event) => onCheckedChange(event.target.checked)}
        />
      </label>
    </div>
  );
}
