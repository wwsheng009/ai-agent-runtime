// 由 components/workspace/runtime-teams/dispatch-console.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { fieldControlVariants } from "@/components/ui/field-variants";

export const consolePanelClass =
  "rounded-card border border-white/8 bg-white/3 px-3 py-2.5";

export const consoleInputClass = fieldControlVariants({
  size: "md",
  surface: "dark",
});

export const consolePillClass =
  "rounded-control border px-2 py-0.5 app-text-10 uppercase tracking-[0.12em]";

export const consoleModeButtonClass =
  "rounded-control border px-2.5 py-1.5 text-base uppercase tracking-[0.12em] transition";
