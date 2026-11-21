import { useCallback, useEffect, useRef, useState } from "react";

import {
  listRuntimeMcps,
  listRuntimeSessionMcps,
  listRuntimeWorkspaceMcps,
} from "@/api/runtime/mcp";
import type {
  RuntimeSessionMcpEntry,
  RuntimeSessionMcpScope,
  RuntimeWorkspaceMcpListResponse,
} from "@/types/runtime";

export type SessionMcpLoadStatus = "loading" | "ready" | "error";

export function formatMcpError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function sessionScopeFromWorkspace(
  response: RuntimeWorkspaceMcpListResponse,
): RuntimeSessionMcpScope {
  return {
    workspace: response.workspace_path,
    workspace_scoped: true,
    workspace_fallback: response.workspace_fallback,
    read: response.read,
    write: response.write,
    candidates: response.candidates,
  };
}

type SessionMcpDataOptions = {
  sessionId: string;
  workspacePath?: string;
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
};

export function useSessionMcpSurfaceData({
  sessionId,
  workspacePath,
  lastRuntimeEventType,
  runtimeEventCount,
}: SessionMcpDataOptions) {
  const [entries, setEntries] = useState<RuntimeSessionMcpEntry[]>([]);
  const [disabledNames, setDisabledNames] = useState<string[]>([]);
  /** 「本会话临时启用」名单（配置停用 + 会话私有内存连接）。 */
  const [tempEnabledNames, setTempEnabledNames] = useState<string[]>([]);
  /** 会话生效的配置面（新后端返回；旧后端缺省 → 隐藏持久化管理入口）。 */
  const [scope, setScope] = useState<RuntimeSessionMcpScope | null>(null);
  const [status, setStatus] = useState<SessionMcpLoadStatus>("loading");
  const [error, setError] = useState("");

  const refresh = useCallback(async () => {
    const normalizedSessionId = sessionId.trim();
    const normalizedWorkspacePath = workspacePath?.trim() ?? "";
    setEntries([]);
    setScope(null);
    setStatus("loading");
    setError("");

    if (normalizedWorkspacePath && !normalizedSessionId) {
      try {
        const workspace = await listRuntimeWorkspaceMcps(normalizedWorkspacePath);
        setDisabledNames([]);
        setTempEnabledNames([]);
        setEntries(workspace.mcps);
        setScope(sessionScopeFromWorkspace(workspace));
        setStatus("ready");
      } catch (loadError) {
        setDisabledNames([]);
        setTempEnabledNames([]);
        setError(formatMcpError(loadError));
        setStatus("error");
      }
      return;
    }

    if (normalizedWorkspacePath && normalizedSessionId) {
      try {
        const [workspace, scoped] = await Promise.all([
          listRuntimeWorkspaceMcps(normalizedWorkspacePath),
          listRuntimeSessionMcps(normalizedSessionId),
        ]);
        setDisabledNames(scoped.disabled);
        setTempEnabledNames(scoped.enabled ?? []);
        setScope(sessionScopeFromWorkspace(workspace));

        const sessionEntries = scoped.mcps ?? [];
        const sessionEntriesByName = new Map(
          sessionEntries.map((entry) => [
            entry.config?.name?.trim() || entry.status?.name?.trim() || "",
            entry,
          ]),
        );
        const workspaceNames = new Set(
          workspace.mcps
            .map((entry) => entry.config?.name?.trim() ?? "")
            .filter(Boolean),
        );
        const workspaceEntries = workspace.mcps.map((entry) => {
          const name = entry.config?.name?.trim() ?? "";
          const sessionEntry = sessionEntriesByName.get(name);
          return {
            ...entry,
            ...sessionEntry,
            source: "workspace" as const,
          };
        });
        // The session API may expose process-global entries while the workspace
        // has no local file. Keep them visible for session-only controls, but
        // mark them global so workspace persistence actions stay unavailable.
        const globalFallbackEntries = sessionEntries.filter((entry) => {
          const name =
            entry.config?.name?.trim() || entry.status?.name?.trim() || "";
          return entry.source === "global" && !workspaceNames.has(name);
        });
        setEntries([...workspaceEntries, ...globalFallbackEntries]);
        setStatus("ready");
      } catch (loadError) {
        setDisabledNames([]);
        setTempEnabledNames([]);
        setError(formatMcpError(loadError));
        setStatus("error");
      }
      return;
    }

    if (!normalizedSessionId) {
      setDisabledNames([]);
      setTempEnabledNames([]);
      setStatus("ready");
      return;
    }

    try {
      const scoped = await listRuntimeSessionMcps(normalizedSessionId);
      setDisabledNames(scoped.disabled);
      setTempEnabledNames(scoped.enabled ?? []);
      setScope(scoped.scope ?? null);
      if (scoped.mcps) {
        // 新后端：条目即会话实际生效的配置面（工作区锚定）。
        setEntries(scoped.mcps);
      } else {
        // 旧后端只返回覆盖清单：回退全局列表，行为与旧版一致。
        const global = await listRuntimeMcps();
        setEntries(global.mcps);
      }
      setStatus("ready");
    } catch (loadError) {
      setError(formatMcpError(loadError));
      setStatus("error");
    }
  }, [sessionId, workspacePath]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // `mcp.*` 运行时事件 → 重载；首帧只记录基线，不额外触发一次请求。
  const lastEventRef = useRef<{ count?: number }>({
    count: runtimeEventCount,
  });
  useEffect(() => {
    const previousCount = lastEventRef.current.count;
    lastEventRef.current = { count: runtimeEventCount };
    if (runtimeEventCount === undefined || previousCount === undefined) {
      return;
    }
    if (runtimeEventCount === previousCount) {
      return;
    }
    if (!(lastRuntimeEventType ?? "").startsWith("mcp")) {
      return;
    }
    void refresh();
  }, [lastRuntimeEventType, refresh, runtimeEventCount]);

  return {
    entries,
    disabledNames,
    setDisabledNames,
    tempEnabledNames,
    setTempEnabledNames,
    scope,
    status,
    error,
    setError,
    refresh,
  };
}