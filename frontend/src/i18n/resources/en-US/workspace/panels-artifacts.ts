// P0-6：workspace.panels.artifacts 英文文案模块；形状由 zh-CN 同名模块经 satisfies 编译期约束。
import type { DeepStringShape } from "../../shape";
import type { zhWorkspacePanelsArtifacts } from "../../zh-CN/workspace/panels-artifacts";

export const enWorkspacePanelsArtifacts = {
  panel: {
    description:
      "Workspace artifacts, plan preview, restore points, and backtrack audit for the current thread.",
  },
  list: {
    empty: "Artifacts appear here as the thread runs.",
  },
  detail: {
    categoryEvidence: "Runtime evidence",
    categoryOutput: "Output file",
    closeLabel: "Close artifact detail",
    artifactPath: "Artifact path",
    tabsLabel: "Artifact reading modes",
    tabPreview: "Preview",
    tabSource: "Source",
    imageTitle: "Rendered image",
    imageHint:
      "Inspect the generated image at full width. Use the metadata below for prompt and integrity details.",
    imageUnavailableTitle: "Image unavailable",
    imageUnavailableHint:
      "This artifact was recorded as an image, but the MIME type is not renderable inline.",
    fileName: "File name",
    mimeRenderHint: "MIME type {{mime}} cannot be rendered inline.",
    openRawFile: "Open raw file",
    previewTitle: "Rendered preview",
    previewHint: "Use the full dialog width to inspect the rendered output.",
    sourceTitle: "Source reader",
    sourceHint:
      "Inspect the exact file contents without squeezing them into the rail.",
    sourceFullHint:
      "Inspect the exact structured payload or file contents in a full-width dialog.",
  },
  tabs: {
    panelTitle: "Artifacts",
    tabListLabel: "Artifact panel surfaces",
    items: "Items",
    plan: "Plan",
    planLive: "live",
    restore: "Restore",
    usage: "Usage",
  },
  lazySurfaces: {
    loadingRestorePoints: "Loading restore points…",
    loadingPlanPreview: "Loading plan preview…",
  },
  plan: {
    title: "Plan preview",
    loading: "Loading",
    noSession:
      "Plan preview becomes available after the thread attaches to a live session.",
    permission: "Permission:",
    previous: "Previous:",
    lastDecision: "Last decision:",
    entered: "Entered {{time}}",
    exited: "Exited {{time}}",
    workspacePath: "Workspace: {{path}}",
    writeAllow: "Write allow:",
    notes: "Notes:",
    contentTitle: "Plan content",
    truncated: "Truncated",
    reviewNotes: "Review notes",
    notesPlaceholder: "Optional notes for approve / request changes / quit",
    approve: "Approve",
    requestChanges: "Request changes",
    quit: "Quit",
    decisionLocked: "Decision actions unlock while plan mode is active.",
  },
  checkpoints: {
    title: "Restore points",
    loading: "Loading",
    noSession:
      "Restore points become available after the thread attaches to a live session.",
    empty: "No restore points available for this session yet.",
  },
  checkpointDetail: {
    title: "Checkpoint detail",
    badgeCheckpoint: "checkpoint",
    messageCount: "{{count}} messages",
    exactConversation: "exact conversation",
    restoreConversation: "Restore conversation",
    restoreFiles: "Restore files",
    restoreBoth: "Restore both",
    conversationSnapshot: "Conversation snapshot",
    previewSummary: "Preview summary",
    fileDiffReader: "File diff reader",
    fileDiffReaderHint:
      "Review captured file changes from the selected checkpoint.",
    snapshotMetadata: "Snapshot metadata",
    summary: "Summary",
    readingState: "Reading state",
    changedFiles: "Changed files",
    fileBadge: "file",
    noFileDiffs: "No checkpoint file diffs available yet.",
    empty: "No checkpoint selected",
    emptyHint:
      "Select a runtime checkpoint from the timeline to inspect the conversation snapshot, preview summary, and captured file diffs.",
  },
  backtrackAudit: {
    title: "Backtrack audit",
    loading: "Loading",
    noSession:
      "Backtrack tombstones appear after the thread attaches to a live session and a user-turn rewind is applied.",
    empty:
      "No backtrack tombstones yet. Apply a user-turn rewind to record an audit summary here.",
    detailTitle: "Tombstone detail",
    badgeAudit: "audit",
    badgeEdited: "edited",
    badgeIncludeAnchor: "include anchor",
    removedMessages: "−{{count}} msgs",
    removedTurns: "−{{count}} turns",
    keptMessages: "kept {{count}}",
    identity: "Identity:",
    message: "Message:",
    baseCheckpoint: "Base checkpoint:",
    laterCheckpoints: "Later checkpoints:",
    removedMessageIds: "Removed message ids:",
    removedTurnIds: "Removed turn ids:",
    footnote:
      "Tombstones are durable audit summaries only. Truncated transcript text is not recoverable from this panel.",
  },
} satisfies DeepStringShape<typeof zhWorkspacePanelsArtifacts>;
