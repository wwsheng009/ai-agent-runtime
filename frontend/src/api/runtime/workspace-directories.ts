import type {
  RuntimeCreateWorkspaceDirectoryRequest,
  RuntimeDeleteWorkspaceDirectoryResponse,
  RuntimeUpdateWorkspaceDirectoryRequest,
  RuntimeWorkspaceDirectory,
  RuntimeWorkspaceDirectoriesResponse,
} from "@/types/runtime";

import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

export async function listWorkspaceDirectories(): Promise<RuntimeWorkspaceDirectoriesResponse> {
  return fetchRuntimeJson<RuntimeWorkspaceDirectoriesResponse>(
    buildRuntimeUrl("/api/runtime/workspace-directories"),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

/** Registers an existing server directory. Duplicates are idempotent. */
export async function createWorkspaceDirectory(
  request: RuntimeCreateWorkspaceDirectoryRequest,
): Promise<{ directory: RuntimeWorkspaceDirectory; existing?: boolean }> {
  return fetchRuntimeJson<{
    directory: RuntimeWorkspaceDirectory;
    existing?: boolean;
  }>(buildRuntimeUrl("/api/runtime/workspace-directories"), {
    method: "POST",
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    body: JSON.stringify(request),
  });
}

/** Only the display alias is mutable; paths are immutable once registered. */
export async function updateWorkspaceDirectory(
  directoryId: string,
  request: RuntimeUpdateWorkspaceDirectoryRequest,
): Promise<{ directory: RuntimeWorkspaceDirectory }> {
  return fetchRuntimeJson<{ directory: RuntimeWorkspaceDirectory }>(
    buildRuntimeUrl(
      `/api/runtime/workspace-directories/${encodeURIComponent(directoryId)}`,
    ),
    {
      method: "PATCH",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
}

/** Drops the registry entry only; files and bound sessions are untouched. */
export async function deleteWorkspaceDirectory(
  directoryId: string,
): Promise<RuntimeDeleteWorkspaceDirectoryResponse> {
  return fetchRuntimeJson<RuntimeDeleteWorkspaceDirectoryResponse>(
    buildRuntimeUrl(
      `/api/runtime/workspace-directories/${encodeURIComponent(directoryId)}`,
    ),
    {
      method: "DELETE",
      headers: {
        Accept: "application/json",
      },
    },
  );
}
