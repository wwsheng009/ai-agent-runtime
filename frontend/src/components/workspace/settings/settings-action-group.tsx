import { type ReactNode } from "react";

import { Button, type ButtonProps } from "@/components/ui/button";
import { cn } from "@/lib/utils";

type SettingsActionGroupProps = {
  children: ReactNode;
  className?: string;
  compact?: boolean;
};

type SettingsActionButtonProps = Omit<ButtonProps, "children" | "size"> & {
  icon?: ReactNode;
  label: ReactNode;
  size?: ButtonProps["size"];
};

type SettingsIconActionButtonProps = Omit<
  ButtonProps,
  "children" | "size" | "variant" | "title" | "aria-label"
> & {
  children: ReactNode;
  className?: string;
  label: string;
};

export function SettingsActionGroup({
  children,
  className,
  compact = false,
}: SettingsActionGroupProps) {
  return (
    <div
      className={cn(
        compact
          ? "flex flex-nowrap justify-end gap-1 whitespace-nowrap"
          : "flex flex-wrap justify-end gap-2",
        className,
      )}
    >
      {children}
    </div>
  );
}

export function SettingsActionButton({
  icon,
  label,
  size = "sm",
  ...props
}: SettingsActionButtonProps) {
  return (
    <Button size={size} {...props}>
      {icon}
      {label}
    </Button>
  );
}

export function SettingsIconActionButton({
  children,
  className,
  label,
  ...props
}: SettingsIconActionButtonProps) {
  return (
    <Button
      variant="ghost"
      size="icon"
      className={cn("size-7 rounded-[0.5rem] p-0", className)}
      title={label}
      aria-label={label}
      {...props}
    >
      {children}
    </Button>
  );
}
