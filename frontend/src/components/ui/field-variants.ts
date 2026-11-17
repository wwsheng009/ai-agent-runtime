import { cva, type VariantProps } from "class-variance-authority";

const fieldBaseClassName = [
  "w-full",
  "rounded-field",
  "text-foreground",
  "outline-none",
  "transition",
  "placeholder:text-muted-foreground",
  "focus-visible:border-accent-primary-border",
  "focus-visible:ring-2",
  "focus-visible:ring-ring",
  "disabled:cursor-not-allowed",
  "disabled:opacity-60",
  "aria-invalid:border-accent-orange",
].join(" ");

const fieldSurfaceVariants = {
  default: "border border-border bg-surface-solid hover:border-border-strong",
  soft: "border border-border bg-surface-soft hover:border-border-strong",
  dark: "border border-white/8 bg-black/20 placeholder:text-white/28 hover:border-white/14",
  transparent: "border border-border bg-transparent hover:border-border-strong",
};

/** 单行字段统一高度轴：与 Button 的 sm/md/lg 对齐。 */
export const fieldControlVariants = cva(fieldBaseClassName, {
  variants: {
    size: {
      sm: "h-8 px-2.5 py-1.5 text-xs",
      md: "h-9 px-3 py-1.5 text-sm",
      lg: "h-10 px-3.5 py-2 text-sm",
    },
    surface: fieldSurfaceVariants,
  },
  defaultVariants: {
    size: "md",
    surface: "default",
  },
});

/** 多行字段不使用固定高度，由 min-height 档位表达密度。 */
export const fieldTextareaVariants = cva(`${fieldBaseClassName} resize-y`, {
  variants: {
    size: {
      sm: "min-h-24 px-2.5 py-1.5 text-xs",
      md: "min-h-28 px-3 py-2 text-sm",
      lg: "min-h-32 px-3.5 py-2.5 text-sm",
    },
    surface: fieldSurfaceVariants,
  },
  defaultVariants: {
    size: "md",
    surface: "default",
  },
});

export type FieldControlVariantProps = VariantProps<typeof fieldControlVariants>;
export type FieldTextareaVariantProps = VariantProps<typeof fieldTextareaVariants>;
export type FieldSize = NonNullable<FieldControlVariantProps["size"]>;
export type FieldSurface = NonNullable<FieldControlVariantProps["surface"]>;
export type FieldTextareaSize = NonNullable<FieldTextareaVariantProps["size"]>;
