import {
  fieldControlVariants,
  fieldTextareaVariants,
} from "@/components/ui/field-variants";

export const editorControlClassName = fieldControlVariants({ size: "md" });

export const editorTextareaClassName = fieldTextareaVariants({
  size: "md",
});

export const editorToggleRowClassName =
  "flex items-center justify-between gap-3 rounded-field border border-border bg-surface-solid px-3 py-2.5 text-sm text-foreground transition focus-within:border-accent-primary-border focus-within:ring-2 focus-within:ring-ring";

export const editorSectionToggleClassName =
  "inline-flex items-center gap-2 rounded-field border border-border bg-surface-solid px-2.5 py-1.5 text-sm text-foreground transition focus-within:border-accent-primary-border focus-within:ring-2 focus-within:ring-ring";
