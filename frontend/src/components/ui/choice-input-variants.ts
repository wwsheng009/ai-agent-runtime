import { cva, type VariantProps } from "class-variance-authority";

export const choiceInputVariants = cva("", {
  variants: {
    size: {
      xs: "size-3",
      sm: "size-3.5",
      md: "size-4",
      lg: "size-5",
    },
    tone: {
      primary: "accent-accent-primary",
      secondary: "accent-accent-secondary",
      gold: "accent-accent-gold",
      teal: "accent-accent-teal",
      current: "accent-current",
    },
  },
  defaultVariants: {
    size: "md",
    tone: "primary",
  },
});

export type ChoiceInputSize = NonNullable<
  VariantProps<typeof choiceInputVariants>["size"]
>;
export type ChoiceInputTone = NonNullable<
  VariantProps<typeof choiceInputVariants>["tone"]
>;