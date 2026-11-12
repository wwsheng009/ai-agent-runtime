import type { RuntimeMemorySummary } from "@/types/runtime";

import { fetchRuntimeJson } from "./shared";
import { readAdminToken } from "@/lib/admin-token";

function memoryURL(
  pathname: string,
  workspacePath?: string,
  extra: Record<string, string | number | boolean | undefined> = {},
) {
  const params = new URLSearchParams();
  if (workspacePath?.trim()) params.set("workspace_path", workspacePath.trim());
  for (const [key, value] of Object.entries(extra)) {
    if (value !== undefined) params.set(key, String(value));
  }
  const query = params.toString();
  return query ? `${pathname}?${query}` : pathname;
}

function adminHeaders(headers: Record<string, string> = {}) {
  const token = readAdminToken();
  if (token) headers["X-Skills-Admin-Token"] = token;
  return headers;
}

export async function activateMemorySummary(
  summaryId: string,
  workspacePath?: string,
): Promise<RuntimeMemorySummary> {
  return fetchRuntimeJson<RuntimeMemorySummary>(
    memoryURL(
      `/api/runtime/memory/summaries/${encodeURIComponent(summaryId)}/activate`,
      workspacePath,
    ),
    { method: "POST", headers: adminHeaders({ Accept: "application/json" }) },
  );
}

export async function purgeMemoryTurnWindows(
  workspacePath: string,
  options: { sessionId?: string; all?: boolean },
): Promise<{
  ok: boolean;
  action: string;
  workspace_path: string;
  scope: string;
  session_id?: string;
  windows_deleted: number;
  digests_released: number;
}> {
  const sessionId = options.sessionId?.trim() || "";
  return fetchRuntimeJson(memoryURL("/api/runtime/memory/turn-windows/purge", workspacePath), {
    method: "POST",
    headers: adminHeaders({ Accept: "application/json", "Content-Type": "application/json" }),
    body: JSON.stringify({
      confirm: true,
      workspace_path: workspacePath,
      session_id: sessionId,
      all: options.all === true && sessionId === "",
    }),
  });
}

export async function setMemorySessionMode(
  sessionId: string,
  mode: "enabled" | "disabled" | "polluted",
  workspacePath?: string,
): Promise<{ ok: boolean; session_id: string; mode: string }> {
  return fetchRuntimeJson(
    memoryURL(
      `/api/runtime/memory/sessions/${encodeURIComponent(sessionId)}/mode`,
      workspacePath,
    ),
    {
      method: "POST",
      headers: adminHeaders({ Accept: "application/json", "Content-Type": "application/json" }),
      body: JSON.stringify({ workspace_path: workspacePath, mode }),
    },
  );
}
