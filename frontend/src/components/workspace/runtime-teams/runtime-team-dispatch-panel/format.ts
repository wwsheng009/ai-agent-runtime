import { fieldControlVariants } from "@/components/ui/field-variants";

export const dispatchControlClass = fieldControlVariants({
  size: "md",
  surface: "dark",
});

export const dispatchStatusPillClass =
  "rounded-control border px-2 py-0.5 app-text-10 uppercase tracking-[0.12em]";

export const dispatchSummaryPillClass =
  "rounded-control border px-2.5 py-1 app-text-11 uppercase tracking-[0.12em]";

export const compactMarkdownClass =
  "app-text-12 [&>p]:my-0 [&>ul]:my-2 [&>ol]:my-2 [&>blockquote]:my-2 [&>div]:my-2";
