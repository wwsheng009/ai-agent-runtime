import type { TeamDetailsSectionState } from "./types";

import { fieldControlVariants } from "@/components/ui/field-variants";

export const detailControlClass = fieldControlVariants({ size: "md" });

export const detailCardClass =
  "rounded-card border border-border bg-surface-soft px-3 py-2.5";

export const detailStatusPillClass =
  "rounded-control border px-2 py-0.5 app-text-10 uppercase tracking-[0.12em]";

export const detailMetaPillClass =
  "rounded-control border border-border bg-surface-solid px-2 py-0.5 app-text-10 uppercase tracking-[0.12em] text-muted-foreground";

export function createInitialOpenSections(): TeamDetailsSectionState {
  return {
    roster: true,
    tasks: true,
    mailbox: false,
    claims: false,
    timeline: false,
    summary: true,
  };
}
