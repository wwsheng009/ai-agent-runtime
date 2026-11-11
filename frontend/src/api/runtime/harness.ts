import type {
  RuntimeHarnessGrantsResponse,
  RuntimeHarnessGrantsUpdateRequest,
  RuntimeHarnessHooksResponse,
  RuntimeHarnessHookUpdateRequest,
  RuntimeHarnessMemoryAppendRequest,
  RuntimeHarnessMemoryResponse,
  RuntimeMemoryRecord,
  RuntimeMemoryRecordsResponse,
  RuntimeMemoryJobsResponse,
  RuntimeMemoryRecallTracesResponse,
  RuntimeMemoryStatus,
  RuntimeMemorySummariesResponse,
  RuntimeMemoryUsageResponse,
  RuntimeMemoryWhyNot,
  RuntimeHarnessPermissionsResponse,
  RuntimeHarnessPluginsResponse,
  RuntimeHarnessPluginUpdateRequest,
  RuntimeHarnessTrustRequest,
  RuntimeHarnessTrustResponse,
} from "@/types/runtime";

import {
  buildRuntimeUrlWithQuery,
  fetchRuntimeJson,
  parseErrorPayload,
  RuntimeApiError,
} from "./shared";
import { readAdminToken } from "@/lib/admin-token";

function withMemoryAdminHeaders(
  headers: Record<string, string> = {},
): Record<string, string> {
  const token = readAdminToken();
  if (token) {
    headers["X-Skills-Admin-Token"] = token;
  }
  return headers;
}

function withWorkspacePath(
  pathname: string,
  workspacePath?: string,
  extra: Record<string, string | number | boolean | undefined> = {},
) {
  return buildRuntimeUrlWithQuery(pathname, {
    workspace_path: workspacePath?.trim() || undefined,
    ...extra,
  });
}

export async function getHarnessPermissions(
  workspacePath?: string,
): Promise<RuntimeHarnessPermissionsResponse> {
  return fetchRuntimeJson<RuntimeHarnessPermissionsResponse>(
    withWorkspacePath("/api/runtime/harness/permissions", workspacePath),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function getHarnessGrants(
  workspacePath?: string,
): Promise<RuntimeHarnessGrantsResponse> {
  return fetchRuntimeJson<RuntimeHarnessGrantsResponse>(
    withWorkspacePath("/api/runtime/harness/grants", workspacePath),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

/**
 * D29 工作区信任结论（Batch 14）：读侧与 CLI `/trust`、启动摘要同源。
 * `feature_enabled && !trusted` 才意味着项目级 profile 的 prompts 被扣留。
 */
export async function getHarnessTrust(
  workspacePath?: string,
): Promise<RuntimeHarnessTrustResponse> {
  return fetchRuntimeJson<RuntimeHarnessTrustResponse>(
    withWorkspacePath("/api/runtime/harness/trust", workspacePath),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

/**
 * 一键信任（Q22）：显式确认后调用；后端持久化到 ~/.aicli/trusted_folders.yaml。
 * 只支持 grant——撤销信任是破坏性操作，走 CLI `/trust` 面，避免 UI 误触。
 */
export async function grantHarnessTrust(
  workspacePath?: string,
  request: RuntimeHarnessTrustRequest = {},
): Promise<RuntimeHarnessTrustResponse> {
  const resolvedWorkspace =
    request.workspace_path?.trim() || workspacePath?.trim() || undefined;
  return fetchRuntimeJson<RuntimeHarnessTrustResponse>(
    withWorkspacePath("/api/runtime/harness/trust", resolvedWorkspace),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        ...request,
        action: request.action ?? "grant",
        workspace_path: resolvedWorkspace,
      }),
    },
  );
}

export async function updateHarnessGrants(
  request: RuntimeHarnessGrantsUpdateRequest,
  workspacePath?: string,
): Promise<RuntimeHarnessGrantsResponse> {
  const resolvedWorkspace =
    request.workspace_path?.trim() || workspacePath?.trim() || undefined;
  return fetchRuntimeJson<RuntimeHarnessGrantsResponse>(
    withWorkspacePath("/api/runtime/harness/grants", resolvedWorkspace),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        ...request,
        workspace_path: resolvedWorkspace,
      }),
    },
  );
}

export async function getHarnessMemory(
  workspacePath?: string,
  query?: { q?: string; limit?: number },
): Promise<RuntimeHarnessMemoryResponse> {
  return fetchRuntimeJson<RuntimeHarnessMemoryResponse>(
    withWorkspacePath("/api/runtime/harness/memory", workspacePath, {
      q: query?.q,
      limit: query?.limit,
    }),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function appendHarnessMemory(
  request: RuntimeHarnessMemoryAppendRequest,
  workspacePath?: string,
): Promise<RuntimeHarnessMemoryResponse> {
  const resolvedWorkspace =
    request.workspace_path?.trim() || workspacePath?.trim() || undefined;
  return fetchRuntimeJson<RuntimeHarnessMemoryResponse>(
    withWorkspacePath("/api/runtime/harness/memory", resolvedWorkspace),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        ...request,
        workspace_path: resolvedWorkspace,
      }),
    },
  );
}

