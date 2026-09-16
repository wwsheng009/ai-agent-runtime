export const enWorkspacePanelsSessionDetail = {
  ariaLabel: "Session detail panel",
  title: "Session details",
  untitled: "Untitled session",
  refresh: "Refresh session details",
  retry: "Retry",
  loading: "Loading session details…",
  errorTitle: "Failed to load session details",
  empty: "No session selected",
  emptyHint:
    "Select a session to see its state, workspace directory, and runtime info.",
  states: {
    active: "Active",
    idle: "Idle",
    closed: "Closed",
    archived: "Archived",
    unknown: "Unknown state",
  },
  relation: {
    label: "Runtime link",
    transportLabel: "Transport",
    states: {
      attached: "Attached runtime session",
      restored: "Restored runtime session",
      error: "Session sync error",
      pending: "No runtime session attached yet",
    },
    details: {
      attached:
        "Attached to a live runtime session from the active workspace flow.",
      restored: "Recovered from runtime session history and ready to continue.",
      error:
        "The session exists, but the latest sync failed and needs another restore attempt.",
      pending: "No runtime session attached yet.",
    },
  },
  fields: {
    id: "Session ID",
    userId: "User",
    createdBy: "Created by",
    workspace: "Workspace",
    workspaceUnbound: "No workspace bound",
    createdAt: "Created",
    updatedAt: "Updated",
    expiresAt: "Expires",
    totalTurns: "Turns",
    lastAgent: "Last agent",
    lastSkill: "Last skill",
    lastModel: "Last model",
    titleSource: "Title source",
    tags: "Tags",
    summary: "Summary",
  },
} as const;
