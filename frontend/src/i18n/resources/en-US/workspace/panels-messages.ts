// P0-6：workspace.panels.messages 英文文案模块；形状由 zh-CN 同名模块经 satisfies 编译期约束。
export const enWorkspacePanelsMessages = {
  messageList: {
    emptyTitle: "The thread timeline is empty",
    emptyHint:
      "Start a turn to populate the workspace timeline. Runtime evidence, related items, and streamed output will attach back to the messages that produced them.",
    emptyInfraOnlyHint:
      "No conversation messages were persisted for this session: CLI / subagent-batch driven sessions keep only prompt infrastructure rows. See the Trajectory tab for this run's execution record.",
    backtrackNavHint:
      "Backtrack navigation active — use ↑/↓ (or j/k) to choose a user turn, Enter to open the confirm dialog, Esc to exit.",
    loadEarlier: "Load earlier messages",
    loadingEarlier: "Loading earlier messages…",
    streamStalledNotice:
      "Server turn may still be running — this page stopped receiving data.",
  },
  userBubble: {
    editAriaLabel: "Edit this user turn before backtrack",
    edit: "Edit",
    backtrackAriaLabel: "Backtrack to this user turn",
    backtrack: "Backtrack",
    editPromptAriaLabel: "Edit user turn prompt",
    editPromptPlaceholder: "Edit this user prompt, then continue to backtrack…",
    cancel: "Cancel",
    continue: "Continue to backtrack",
    copy: "Copy",
    copyAriaLabel: "Copy this user message",
    selected: "Selected this user turn",
    editHint:
      "Inline edit seeds the backtrack dialog. Confirm there to truncate later turns and prefill the composer.",
  },
  messageCard: {
    streamingBadge: "Streaming response in progress",
  },
  collapsedSummary: {
    tools: "{{count}} tool calls",
    replies: "{{count}} with replies",
    subagents: "{{count}} subagents",
    empty: "Thought for a while",
    expand: "Expand process details",
    collapse: "Collapse process details",
  },
  systemPrompt: {
    title: "System prompt",
    expand: "Expand system prompt",
    collapse: "Collapse system prompt",
  },
  contextRow: {
    title: "Context injection",
    expand: "Expand context",
    collapse: "Collapse context",
  },
  toolReceipt: {
    title: "Tool call",
  },
  turnUsage: {
    summary:
      "Token usage: prompt {{prompt}} · completion {{completion}} · total {{total}}",
  },
  turnTail: {
    copy: "Copy",
    copyAriaLabel: "Copy this reply",
    copied: "Copied",
    retryAriaLabel: "Retry this turn",
    statsLabel: "Turn stats",
  },
  // Wording mirrors the reference implementation (deepseek-harness locale.ts:71-72).
  branch: {
    label: "Branch into a new conversation",
    failed: "Branch failed. Please try again.",
    pending: "Creating branch session…",
  },
  flowFallback: {
    title: "Unknown event type",
    hint: "Raw payload preserved in a read-only view.",
  },
  segmentFallback: {
    loading: "Loading {{label}}…",
  },
  relatedArtifactsFallback: {
    loading: "Loading {{count}} related evidence items…",
  },
  markdown: {
    stoppedAriaLabel: "Response stopped",
    stopped: "Stopped",
    openArtifactOutput: "View full raw output",
    openArtifactOutputAriaLabel: "View full raw output ({{artifactId}})",
    artifactOutputCopied: "Copied full raw output id",
    artifactOutputCopyFailed: "Copy failed. Full id: {{artifactId}}",
  },
  reasoningRow: {
    title: "Reasoning",
    trimmed: "{{chars}} leading chars trimmed",
    expandLabel: "Expand reasoning",
    collapseLabel: "Collapse reasoning",
  },
  toolRow: {
    inputLabel: "Input",
    outputLabel: "Output",
    status: {
      started: "Started",
      running: "Running",
      finished: "Finished",
      failed: "Failed",
    },
    announcement: "Tool {{name}} status: {{status}}",
    expandLabel: "Expand tool details",
    collapseLabel: "Collapse tool details",
    exitCode: "Exit code {{code}}",
    diffAdditions: "+{{value}}",
    diffRemovals: "−{{value}}",
    openFile: "Open {{path}}",
    diff: {
      label: "Diff (line view)",
      ariaLabel: "Line-level diff of the tool patch: {{path}}",
      expand: "Expand diff view",
      filesAriaLabel: "Patch touches {{count}} files",
      truncated: "Patch text was truncated (first {{count}} lines kept): the line view covers only what was kept.",
      partial: "Patch text is incomplete (the trailing hunk has fewer lines); line numbers still come from the patch itself.",
    },
  },
  richContent: {
    relatedEvidence: "Related evidence",
  },
  backtrackDialog: {
    title: "Backtrack to this user message",
    description:
      "Truncate the conversation after this turn and optionally restore later file mutations.",
    closeLabel: "Close backtrack dialog",
    anchor: "Anchor",
    planning: "Planning backtrack…",
    willRemove: "Will remove",
    removedMessagesSuffix: "messages",
    laterUserTurnsSuffix: "later user turns",
    historyKeepsFirst: "History keeps the first",
    historyKeepsMessagesSuffix: "messages.",
    codeRestoreBaseCheckpoint: "Code restore can use base checkpoint",
    noCheckpoint: "No mutation checkpoint is mapped to this turn yet.",
    restoreMode: "Restore mode",
    modeConversation: "Conversation only",
    modeBoth: "Conversation + files",
    modeCode: "Files only (advanced)",
    modeCodeHint:
      "Only file mutations after this turn are written back; the conversation keeps its truncated state.",
    modeSelectHint:
      "Switching modes only refreshes the preview below; the backtrack runs after you press Confirm.",
    editPromptLabel: "Edit prompt before prefill",
    editPromptAriaLabel: "Edit backtrack prompt",
    editPromptPlaceholder: "Edit the original user prompt…",
    editPromptHint:
      "Leave unchanged to keep the original text. Edits are sent as edit_prompt and prefilled into the composer after apply.",
    prefillToggle: "Prefill composer with the original (or edited) prompt",
    cancel: "Cancel",
    confirm: "Confirm backtrack",
    working: "Working…",
  },
} as const;
