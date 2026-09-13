// 由 src/i18n/resources/en-US.ts 机械拆分而来（P0-6），仅搬迁不改语义。
import type { DeepStringShape } from "../shape";
import type { zhSettings } from "../zh-CN/settings";

export const enSettings = {
  dialog: {
    eyebrow: "Workspace settings",
    title: "Frontend workspace settings",
    description:
      "This panel only stores local frontend preferences. Backend config.yaml has moved to the dedicated Runtime Config page.",
    backendConfig: "Backend config",
    resetFrontendDefaults: "Reset frontend defaults",
    close: "Close settings",
    localStorageFooter:
      "Frontend settings are written to the current browser localStorage immediately. Use the Runtime Config page for backend configuration. Open this panel again with Ctrl/Cmd + ,.",
  },
  sections: {
    appearance: {
      label: "Appearance",
      description: "Accent, theme, fonts, and motion",
    },
    workspace: {
      label: "Workspace",
      description: "Layout and file bar behavior",
    },
    chat: {
      label: "Chat defaults",
      description: "Provider, model, and reasoning effort",
    },
    notifications: {
      label: "Notifications",
      description: "Desktop alerts and permissions",
    },
    harness: {
      label: "Harness",
      description: "Permissions, memory, and local plugins",
    },
    about: {
      label: "About",
      description: "Runtime summary and local storage",
    },
  },
  localization: {
    title: "Language and region",
    description: "Controls interface language, date/time, and relative time formats.",
    system: "Follow system",
    simplifiedChinese: "Simplified Chinese",
    english: "English",
  },
  appearance: {
    theme: "Theme",
    themeDescription:
      "Controls the entire frontend color mode, including workspace, settings, and landing page.",
    themeApplied: "Applied",
    currentlySetTo: "Currently set to",
    themeSystemResolved: "Follow system (currently resolves to {{resolved}})",
    themeOptions: {
      system: {
        label: "Follow system",
        description: "Listen to the system color preference and sync automatically when it changes.",
      },
      light: {
        label: "Light",
        description: "Best for daytime use, documentation, and bright environments.",
      },
      dark: {
        label: "Dark",
        description: "Best for terminal-style workflows, nights, and low light.",
      },
    },
    accent: "Accent",
    accentDescription:
      "Affects the settings dialog, primary action buttons, and any workspace surfaces wired to variables.",
    accentOptions: {
      gold: {
        label: "Amber relay",
        description: "Keep the current gold-highlighted workspace tone.",
      },
      cyan: {
        label: "Cool signal",
        description: "Switch the main accent to a cooler system-state tone.",
      },
      violet: {
        label: "Route focus",
        description: "Use a violet accent closer to routing and planning semantics.",
      },
    },
    fontFamily: "Font family",
    fontFamilyDescription:
      "Switch the typeface consistently between interface body copy and code surfaces.",
    bodyFont: "Interface and body",
    bodyFontDescription:
      "Applies to the landing page, workspace, settings panel, and serif display headings.",
    codeFont: "Code and logs",
    codeFontDescription:
      "Applies to code blocks, log JSON previews, and monospace editor surfaces.",
    fontFamilyOptions: {
      system: {
        label: "System UI",
        description: "Segoe UI / Helvetica Neue / system-ui",
        sample: "Operational detail stays readable during long sessions.",
      },
      humanist: {
        label: "Readable humanist",
        description: "Trebuchet MS / Verdana / Palatino",
        sample: "Comfortable for dense workspace copy and settings text.",
      },
      editorial: {
        label: "Editorial modern",
        description: "Aptos / Cambria / Georgia",
        sample: "Softer rhythm for landing copy and long-form reading.",
      },
    },
    codeFontOptions: {
      jetbrains: {
        label: "JetBrains stack",
        description: "JetBrains Mono / Cascadia Code / Consolas",
        sample: 'const traceId = receipts.latest()?.trace_id ?? "";',
      },
      cascadia: {
        label: "Cascadia stack",
        description: "Cascadia Code / JetBrains Mono / Consolas",
        sample: "await runtime.follow({ requestId, sessionId });",
      },
      classic: {
        label: "Classic console",
        description: "Consolas / SFMono / Menlo / Monaco",
        sample: 'if (event.level === "error") return halt(event);',
      },
    },
    size: "Size",
    sizeDescription:
      "Controls the base rhythm of the entire site; chat and code sizes override their own high-frequency reading areas.",
    sizeHint: "You can type an exact size directly, in the range {{min}}-{{max}}px.",
    customPixels: "Custom pixel value",
    customPixelsUnit: "px",
    restoreDefaultWithValue: "Restore default {{value}}",
    motion: "Motion",
    motionDescription:
      "Useful for remote desktops, screen recordings, or when you want a more restrained interface.",
    reducedMotion: "Reduce motion",
    reducedMotionDescription:
      "Turns off pulses, floating effects, and most transitions, and makes scrolling immediate.",
    preview: "Live preview",
    previewDescription:
      "The sample below refreshes immediately with your current font and size choices.",
    workspaceSample: "Workspace reading sample",
    chatSample: "Chat reading sample",
    codeSample: "Code and font stack preview",
    previewSampleText: "Operational detail stays readable in long sessions.",
    previewWorkspaceBody:
      "Task threads, runtime context, and artifacts stay readable in this area.",
    currentUIStack: "Current interface font stack:",
    currentSerifStack: "Current heading serif stack:",
    currentCodeStack: "Current code font stack:",
    currentScope: "Current scope",
    immediate: "Immediate",
    browserPersistence: "Browser persistence",
    uiTypography: "Interface and body",
    codeTypography: "Code and logs",
  },
  workspace: {
    density: "Interface density",
    densityDescription:
      "Controls the vertical spacing of the sidebar, message area, and input area.",
    densityOptions: {
      comfortable: {
        label: "Comfortable",
        description: "Keep larger paragraph spacing and sidebar breathing room for long reading sessions.",
      },
      compact: {
        label: "Compact",
        description: "Reduce message and navigation spacing to fit more content on smaller screens.",
      },
    },
    compact: "Compact",
    comfortable: "Comfortable",
    fileBar: "File bar behavior",
    fileBarDescription:
      "Controls how the right-side file panel opens when the runtime produces a new artifact.",
    autoOpenArtifacts: "Auto open file panel",
    autoOpenArtifactsDescription:
      "When enabled, the right-side panel expands automatically for newly selected artifacts; when disabled, the selection is recorded without forcing the panel open.",
  },
  chat: {
    title: "Default model routing",
    description:
      "Updates the provider and model attached when the workspace starts a new turn.",
    defaultProvider: "Provider",
    defaultModel: "Model",
    loadingProvider: "Loading provider...",
    noProvider: "No provider available",
    loadingModel: "Loading models...",
    noModel: "This provider has no selectable models",
    summaryLoading: "Runtime model catalog is loading.",
    summaryTemplate:
      "Detected {{providerCount}} providers. The default session now routes to {{provider}} / {{model}}.",
    openBackendConfig: "Open backend config page",
    manageProviders: "Manage Provider list",
    executionMode: "Execution mode",
    executionModeDescription:
      "Controls whether workspace chat enters the backend ReAct tool loop.",
    enableReact: "Enable ReAct tool loop",
    enableReactDescription:
      "When enabled, requests carry <code>enable_react: true</code>, the backend exposes tool definitions to the model, and the agent enters the tool-calling loop. When disabled, skill routes or direct LLM fallback still work, but the model itself will not trigger tool calls.",
    currentMode: "Current mode",
    reactMode: "ReAct tool mode",
    routeDirectMode: "Route / direct mode",
    reasoning: "Reasoning effort",
    reasoningDescription:
      "Choose the default reasoning level that shapes planning depth and tool budget for new turns.",
    reasoningOptions: {
      default: {
        label: "Runtime default",
        description: "Let the backend default strategy handle reasoning effort completely.",
      },
      minimal: {
        label: "Minimal",
        description: "Uses the smallest reasoning budget for simple follow-ups and very short turns.",
      },
      low: {
        label: "Low",
        description: "Returns faster and suits normal Q&A and small edits.",
      },
      medium: {
        label: "Medium",
        description: "Balances speed and quality for most daily tasks.",
      },
      high: {
        label: "High",
        description: "Favors deeper decomposition and multi-step reasoning.",
      },
    },
    maxSteps: "Max steps",
    maxStepsDescription:
      "Limits the maximum number of planning / routing / tool execution steps per turn; set 0 for no limit.",
    currentMaxSteps: "Current value: {{count}}.",
    maxStepsAdvice:
      "Defaults to 0 (no limit) so the model decides when a run is done; when constraining, 8 to 12 usually covers common workspace tasks.",
  },
  notifications: {
    title: "Workspace notifications",
    description:
      "Only used when the current tab is hidden, to notify you when a response completes or the runtime errors.",
    desktop: "Desktop alerts",
    desktopDescription:
      "If turned off, no desktop notification will appear even when browser permission is granted.",
    permission: "Permission status",
    permissionDescription: "Browser permission is required. It will not interrupt you while the page is visible.",
    permissionStates: {
      granted: "Granted. Background completion or error notifications can appear.",
      denied: "Blocked by the browser. Re-enable the site permission manually.",
      default: "Not requested yet. Click the button on the right to ask the browser for permission.",
      unsupported: "This environment does not support browser desktop notifications.",
    },
    currentConfig: "Current configuration",
    effectiveCondition: "Effective conditions",
    effectiveConditionDescription:
      "Notifications only actually appear when the master switch is on, desktop alerts are enabled, permission is granted, and the page is in the background.",
    requestPermission: "Request permission",
    enableDesktop: "Enable desktop alerts",
    disableDesktop: "Disable desktop alerts",
    enabled: "On",
    disabled: "Off",
    currentConfigMasterSwitch: "Notification master switch",
    currentConfigDesktopSwitch: "Desktop alerts",
  },
  harness: {
    title: "Project harness",
    description:
      "Inspect project permission rules, durable remembered grants, project memory notes, and local plugins for the current workspace.",
    workspaceTitle: "Current workspace",
    workspaceMissing: "Workspace path is not set for this runtime client.",
    loading: "Loading",
    ready: "Ready",
    refresh: "Refresh",
    notAvailable: "not available",
    permissionsTitle: "Permission rules",
    permissionsDescription:
      "Read-only view of project `.aicli/permissions.yaml`. Edit the file on disk to change rules.",
    permissionsFile: "Permissions file",
    permissionsExists: "Loaded version {{version}}",
    permissionsMissing: "No project permissions file found yet.",
    denyTools: "Deny tools",
    allowTools: "Allow tools",
    noDenyTools: "No hard deny tools configured.",
    noAllowTools: "No hard allowlist configured.",
    rulesTitle: "Static rules",
    noRules: "No named rules in permissions.yaml.",
    unnamedRule: "Rule {{index}}",
    grantsTitle: "Remembered grants",
    grantsDescription:
      "Durable always-allow grants stored in project `.aicli/grants.json`. Dangerous tools cannot be remembered.",
    grantsStore: "Grants store",
    rememberGrant: "Remember grant",
    grantToolPlaceholder: "Tool name, e.g. write",
    grantPatternPlaceholder: "Optional path/pattern",
    grantHint:
      "Pattern is optional. Empty pattern remembers a tool-wide grant for safer tools only.",
    remember: "Remember",
    revoke: "Revoke",
    toolWideGrant: "(tool-wide)",
    noGrants: "No durable grants remembered yet.",
    grantRememberFailed: "Failed to remember grant",
    grantRevokeFailed: "Failed to revoke grant",
    memoryTitle: "Project memory",
    memoryDescription:
      "Keyword list/search and append for project memory notes under `.aicli/memory`.",
    memorySearch: "Search notes",
    memorySearchPlaceholder: "Keyword query",
    search: "Search",
    memoryAppend: "Append note",
    memoryTextPlaceholder: "Write a durable project note",
    memoryTagsPlaceholder: "Optional tags, comma separated",
    append: "Append",
    noMemoryNotes: "No memory notes yet.",
    noMemoryHits: "No notes matched this query.",
    memoryAppendFailed: "Failed to append memory note",
    pluginsTitle: "Local plugins",
    pluginsDescription:
      "Local plugin catalog discovered under project/user plugin roots. Marketplace install is out of scope for this panel.",
    pluginNoDescription: "No description provided.",
    pluginActive: "active",
    pluginInactive: "inactive",
    pluginVersion: "v{{version}}",
    capabilityBadge: "cap:{{capability}}",
    trust: "Trust",
    untrust: "Untrust",
    enable: "Enable",
    disable: "Disable",
    noPlugins: "No local plugins discovered.",
    pluginUpdateFailed: "Failed to update plugin",
  },
  about: {
    currentWorkspace: "Current workspace",
    description:
      "These details quickly confirm where the frontend is sending requests and where local preferences are stored.",
    runtimeIdentityDescription:
      "The frontend creates a persistent runtime client id for the current browser and uses it to derive the userId. Resetting switches to a new session namespace.",
    apiBase: "API base",
    apiBaseFallback: "same-origin /api proxy",
    currentRoute: "Current route",
    runtimeIdentity: "Runtime identity",
    runtimeUserId: "Runtime user id",
    workspacePath: "Workspace path",
    resetRuntimeClientId: "Reset local runtime client id",
    runtimeOverview: "Runtime overview",
    runtimeOverviewDescription:
      "This reads the runtime summary already loaded on the page and does not send any extra request.",
    localStorage: "Local storage",
    localStorageDescription:
      "Settings are stored in browser localStorage and are not written back to the repo config file.",
    settingsKey: "settings localStorage key",
    runtimeClientKey: "runtime client localStorage key",
    selectedProvider: "Available providers",
    selectedModel: "Current model",
    sessionCount: "Session count",
    recoverableSessions: "Recoverable sessions",
    activeTeams: "Active teams",
    activeTeamsSummary: "Loaded {{count}} team summaries",
    sessionBreakdown: "{{active}} active / {{archived}} archived",
    latestUpdated: "Updated {{time}}",
    noSessions: "No sessions found yet",
    runtimeDefault: "runtime default",
    scopeLabel: "scope",
    notSet: "not set",
  },
} satisfies DeepStringShape<typeof zhSettings>;
