import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

import {
  fieldControlVariants,
  type FieldSize,
  type FieldSurface,
} from "./field-variants";

export type InputProps = Omit<ComponentProps<"input">, "size"> & {
  size?: FieldSize;
  surface?: FieldSurface;
};

export function Input({
  className,
  size = "md",
  surface = "default",
  ...props
}: InputProps) {
  return (
    <input
      className={cn(fieldControlVariants({ size, surface }), className)}
      {...props}
    />
  );
}
