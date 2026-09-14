// 由 src/i18n/resources/en-US.ts 机械拆分而来（P0-6），仅搬迁不改语义。
import type { DeepStringShape } from "../shape";
import type { zhCommon } from "../zh-CN/common";

export const enCommon = {
  actions: {
    close: "Close",
    cancel: "Cancel",
    confirm: "Confirm",
    reset: "Reset",
    refresh: "Refresh",
    copied: "Copied",
    copyLink: "Copy link",
    clearAll: "Clear all",
    clearSearch: "Clear search",
    copyValue: "Copy value",
    copyMetadata: "Copy metadata",
    copyPreview: "Copy preview",
    copyFields: "Copy fields",
    copyJson: "Copy JSON",
  },
  brand: {
    shortName: "AR",
  },
  codeBlock: {
    fallbackTitle: "Code snippet",
    copy: "Copy code",
    showMoreLines: "Show {{count}} more lines",
    collapse: "Collapse code",
    showingAll: "Showing all {{count}} lines.",
    hiddenNote: "{{count}} more lines hidden for readability.",
  },
  loading: {
    page: "Loading page...",
    details: "Loading details...",
    logs: "Loading logs...",
  },
  errors: {
    boundary: {
      globalTitle: "Something went wrong",
      globalDescription:
        "The interface failed to render and this view cannot continue. You can retry, or go back to the home page.",
      routeTitle: "Page failed to load",
      routeDescription:
        "This page is temporarily unavailable. Retry, or go back to the home page.",
      panelTitle: "Panel failed to load",
      panelDescription:
        "This panel is temporarily unavailable. The rest of the workspace is unaffected.",
      retry: "Retry",
      home: "Back to home",
      reload: "Reload",
    },
    chunk: {
      title: "Resource failed to load",
      description:
        "Page resources failed to load after {{attempts}} automatic retries. Check your network and retry manually.",
      hint: "If retrying keeps failing, refresh the page.",
    },
    startup: {
      title: "Application failed to start",
      description:
        "Something went wrong while initializing the application, so the interface cannot load.",
      missingRoot: "The application mount node (#root) is missing. Check the page markup.",
      reload: "Reload",
    },
  },
  states: {
    justNow: "just now",
    none: "None",
    live: "Live",
    online: "Online",
    offline: "Offline",
    connecting: "Connecting",
    reconnecting: "Reconnecting",
    streamError: "Stream error",
    idle: "Idle",
    fileDetected: "File detected",
    waitingForLogFile: "Waiting for log file",
    generated: "Generated",
    notGenerated: "Not generated",
    synced: "Synced",
    unsynced: "Unsynced",
    enabled: "Enabled",
    disabled: "Disabled",
  },
} satisfies DeepStringShape<typeof zhCommon>;
