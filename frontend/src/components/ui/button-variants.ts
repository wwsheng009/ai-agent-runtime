import { cva } from "class-variance-authority";

export const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 rounded-[0.7rem] border text-base font-medium transition-colors outline-none disabled:pointer-events-none disabled:opacity-50 focus-visible:ring-2 focus-visible:ring-[var(--ring)]",
  {
    variants: {
      variant: {
        primary:
          "border-transparent bg-[linear-gradient(135deg,var(--accent-primary)_0%,var(--accent-primary-strong)_100%)] text-black shadow-[0_6px_20px_var(--accent-primary-shadow)] hover:brightness-105",
        secondary:
          "border-[var(--border)] bg-[var(--surface-soft)] text-[var(--foreground)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft-hover)]",
        ghost:
          "border-transparent bg-transparent text-[var(--muted-foreground)] hover:bg-[var(--surface-soft)] hover:text-[var(--foreground)]",
      },
      size: {
        sm: "h-8 px-3",
        md: "h-9 px-3.5",
        lg: "h-10 px-4",
        icon: "size-8 rounded-[0.7rem]",
      },
    },
    defaultVariants: {
      variant: "primary",
      size: "md",
    },
  },
);
