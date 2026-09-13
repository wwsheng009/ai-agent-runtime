import type { ComponentProps } from "react";

import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

// P0-3：设置域「表面」的唯一真源。frame/surface/radius/density/state 五个维度覆盖了
// 原先散落在 settings-* 组件里手写的 border/bg/rounded/padding/交互态组合。
// 消费方通过 className 透传覆盖（cn/twMerge 后出现的类胜出），保持既有调用方行为不变。
// 非 div 容器（button/label/section）直接调用 surfaceCardVariants(...) 取类名。
// eslint-disable-next-line react-refresh/only-export-components
export const surfaceCardVariants = cva("", {
  variants: {
    frame: {
      none: "",
      outlined: "border border-border",
      dashed: "border border-dashed border-border",
    },
    surface: {
      none: "",
      solid: "bg-surface-solid",
      softer: "bg-surface-softer",
      panel: "surface-panel",
      accent:
        "border-accent-primary-border bg-accent-primary-soft shadow-[0_0_0_1px_var(--accent-primary-border)]",
      warning: "border-accent-orange/20 bg-accent-orange/8",
      "warning-soft": "border-accent-orange/24 bg-accent-orange/10",
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
      roomy: "px-3 py-3",
      wide: "px-3 py-6",
    },
    state: {
      none: "",
      disabled: "cursor-not-allowed opacity-60",
      hoverable:
        "hover:border-border-strong hover:bg-surface-soft",
    },
  },
  defaultVariants: {
    frame: "outlined",
    surface: "softer",
    radius: "xl",
    density: "default",
  },
});

export type SurfaceCardVariantProps = VariantProps<typeof surfaceCardVariants>;

export type SurfaceCardProps = ComponentProps<"div"> & SurfaceCardVariantProps;

export function SurfaceCard({
  className,
  frame,
  surface,
  radius,
  density,
  state,
  ...props
}: SurfaceCardProps) {
  return (
    <div
      className={cn(
        surfaceCardVariants({ frame, surface, radius, density, state }),
        className,
      )}
      {...props}
    />
  );
}
