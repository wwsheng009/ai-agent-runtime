// 「会话 MCP」面单测共享夹具（非测试文件）：
// entry 构造 + 带 scope 的后端响应构造，供 surface 的多个测试文件复用。

import type { RuntimeMcpEntry } from "@/types/runtime";

export const SESSION_ID = "session-mcp-1";

export function buildEntry(
  name: string,
  options: { globalEnabled?: boolean; connected?: boolean; toolCount?: number } = {},
): RuntimeMcpEntry {
  const globalEnabled = options.globalEnabled ?? true;
  return {
    config: {
      name,
      type: "stdio",
      command: "npx",
      args: [],
      enabled: globalEnabled,
      disabled: !globalEnabled,
    },
    status: {
      name,
      type: "stdio",
      enabled: globalEnabled,
      connected: options.connected ?? globalEnabled,
      toolCount: options.toolCount ?? 3,
    },
  };
}

export function buildScopedPayload(
  options: {
    entries?: Array<
      ReturnType<typeof buildEntry> & {
        source?: "workspace" | "global";
        session_enabled?: boolean;
        session_disabled?: boolean;
      }
    >;
    fallback?: boolean;
    enabled?: string[];
    disabled?: string[];
  } = {},
) {
  const entries = options.entries ?? [];
  const workspaceFile = "E:/ws/.aicli/mcp.yaml";
  return {
    session_id: SESSION_ID,
    disabled: options.disabled ?? ([] as string[]),
    enabled: options.enabled ?? [],
    count: 0,
    scope: {
      workspace: "E:/ws",
      workspace_scoped: true,
      workspace_fallback: options.fallback ?? false,
      read: { path: workspaceFile, source: "project", exists: true },
      write: { path: workspaceFile, source: "project", exists: true },
    },
    mcps: entries,
    summary: {
      total: entries.length,
      enabled: entries.length,
      disabled: 0,
      connected: entries.length,
      tools: entries.length * 3,
    },
  };
}
