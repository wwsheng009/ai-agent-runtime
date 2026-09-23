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
  sections: {
    basic: "Basics",
    timing: "Timing",
    runtime: "Runtime",
    content: "Content",
  },
  network: {
    title: "Network details",
    channels: {
      runtime: "Runtime stream",
      chat: "Direct turn",
    },
    state: {
      active: "Connected",
      inactive: "Not connected",
    },
    fields: {
      lastFrame: "Last bytes",
      lastEvent: "Last event",
      lastKeepalive: "Last keepalive",
      events: "Events",
      keepalives: "Keepalives",
      bytes: "Bytes",
      stalls: "Idle stalls",
      errors: "Errors",
      cursor: "Cursor",
      opens: "Opens/closes",
      gap: "Frame gap",
      never: "None yet",
      none: "—",
    },
    gate: {
      title: "Render gate",
      open: "Open",
      closed: "Closed",
      blockedDeltas: "Blocked deltas",
      unownedTurns: "Unowned turns",
      snapshotRefreshes: "Snapshot refreshes",
      turn: "Live turn",
      none: "None",
    },
    // Session subscriptions (Batch 4 §4.6): registry projection — how many entries
    // hold live SSE, how many degraded to polling, whether page visibility forced
    // background downsampling, and the live budget. Hidden when the flag is off.
    subscriptions: {
      title: "Session subscriptions",
      live: "Live",
      poll: "Polling",
      idle: "Idle",
      none: "Not subscribed",
      total: "Subscriptions",
      session: "This session",
      visibility: "Visibility",
      foreground: "Foreground",
      background: "Background downsampled",
      budget: "Live budget",
    },
    dom: {
      title: "DOM activity",
      changes: "Changes",
      lastChange: "Last change",
      observing: "Message list attached",
      detached: "Message list not found",
    },
    frames: {
      title: "Recent frames",
      empty: "No frames received yet",
      keepalive: "Keepalive",
    },
    // Traffic sparkline: per-second throughput (bytes/s) as a mini bar chart.
    // The counter grid says how much arrived; this says whether it is still
    // arriving, and whether it trickles or bursts.
    traffic: {
      title: "Traffic · last 60s",
      empty: "No traffic in the last 60s",
      peak: "Peak",
      total: "Total",
      currentSecond: "current second (in progress)",
      windowStart: "60s ago",
      windowNow: "now",
    },
    verdict: {
      states: {
        healthy: "Healthy",
        renderBlocked: "Arrived, not rendered",
        noEvents: "No events",
        channelError: "Channel error",
        idle: "Idle",
      },
      hints: {
        healthy: "SSE is pushing data and the gate is open: the render path is healthy.",
        renderBlocked:
          "Deltas arrive over SSE but the front-end gate drops them: the fault is in rendering, not the network.",
        noEvents:
          "The channel is open but no bytes arrived recently: the fault is in the SSE live / server / proxy path, not in front-end rendering.",
        channelError:
          "The connection reported errors or fell back after repeated failures: check the network and the runtime service first.",
        idle: "The channel is open; no in-flight turn is producing data right now.",
      },
    },
  },
  // Routing block (plan §7.2/§7.3/§7.4): read-only projection of session agent
  // routing plus the three writable layers. Every displayed value comes from the
  // backend projection (I-6); this module only holds copy.
  routing: {
    title: "Routing",
    loading: "Loading routing…",
    scope: {
      main: "Main agent",
      sub: "Sub-agent",
    },
    state: {
      enabled: "Enabled",
      disabled: "Disabled",
    },
    summary: {
      level: "Effective level",
      provider: "provider",
      model: "model",
      effort: "effort",
      source: "Source",
      revision: "Revision",
      effectiveFrom: "Takes effect",
      none: "—",
      effectiveFromNextTurn: "next turn",
    },
    source: {
      session: "Session",
      workspace: "Workspace",
      config: "Config",
      default: "Default",
      derived: "Derived",
    },
    layers: {
      label: "Write layer",
      session: "Session",
      workspace: "Workspace",
      config: "Config",
      locked: "Read-only",
      lockedHint: "This layer is not writable right now (not exposed by the backend); it is greyed out.",
      childSessionHint: "Child sessions do not write routing overrides: the parent session and config layers decide.",
    },
    target: {
      label: "Write target",
      session: "Session record (persisted with the session)",
      none: "—",
    },
    levels: {
      level: "Level",
      enabled: "On",
      disabled: "Off",
      expensive: "Expensive level",
      provider: "provider",
      model: "model",
      effort: "effort",
      source: "Source",
      empty: "No editable levels right now (routing is disabled or has no configured levels).",
      inheritedHint:
        "This value comes from the \"{{source}}\" layer: clearing it here changes nothing — use Reset to drop this layer's override.",
    },
    enableToggle: {
      label: "Enable routing",
      hint: "Maps to main_agent.enabled; writes take effect next turn.",
    },
    warnings: {
      title: "Warnings",
    },
    actions: {
      save: "Save",
      reset: "Reset",
      confirm: "Confirm write",
      cancel: "Cancel",
      reload: "Refresh routing",
    },
    confirm: {
      title: "Write to the global config?",
      body: "This write modifies {{path}} and applies to every workspace and session.",
      bodyNoPath: "This write modifies the global config and applies to every workspace and session.",
    },
    notice: {
      saved: "Saved to the \"{{layer}}\" layer; takes effect next turn.",
      unchanged: "Nothing to write.",
      reset: "Cleared the \"{{layer}}\" layer's routing override.",
      actorInvalidated: "The running actor was invalidated and will re-resolve routing on the next turn.",
    },
    errors: {
      load: "Failed to load routing",
      save: "Failed to write routing",
    },
  },
} as const;
