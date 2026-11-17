import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

import {
  choiceInputVariants,
  type ChoiceInputSize,
  type ChoiceInputTone,
} from "./choice-input-variants";

export type RadioInputProps = Omit<ComponentProps<"input">, "type" | "size"> & {
  size?: ChoiceInputSize;
  tone?: ChoiceInputTone;
  shrink?: boolean;
};

export type RadioInputSize = ChoiceInputSize;
export type RadioInputTone = ChoiceInputTone;

export function RadioInput({
  className,
  size = "md",
  shrink = false,
  tone = "primary",
  ...props
}: RadioInputProps) {
  return (
    <input
      type="radio"
      className={cn(
        choiceInputVariants({ size, tone }),
        shrink ? "shrink-0" : null,
        className,
      )}
      {...props}
    />
  );
}