export async function getMemoryStatus(
  workspacePath?: string,
): Promise<RuntimeMemoryStatus> {
  return fetchRuntimeJson<RuntimeMemoryStatus>(
    withWorkspacePath("/api/runtime/memory/status", workspacePath),
    { headers: { Accept: "application/json" } },
  );
}

export async function getMemoryRecords(
  workspacePath?: string,
  query?: { status?: string; kind?: string; q?: string; limit?: number },
): Promise<RuntimeMemoryRecordsResponse> {
  return fetchRuntimeJson<RuntimeMemoryRecordsResponse>(
    withWorkspacePath("/api/runtime/memory/records", workspacePath, {
      status: query?.status,
      kind: query?.kind,
      q: query?.q,
      limit: query?.limit,
    }),
    { headers: { Accept: "application/json" } },
  );
}

export async function getMemorySummaries(
  workspacePath?: string,
  limit = 20,
): Promise<RuntimeMemorySummariesResponse> {
  return fetchRuntimeJson<RuntimeMemorySummariesResponse>(
    withWorkspacePath("/api/runtime/memory/summaries", workspacePath, { limit }),
    { headers: { Accept: "application/json" } },
  );
}

export async function getMemoryUsage(
  workspacePath?: string,
  query?: { memory_id?: string; session_id?: string; use_kind?: string; limit?: number },
): Promise<RuntimeMemoryUsageResponse> {
  return fetchRuntimeJson<RuntimeMemoryUsageResponse>(
    withWorkspacePath("/api/runtime/memory/usage", workspacePath, {
      memory_id: query?.memory_id,
      session_id: query?.session_id,
      use_kind: query?.use_kind,
      limit: query?.limit,
    }),
    { headers: withMemoryAdminHeaders({ Accept: "application/json" }) },
  );
}

export async function getMemoryJobs(
  workspacePath?: string,
  limit = 20,
): Promise<RuntimeMemoryJobsResponse> {
  return fetchRuntimeJson<RuntimeMemoryJobsResponse>(
    withWorkspacePath("/api/runtime/memory/jobs", workspacePath, { limit }),
    { headers: withMemoryAdminHeaders({ Accept: "application/json" }) },
  );
}

export async function getMemoryRecallTraces(
  workspacePath?: string,
  query?: { session_id?: string; limit?: number },
): Promise<RuntimeMemoryRecallTracesResponse> {
  return fetchRuntimeJson<RuntimeMemoryRecallTracesResponse>(
    withWorkspacePath("/api/runtime/memory/recall-traces", workspacePath, {
      session_id: query?.session_id,
      limit: query?.limit,
    }),
    { headers: withMemoryAdminHeaders({ Accept: "application/json" }) },
  );
}

export async function getMemoryWhyNot(
  workspacePath?: string,
  query?: { q: string; limit?: number; token_budget?: number; session_id?: string },
): Promise<RuntimeMemoryWhyNot> {
  return fetchRuntimeJson<RuntimeMemoryWhyNot>(
    withWorkspacePath("/api/runtime/memory/why-not", workspacePath, {
      q: query?.q,
      limit: query?.limit,
      token_budget: query?.token_budget,
      session_id: query?.session_id,
    }),
    { headers: withMemoryAdminHeaders({ Accept: "application/json" }) },
  );
}

export async function rebuildMemorySummaries(
  workspacePath?: string,
): Promise<{ workspace_path: string; rebuilt: number; revision?: number }> {
  return fetchRuntimeJson(
    withWorkspacePath("/api/runtime/memory/summaries/rebuild", workspacePath),
    {
      method: "POST",
      headers: withMemoryAdminHeaders({ Accept: "application/json", "Content-Type": "application/json" }),
      body: JSON.stringify({ workspace_path: workspacePath }),
    },
  );
}

export async function revalidateMemoryRecord(
  recordId: string,
  workspacePath?: string,
): Promise<RuntimeMemoryRecord> {
  return fetchRuntimeJson<RuntimeMemoryRecord>(
    withWorkspacePath(
      `/api/runtime/memory/records/${encodeURIComponent(recordId)}/revalidate`,
      workspacePath,
    ),
    { method: "POST", headers: withMemoryAdminHeaders({ Accept: "application/json" }) },
  );
}

export async function setMemoryRecordPinned(
  recordId: string,
  pinned: boolean,
  workspacePath?: string,
): Promise<RuntimeMemoryRecord> {
  return fetchRuntimeJson<RuntimeMemoryRecord>(
    withWorkspacePath(
      `/api/runtime/memory/records/${encodeURIComponent(recordId)}`,
      workspacePath,
    ),
    {
      method: "PATCH",
      headers: withMemoryAdminHeaders({ Accept: "application/json", "Content-Type": "application/json" }),
      body: JSON.stringify({ pinned, workspace_path: workspacePath }),
    },
  );
}

