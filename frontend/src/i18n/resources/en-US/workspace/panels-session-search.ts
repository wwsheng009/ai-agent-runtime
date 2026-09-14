// P2-1A: workspace.panels.sessionSearch copy (session metadata search dialog).
// Keep keys aligned with zh-CN/workspace/panels-session-search.ts.
import type { DeepStringShape } from "../../shape";
import type { zhWorkspaceSessionSearch } from "../../zh-CN/workspace/panels-session-search";

export const enWorkspaceSessionSearch = {
  trigger: "Open session search",
  triggerHint: "Search server-side session metadata by user / tags / state",
  ariaLabel: "Session metadata search",
  eyebrow: "Runtime · Session search",
  title: "Session metadata search",
  description:
    "Search server-side session metadata by user, tags, and state; results are filtered server-side, not by local title matching.",
  close: "Close session search",
  form: {
    legend: "Filters",
    userId: "User",
    userIdAny: "Any user",
    tags: "Tags",
    tagsPlaceholder: "Comma separated, e.g. support,billing",
    tagsHint: "Multiple tags use AND semantics: a session must carry all of them.",
    state: "State",
    stateAny: "Any state",
    submit: "Search",
    searching: "Searching…",
    reset: "Clear filters",
  },
  state: {
    active: "Active",
    idle: "Idle",
    closed: "Closed",
    archived: "Archived",
    unknown: "Unknown state",
  },
  result: {
    title: "Results",
    count: "{{count}} session(s) matched",
    loading: "Searching sessions…",
    idle: "Set filters and press “Search”.",
    empty: "No matching sessions",
    emptyHint: "Try loosening the tag or state filters.",
    limitHint:
      "Reached the {{limit}} row page limit; narrow the filters and retry.",
    tags: "Tags: {{tags}}",
    updatedAt: "Updated {{time}}",
    timeUnknown: "Time unknown",
    open: "Open session {{title}}",
  },
  error: {
    title: "Session search failed",
    retry: "Retry",
    unavailable:
      "Server-side metadata search is unavailable (HTTP {{status}}); local title search still works.",
    unavailableUnknown:
      "Server-side metadata search is unavailable; local title search still works.",
  },
} satisfies DeepStringShape<typeof zhWorkspaceSessionSearch>;
