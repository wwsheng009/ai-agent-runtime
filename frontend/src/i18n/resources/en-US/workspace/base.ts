// 由 src/i18n/resources/en-US.ts 机械拆分而来（P0-6），仅搬迁不改语义；
// 后续面板文案写入同目录 panels-*.ts，由 index.ts 组合。
import type { DeepStringShape } from "../../shape";
import type { zhWorkspaceBase } from "../../zh-CN/workspace/base";

export const enWorkspaceBase = {
  shell: {
    newChatTitle: "What do you want to accomplish?",
    suggestions: {
      analyze: {
        title: "Analyze project",
        description: "Understand the code, dependencies, and current state",
        prompt:
          "Analyze the current project's structure, dependencies, and workspace state first. Summarize the key findings, then recommend the next step.",
      },
      implement: {
        title: "Implement a change",
        description: "Update the code and run the necessary checks",
        prompt:
          "Analyze this requirement, create a concise implementation path, then update the code and run the necessary verification.",
      },
      review: {
        title: "Review code",
        description: "Find defects, risks, and improvement opportunities",
        prompt:
          "Review the current code changes. Prioritize defects, regression risks, and missing tests, then recommend actionable improvements.",
      },
      plan: {
        title: "Create a plan",
        description: "Break down the task and move it forward",
        prompt:
          "Clarify the goal and constraints, create a phased implementation plan, and begin with the highest-priority step.",
      },
    },
    loadingSettingsPanel: "Loading settings panel...",
    loadingArtifactPanel: "Loading artifact panel...",
    loadingArtifactDetails: "Loading artifact details...",
  },
  usagePanel: {
    title: "Session usage",
    ariaLabel: "Session usage panel",
    refresh: "Refresh session usage",
    loading: "Loading session usage...",
    errorTitle: "Failed to load session usage",
    empty: "No usage recorded for this session yet",
    emptyHint:
      "Token, request, and coverage stats appear here once the session makes LLM calls.",
    openFullReport: "Full report",
    openFullReportHint: "Open the full usage detail for this session",
    unknownProvider: "Unknown provider",
    qualityUnknown: "unknown",
    metrics: {
      tokens: "Total tokens",
      requests: "LLM requests",
      turns: "Turns",
      cacheHit: "Cache hit",
      averageResponse: "Avg response",
      coverage: "Usage coverage",
    },
    quality: "Quality {{quality}} · coverage {{coverage}}",
    updatedAt: "Updated {{time}}",
    partial: "This session has partial usage data; see the usage page for details.",
  },
  topbar: {
    home: "Home",
    newChat: "New chat",
    logs: "Logs",
    usage: "Usage",
    runtime: "Runtime",
    settings: "Settings",
    openSidebar: "Open chat navigation",
    showFiles: "Show files",
    hideFiles: "Hide files",
    newThreadTitle: "New chat",
    threadTransport: {
      live: "Live runtime",
      error: "Runtime degraded",
      seeded: "Seeded preview",
    },
    threadStatus: {
      sessionAttached: "Session attached",
      previewThread: "Preview thread",
      newThread: "New thread",
    },
    subtitle: {
      needsRestoreWithSession: "Session {{sessionId}} needs restore attention",
      needsRestore: "Runtime restore needs attention",
      viaSource: "{{transportLabel}} via {{source}}",
      session: "Session {{sessionId}}",
    },
  },
  sidebar: {
    workspaceLabel: "Workspace",
    appName: "AI Agent Runtime",
    refreshRuntimeTeams: "Refresh runtime teams",
    openSettings: "Open settings",
    navigation: "Chat and session navigation",
    closeNavigation: "Close chat navigation",
    startNewChat: "Start new chat",
    searchPlaceholder: "Search threads",
    sections: {
      chats: "Local chats",
      directories: "Directories",
      sessions: "Sessions",
      runtime: "Runtime overview",
    },
    sessionUserSelect: "Session user",
    sessionUserOption: "{{user}} · {{count}} sessions",
    sessionUserOptionDefault: "{{user}} · {{count}} sessions · default",
    sessionUserDefault: "default",
    sessionUsersLoading: "loading users",
    sessionDirectoryUnscoped: "Unscoped sessions",
    session: {
      rename: "Rename session",
      renamePlaceholder: "New session title",
      switchTitle: "Session is still responding",
      switchMessage: "Switch to {{title}}?",
      switchHint:
        "The reply keeps running in the background — switch back anytime.",
      switchConfirmButton: "Switch",
      switchCancel: "Cancel",
    },
    directories: {
      add: "Add directory",
      addTitle: "Add workspace directory",
      addHint:
        "Register an existing folder so its sessions can be grouped. The directory must already exist on the runtime host.",
      pathLabel: "Directory path",
      pathPlaceholder: "e.g. E:\\projects\\demo",
      nameLabel: "Display name (optional)",
      namePlaceholder: "Defaults to the folder name",
      cancel: "Cancel",
      existsWarning: "Directory is missing on the runtime host",
      newChat: "New chat in this directory",
      rename: "Rename directory",
      deleteTitle: "Remove directory",
      deleteConfirm:
        "This ungroups {{count}} sessions from this directory in the sidebar.",
      deleteHint:
        "Only the registry entry is removed; files on disk stay untouched.",
      deleteConfirmButton: "Remove",
      empty: "Registered workspace directories will appear here.",
    },
    threadStatuses: {
      review: "Waiting for review",
      draft: "Draft thread",
      active: "Active thread",
    },
    sessionStatuses: {
      error: "Session sync error",
      restored: "Restored session",
      attached: "Attached runtime session",
      pending: "No runtime session attached yet",
    },
    sessionDetails: {
      pending: "No runtime session attached yet.",
      error:
        "The session exists, but the latest sync failed and needs another restore attempt.",
      restored: "Recovered from runtime session history and ready to continue.",
      attached: "Attached to a live runtime session from the active workspace flow.",
    },
    emptyChats: {
      search: "No local chats match the current search.",
      default:
        "Local-only chats will appear here before runtime session attachment.",
    },
    emptySessions: {
      search: "No sessions match the current search.",
      default:
        "Recoverable runtime sessions will appear here after loading.",
    },
    runtimeStats: {
      sessions: "{{count}} sessions",
      recoverable: "{{count}} recoverable",
      pending: "{{count}} pending",
      syncing: "syncing",
    },
    runtimeTeamsUnavailable: "No runtime teams available.",
    openRuntimeTeamDetails: "Open runtime team details",
    backendConfigPage: "Backend config page",
    active: "{{count}} active",
    unknown: "unknown",
  },
  composer: {
    transport: {
      live: "live runtime",
      error: "runtime error",
      seeded: "seeded",
    },
    sessionState: {
      attached: "session attached",
      new: "new session",
    },
    placeholder: {
      newThread:
        "Ask the workspace to inspect, build, review, or coordinate the next step...",
      thread:
        "Ask the workspace to research, change, verify, or coordinate the next step...",
    },
    submit: {
      stopResponse: "Stop response",
      startNewThread: "Start new thread",
      sendTurn: "Send turn",
      startThread: "Start thread",
    },
    promptTips: "prompt tips",
    promptTipsMenuTitle: "Prompt tips",
    responseActive: "response active",
    provider: "Provider",
    model: "Model",
    reasoning: "Reasoning",
    reasoningDefault: "Default",
    reasoningDefaultWithValue: "Default ({{effort}})",
    loadingModels: "loading models",
    modelCatalogUnavailable: "model catalog unavailable",
    runtimeDefaultModel: "runtime default model",
    modelWithName: "model {{model}}",
    shortcuts: "Ctrl/Cmd + Enter",
    stop: "stop",
    submitShort: "submit",
    filesCount: "{{count}} files",
  },
} satisfies DeepStringShape<typeof zhWorkspaceBase>;
