// MCP 管理面板文案（en-US 对齐 zh-CN 的 key 形状）。

import type { DeepStringShape } from "../../shape";
import type { zhRuntimeConfigMcp } from "../../zh-CN/runtime-config/mcp";

export const enRuntimeConfigMcp = {
  title: "MCP management",
  description:
    "Inspect the MCP servers registered with the runtime and add, edit, delete, enable/disable, or hot-reload them. Changes are written to the runtime config directly and do not go through the config document draft below.",
  counts: {
    total: "{{count}} MCP servers",
    connected: "{{count}} connected",
    tools: "{{count}} tools",
  },
  fields: {
    name: "Name",
    type: "Type",
    status: "Connection",
    trustLevel: "Trust level",
    endpoint: "Endpoint",
    tools: "Tools",
    description: "Description",
    enabled: "Enabled",
    command: "Command",
    args: "Arguments",
    env: "Environment",
    url: "URL",
    headers: "Headers",
    timeoutSeconds: "Timeout (seconds)",
    maxParallelCalls: "Max parallel calls",
    lastError: "Last error",
  },
  placeholders: {
    name: "e.g. chrome-mcp",
    description: "Optional one-line summary",
    command: "e.g. npx",
    args: "One argument per line",
    url: "e.g. http://127.0.0.1:12306/mcp",
    timeoutSeconds: "e.g. 30",
    maxParallelCalls: "e.g. 4",
  },
  hints: {
    args: "Each line becomes one argument; blank lines are ignored.",
    env: "Process environment for stdio; blank rows are ignored, deleting every row clears env.",
    urlEnv:
      "Env vars without the HEADER_ prefix are kept as-is; HEADER_<Name> entries appear in the headers section below.",
    headers:
      "Stored in env as HEADER_<Name>; deleting every row clears the existing headers.",
    timeoutSeconds: "Leave empty to use the runtime default timeout.",
    maxParallelCalls: "Leave empty to use the runtime default limit.",
  },
  kv: {
    addRow: "Add row",
    removeRow: "Remove row",
    keyPlaceholder: "Key",
    valuePlaceholder: "Value",
    keyRequired: "Key is required.",
    duplicateKey: "Duplicate key.",
    pasteHint:
      "Paste multiple lines (e.g. A=1 and B=2) to split them into rows automatically.",
  },
  types: {
    stdio: "stdio (local process)",
    sse: "SSE",
    websocket: "WebSocket",
    streamable: "Streamable HTTP",
  },
  trustLevels: {
    inherit:
      "Default (stdio → local, everything else → untrusted_remote)",
    defaultShort: "Default",
    local: "local",
    trusted_remote: "trusted_remote",
    untrusted_remote: "untrusted_remote",
  },
  status: {
    connected: "Connected",
    disconnected: "Disconnected",
    disabled: "Disabled",
  },
  form: {
    createTitle: "Add MCP",
    editTitle: "Edit MCP {{name}}",
  },
  actions: {
    refresh: "Refresh",
    reload: "Hot reload",
    add: "Add MCP",
    tools: "Tools",
    edit: "Edit",
    delete: "Delete",
    save: "Save",
    cancel: "Cancel",
    close: "Close",
    retry: "Retry",
    enable: "Enable",
    disable: "Disable",
  },
  tools: {
    title: "Tools · {{name}}",
    dialogAriaLabel: "Tools of MCP \"{{name}}\"",
    loading: "Loading tools…",
    loadFailed: "Failed to load tools",
    empty:
      "This MCP exposes no tool right now: it may be disabled, disconnected, or has not completed the tools/list handshake yet.",
    disabled: "Disabled",
    notExposed: "Not exposed",
    unhealthy: "Unhealthy",
    unhealthyHint:
      "The runtime health check is failing; this tool may not be callable.",
    toggleOn: "Enabled",
    toggleOff: "Disabled",
    toggleAriaLabel: "Enable switch for tool {{tool}}",
    enableAll: "Enable all",
    disableAll: "Disable all",
    actionFailed: "Tool operation failed",
    schema: "inputSchema",
  },
  messages: {
    loadFailed: "Failed to load the MCP list",
    loading: "Loading MCP servers…",
    empty: "No MCP server is configured yet.",
    createSuccess: "Added MCP \"{{name}}\".",
    updateSuccess: "Updated MCP \"{{name}}\".",
    deleteSuccess: "Deleted MCP \"{{name}}\".",
    confirmDelete:
      "Delete MCP \"{{name}}\"? This writes to the runtime config and removes it from the registry.",
    enableSuccess: "Enabled MCP \"{{name}}\".",
    disableSuccess: "Disabled MCP \"{{name}}\".",
    reloadSuccess: "MCP hot reload triggered.",
    operationFailed: "Operation failed",
    validationNameRequired: "Name is required.",
    validationCommandRequired: "A stdio server requires a command.",
    validationUrlRequired:
      "Remote transports (sse/websocket/streamable) require a URL.",
    validationTimeoutInvalid:
      "Timeout must be a positive integer number of seconds, or empty.",
    validationMaxParallelCallsInvalid:
      "Max parallel calls must be a positive integer, or empty.",
    validationDuplicateKey: "Duplicate keys found; fix them before saving.",
  },
} satisfies DeepStringShape<typeof zhRuntimeConfigMcp>;
