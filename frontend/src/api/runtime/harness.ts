import type {
  RuntimeHarnessGrantsResponse,
  RuntimeHarnessGrantsUpdateRequest,
  RuntimeHarnessMemoryAppendRequest,
  RuntimeHarnessMemoryResponse,
  RuntimeHarnessPermissionsResponse,
  RuntimeHarnessPluginsResponse,
  RuntimeHarnessPluginUpdateRequest,
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
