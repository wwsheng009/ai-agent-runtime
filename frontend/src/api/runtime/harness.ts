import type {
  RuntimeHarnessGrantsResponse,
  RuntimeHarnessGrantsUpdateRequest,
  RuntimeHarnessMemoryAppendRequest,
  RuntimeHarnessMemoryResponse,
  RuntimeHarnessPermissionsResponse,
  RuntimeHarnessPluginsResponse,
  RuntimeHarnessPluginUpdateRequest,
  RuntimeHarnessTrustRequest,
  RuntimeHarnessTrustResponse,
} from "@/types/runtime";

import {
  buildRuntimeUrlWithQuery,
  fetchRuntimeJson,
} from "./shared";

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
