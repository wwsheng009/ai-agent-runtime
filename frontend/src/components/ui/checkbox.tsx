import type { ComponentProps } from "react";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

const checkboxInputVariants = cva("", {
  variants: {
    size: {
      xs: "size-3",
      sm: "size-3.5",
      md: "size-4",
      lg: "size-5",
    },
    tone: {
      primary: "accent-accent-primary",
      secondary: "accent-accent-secondary",
      gold: "accent-accent-gold",
      teal: "accent-accent-teal",
      current: "accent-current",
    },
  },
  defaultVariants: {
    size: "md",
    tone: "primary",
  },
});

export type CheckboxInputProps = Omit<ComponentProps<"input">, "type" | "size"> &
  VariantProps<typeof checkboxInputVariants> & {
  shrink?: boolean;
};

export type CheckboxInputSize = NonNullable<
  VariantProps<typeof checkboxInputVariants>["size"]
>;
export type CheckboxInputTone = NonNullable<
  VariantProps<typeof checkboxInputVariants>["tone"]
>;

// P0-3：设置域复选框输入的唯一实现（默认 size-4 + accent-primary，可选 shrink-0）。
export function CheckboxInput({
  className,
  size = "md",
  shrink = false,
  tone = "primary",
  ...props
}: CheckboxInputProps) {
  return (
    <input
      type="checkbox"
      className={cn(
        checkboxInputVariants({ size, tone }),
        shrink ? "shrink-0" : null,
        className,
      )}
      {...props}
    />
  );
}
