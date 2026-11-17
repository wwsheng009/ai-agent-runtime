import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

import {
  fieldTextareaVariants,
  type FieldSurface,
  type FieldTextareaSize,
} from "./field-variants";

export type TextareaProps = ComponentProps<"textarea"> & {
  size?: FieldTextareaSize;
  surface?: FieldSurface;
};

export function Textarea({
  className,
  size = "md",
  surface = "default",
  ...props
}: TextareaProps) {
  return (
    <textarea
      className={cn(fieldTextareaVariants({ size, surface }), className)}
      {...props}
    />
  );
}
