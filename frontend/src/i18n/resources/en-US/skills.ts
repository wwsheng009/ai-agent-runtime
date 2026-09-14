// P2-1A：技能市场 / 热重载（独立页面 /runtime/skills）。
// 与 zh-CN 真源逐键对齐（satisfies DeepStringShape）。

import type { DeepStringShape } from "../shape";
import type { zhSkills } from "../zh-CN/skills";

export const enSkills = {
  badge: "Skill market",
  independentPage: "Standalone page",
  title: "Skills / hot reload",
  description: "Browse the runtime skill catalog, search skill definitions, and manage file watching for skill directories.",
  backToRuntimeConfig: "Back to runtime config",

  nav: {
    usage: "Usage analytics",
    logs: "Logs",
  },

  actions: {
    refresh: "Refresh",
    search: "Search",
    clearSearch: "Back to catalog",
    refreshDetail: "Refresh detail",
    closeDetail: "Collapse",
    retry: "Retry",
  },

  errors: {
    forbidden: "Request rejected (403): the current admin token may not perform this action.",
    unavailable: "This capability is not enabled in the current runtime (endpoint returned 404/405/501/503).",
    failed: "Request failed. Check the logs and try again.",
  },

  catalog: {
    title: "Skill catalog",
    count: "{{count}} skills",
    loading: "Loading skill catalog…",
    empty: "No skills match the current filter.",
    searchPlaceholder: "Search skill keywords",
    categoryPlaceholder: "Category (optional)",
    modeLabel: "Search mode",
    mode: {
      auto: "Auto",
      lexical: "Lexical",
      semantic: "Semantic",
    },
    searching: "Searching…",
    resultCount: "“{{query}}” matched {{count}} results",
    resolvedMode: "Resolved mode: {{mode}}",
    usedEmbedding: "Embedding search used",
    lexicalOnly: "Lexical only",
    limitReached: "Reached the {{limit}} result limit; results may be truncated.",
    searchEmpty: "No matching skills.",
    selectHint: "Select a skill to view its detail",
  },

  detail: {
    title: "Skill detail",
    loading: "Loading skill detail…",
    absent: "Not provided",
    category: "Category",
    version: "Version",
    source: "Source",
    promptPath: "Prompt file",
    tags: "Tags",
    capabilities: "Capabilities",
    tools: "Tools",
    permissions: "Permissions",
    triggers: "Triggers",
    weight: "Weight {{value}}",
    workflow: "Workflow",
    dependsOn: "Depends on {{value}}",
    systemPrompt: "System prompt",
    userPrompt: "User prompt",
    contextFiles: "Context files: {{value}}",
  },

  stats: {
    title: "Skill stats",
    loading: "Loading skill stats…",
    totalSkills: "Total skills",
    embedding: "Embedding search",
    embeddingEnabled: "Enabled",
    embeddingDisabled: "Disabled",
    unknown: "Unknown",
    absent: "Not provided",
    skillDirs: "Skill directories",
    policy: "Mutation policy",
    policyUnknown: "The backend did not report a mutation policy; write availability is unknown.",
    policyFlags: {
      readOnly: "Read only",
      disableImport: "Import disabled",
      disablePersist: "Persist disabled",
      disableReloadOps: "Reload ops disabled",
      disableHotReload: "Hot reload disabled",
    },
    flagOn: "on",
    flagOff: "off",
    sourceSummary: "Source summary",
    unknownSource: "Unknown source",
    topSkills: "Top by call count",
    noRows: "No call statistics yet.",
    callCount: "{{count}} calls",
    successRate: "Success rate",
    avgDuration: "Avg duration",
  },

  hotReload: {
    title: "Skill hot reload",
    loading: "Loading hot reload status…",
    policyDisabled: "The mutation policy disables hot reload; start / stop / reload are unavailable.",
    enabled: "Hot reload",
    watching: "Watching",
    yes: "Yes",
    no: "No",
    skillCount: "Loaded skills",
    callbackCount: "Callbacks",
    debounce: "Debounce",
    dirs: "Watched directories",
    startDirs: "Directories to watch (one per line)",
    startDirsPlaceholder: "D:/skills\nE:/shared/skills",
    debounceMs: "Debounce (ms)",
    debouncePlaceholder: "Leave empty for backend default",
    start: "Start watching",
    starting: "Starting…",
    stop: "Stop watching",
    stopping: "Stopping…",
    reload: "Reload now",
    reloading: "Reloading…",
    forbiddenHint: "Set the admin token on the logs page and retry.",
    setTokenLink: "Open logs page to set the token",
  },

  // Workspace "Skills" tab (after the chat tab): catalog + detail dialog only;
  // hot reload stays on the standalone page.
  workspaceTab: {
    description: "Skills loaded by the current runtime; select one to inspect its full definition.",
    dialogAriaLabel: "Skill detail",
    closeDialog: "Close skill detail",
  },
} satisfies DeepStringShape<typeof zhSkills>;
