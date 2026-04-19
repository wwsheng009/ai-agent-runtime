import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

export function Card({ className, ...props }: ComponentProps<"section">) {
  return (
    <section
      className={cn("surface-panel rounded-[1rem] p-4", className)}
      {...props}
    />
  );
}

export function CardTitle({
  className,
  ...props
}: ComponentProps<"h3">) {
  return (
    <h3
      className={cn(
        "text-lg font-semibold tracking-[-0.02em] text-[var(--foreground)]",
        className,
      )}
      {...props}
    />
  );
}

export function CardDescription({
  className,
  ...props
}: ComponentProps<"p">) {
  return (
    <p
      className={cn("text-sm leading-6 text-[var(--muted-foreground)]", className)}
      {...props}
    />
  );
}
