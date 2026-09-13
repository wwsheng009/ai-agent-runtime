import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

export type CheckboxInputProps = Omit<ComponentProps<"input">, "type"> & {
  shrink?: boolean;
};

// P0-3：设置域复选框输入的唯一实现（h-4 w-4 + accent 变量，可选 shrink-0）。
export function CheckboxInput({
  className,
  shrink = false,
  ...props
}: CheckboxInputProps) {
  return (
    <input
      type="checkbox"
      className={cn(
        "h-4 w-4 accent-accent-primary",
        shrink ? "shrink-0" : null,
        className,
      )}
      {...props}
    />
  );
}
