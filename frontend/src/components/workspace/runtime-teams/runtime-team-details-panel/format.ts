import type { TeamDetailsSectionState } from "./types";

export const detailControlClass =
  "w-full rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2 text-sm text-[var(--foreground)] outline-none";

export const detailCardClass =
  "rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-soft)] px-3 py-2.5";

export const detailStatusPillClass =
  "rounded-[0.65rem] border px-2 py-0.5 app-text-10 uppercase tracking-[0.12em]";

export const detailMetaPillClass =
  "rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-solid)] px-2 py-0.5 app-text-10 uppercase tracking-[0.12em] text-[var(--muted-foreground)]";

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
