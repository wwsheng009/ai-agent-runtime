// P0-6：workspace.panels.messages 英文文案模块；形状由 zh-CN 同名模块经 satisfies 编译期约束。
export const enWorkspacePanelsMessages = {
  messageList: {
    emptyTitle: "The thread timeline is empty",
    emptyHint:
      "Start a turn to populate the workspace timeline. Runtime evidence, related items, and streamed output will attach back to the messages that produced them.",
    backtrackNavHint:
      "Backtrack navigation active — use ↑/↓ (or j/k) to choose a user turn, Enter to open the confirm dialog, Esc to exit.",
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
    expand: "Expand context",
    collapse: "Collapse context",
  },
  turnUsage: {
    summary:
      "Token usage: prompt {{prompt}} · completion {{completion}} · total {{total}}",
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
  },
  reasoningRow: {
    title: "Reasoning",
    trimmed: "{{chars}} leading chars trimmed",
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
    expandLabel: "Expand tool input",
    collapseLabel: "Collapse tool input",
    exitCode: "Exit code {{code}}",
    diffAdditions: "+{{value}}",
    diffRemovals: "−{{value}}",
    openFile: "Open {{path}}",
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
    editPromptLabel: "Edit prompt before prefill",
    editPromptAriaLabel: "Edit backtrack prompt",
    editPromptPlaceholder: "Edit the original user prompt…",
    editPromptHint:
      "Leave unchanged to keep the original text. Edits are sent as edit_prompt and prefilled into the composer after apply.",
    prefillToggle: "Prefill composer with the original (or edited) prompt",
    cancel: "Cancel",
    working: "Working…",
  },
} as const;
