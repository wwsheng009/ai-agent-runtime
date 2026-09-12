import type { ComponentProps } from "react";

import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

// P0-3：设置域「表面」的唯一真源。frame/surface/radius/density 四个维度覆盖了
// 原先散落在 19 个 settings-* 组件里手写的 border/bg/rounded/padding 组合。
// 消费方通过 className 透传覆盖（cn/twMerge 后出现的类胜出），保持既有调用方行为不变。
// eslint-disable-next-line react-refresh/only-export-components
export const surfaceCardVariants = cva("", {
  variants: {
    frame: {
      none: "",
      outlined: "border border-[var(--border)]",
      dashed: "border border-dashed border-[var(--border)]",
    },
    surface: {
      none: "",
      solid: "bg-[var(--surface-solid)]",
      softer: "bg-[var(--surface-softer)]",
    },
    radius: {
      none: "",
      sm: "rounded-[0.75rem]",
      md: "rounded-[0.8rem]",
      lg: "rounded-[0.85rem]",
      xl: "rounded-[0.9rem]",
      panel: "rounded-[1rem]",
    },
    density: {
      none: "",
      snug: "p-3",
      default: "p-3.5",
      panel: "p-4",
      compact: "px-3 py-2.5",
      tight: "px-3 py-2",
    },
  },
  defaultVariants: {
    frame: "outlined",
    surface: "softer",
    radius: "xl",
    density: "default",
  },
});

export type SurfaceCardProps = ComponentProps<"div"> &
  VariantProps<typeof surfaceCardVariants>;

export function SurfaceCard({
  className,
  frame,
  surface,
  radius,
  density,
  ...props
}: SurfaceCardProps) {
  return (
    <div
      className={cn(
        surfaceCardVariants({ frame, surface, radius, density }),
        className,
      )}
      {...props}
    />
  );
}
