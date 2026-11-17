import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

import {
  choiceInputVariants,
  type ChoiceInputSize,
  type ChoiceInputTone,
} from "./choice-input-variants";

export type CheckboxInputProps = Omit<ComponentProps<"input">, "type" | "size"> &
  {
    size?: ChoiceInputSize;
    tone?: ChoiceInputTone;
    shrink?: boolean;
  };

export type CheckboxInputSize = ChoiceInputSize;
export type CheckboxInputTone = ChoiceInputTone;

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
        choiceInputVariants({ size, tone }),
        shrink ? "shrink-0" : null,
        className,
      )}
      {...props}
    />
  );
}