export async function restoreMemoryRecord(
  recordId: string,
  workspacePath?: string,
): Promise<RuntimeMemoryRecord> {
  return fetchRuntimeJson<RuntimeMemoryRecord>(
    withWorkspacePath(
      `/api/runtime/memory/records/${encodeURIComponent(recordId)}/restore`,
      workspacePath,
    ),
    { method: "POST", headers: withMemoryAdminHeaders({ Accept: "application/json" }) },
  );
}

export async function deleteMemoryRecord(
  recordId: string,
  workspacePath?: string,
): Promise<{ ok: boolean }> {
  return fetchRuntimeJson(
    withWorkspacePath(
      `/api/runtime/memory/records/${encodeURIComponent(recordId)}`,
      workspacePath,
    ),
    { method: "DELETE", headers: withMemoryAdminHeaders({ Accept: "application/json" }) },
  );
}

export async function resetMemory(
  workspacePath?: string,
): Promise<{ ok: boolean; action: string; workspace_path: string }> {
  return fetchRuntimeJson(
    withWorkspacePath("/api/runtime/memory/reset", workspacePath),
    {
      method: "POST",
      headers: withMemoryAdminHeaders({ Accept: "application/json", "Content-Type": "application/json" }),
      body: JSON.stringify({ confirm: true, workspace_path: workspacePath }),
    },
  );
}

export async function correctMemoryRecord(
  recordId: string,
  text: string,
  workspacePath?: string,
): Promise<RuntimeMemoryRecord> {
  return fetchRuntimeJson<RuntimeMemoryRecord>(
    withWorkspacePath(
      `/api/runtime/memory/records/${encodeURIComponent(recordId)}`,
      workspacePath,
    ),
    {
      method: "PATCH",
      headers: withMemoryAdminHeaders({ Accept: "application/json", "Content-Type": "application/json" }),
      body: JSON.stringify({ text, workspace_path: workspacePath }),
    },
  );
}

export function memoryExportUrl(
  workspacePath: string,
  format: "jsonl" | "markdown",
): string {
  return withWorkspacePath("/api/runtime/memory/export", workspacePath, { format });
}

/**
 * Export is an admin endpoint. `window.open` cannot attach the
 * X-Skills-Admin-Token header, so fetch the body with the same header set as
 * the other governance calls and hand the browser a short-lived blob URL.
 */
export async function downloadMemoryExport(
  workspacePath: string,
  format: "jsonl" | "markdown",
): Promise<void> {
  const response = await fetch(memoryExportUrl(workspacePath, format), {
    headers: withMemoryAdminHeaders({ Accept: format === "markdown" ? "text/markdown" : "application/jsonl" }),
  });
  if (!response.ok) {
    throw new RuntimeApiError(response.status, await parseErrorPayload(response));
  }
  const blob = await response.blob();
  const href = URL.createObjectURL(blob);
  try {
    const anchor = document.createElement("a");
    anchor.href = href;
    anchor.download = format === "markdown" ? "agent-memory.md" : "agent-memory.jsonl";
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
  } finally {
    URL.revokeObjectURL(href);
  }
}

export async function getHarnessPlugins(
  workspacePath?: string,
): Promise<RuntimeHarnessPluginsResponse> {
  return fetchRuntimeJson<RuntimeHarnessPluginsResponse>(
    withWorkspacePath("/api/runtime/harness/plugins", workspacePath),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function updateHarnessPlugin(
  pluginId: string,
  request: RuntimeHarnessPluginUpdateRequest = {},
  workspacePath?: string,
): Promise<RuntimeHarnessPluginsResponse> {
  const resolvedWorkspace =
    request.workspace_path?.trim() || workspacePath?.trim() || undefined;
  return fetchRuntimeJson<RuntimeHarnessPluginsResponse>(
    withWorkspacePath(
      `/api/runtime/harness/plugins/${encodeURIComponent(pluginId)}`,
      resolvedWorkspace,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        ...request,
        workspace_path: resolvedWorkspace,
      }),
    },
  );
}

export async function getHarnessHooks(
  workspacePath?: string,
): Promise<RuntimeHarnessHooksResponse> {
  return fetchRuntimeJson<RuntimeHarnessHooksResponse>(
    withWorkspacePath("/api/runtime/harness/hooks", workspacePath),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export async function updateHarnessHook(
  request: RuntimeHarnessHookUpdateRequest,
  workspacePath?: string,
): Promise<RuntimeHarnessHooksResponse> {
  const resolvedWorkspace =
    request.workspace_path?.trim() || workspacePath?.trim() || undefined;
  return fetchRuntimeJson<RuntimeHarnessHooksResponse>(
    withWorkspacePath("/api/runtime/harness/hooks", resolvedWorkspace),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        ...request,
        workspace_path: resolvedWorkspace,
      }),
    },
  );
}
