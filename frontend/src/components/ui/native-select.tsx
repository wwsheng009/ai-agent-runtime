import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

import {
  fieldControlVariants,
  type FieldSize,
  type FieldSurface,
} from "./field-variants";

export type NativeSelectProps = Omit<ComponentProps<"select">, "size"> & {
  size?: FieldSize;
  surface?: FieldSurface;
};

export function NativeSelect({
  className,
  size = "md",
  surface = "default",
  ...props
}: NativeSelectProps) {
  return (
    <select
      className={cn(fieldControlVariants({ size, surface }), className)}
      {...props}
    />
  );
}
