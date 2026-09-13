import { cva } from "class-variance-authority";

export const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 rounded-[0.7rem] border text-base font-medium transition-colors outline-none disabled:pointer-events-none disabled:opacity-50 focus-visible:ring-2 focus-visible:ring-ring",
  {
    variants: {
      variant: {
        primary:
          "border-transparent bg-[linear-gradient(135deg,var(--accent-primary)_0%,var(--accent-primary-strong)_100%)] text-black shadow-[0_6px_20px_var(--accent-primary-shadow)] hover:brightness-105",
        secondary:
          "border-border bg-surface-soft text-foreground hover:border-border-strong hover:bg-surface-soft-hover",
        ghost:
          "border-transparent bg-transparent text-muted-foreground hover:bg-surface-soft hover:text-foreground",
        destructive:
          "border-[#f59e7d]/30 bg-[#f59e7d]/12 text-[#f59e7d] hover:bg-[#f59e7d]/20",
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
