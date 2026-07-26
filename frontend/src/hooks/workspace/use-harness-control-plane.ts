import { useCallback, useEffect, useState } from "react";

import {
  appendHarnessMemory,
  getHarnessGrants,
  getHarnessMemory,
  getHarnessPermissions,
  getHarnessPlugins,
  updateHarnessGrants,
  updateHarnessPlugin,
  type RuntimeHarnessGrant,
  type RuntimeHarnessGrantsUpdateRequest,
  type RuntimeHarnessMemoryNote,
  type RuntimeHarnessPermissionsResponse,
  type RuntimeHarnessPlugin,
  type RuntimeHarnessPluginAction,
} from "@/lib/runtime-api";

type UseHarnessControlPlaneOptions = {
  workspacePath?: string;
};

function errorMessage(error: unknown, fallback: string) {
  return error instanceof Error && error.message.trim()
    ? error.message
    : fallback;
}

export function useHarnessControlPlane({
  workspacePath,
}: UseHarnessControlPlaneOptions) {
  const [permissions, setPermissions] =
    useState<RuntimeHarnessPermissionsResponse | null>(null);
  const [grants, setGrants] = useState<RuntimeHarnessGrant[]>([]);
  const [grantsStorePath, setGrantsStorePath] = useState("");
  const [memoryNotes, setMemoryNotes] = useState<RuntimeHarnessMemoryNote[]>(
    [],
  );
  const [memoryQuery, setMemoryQuery] = useState("");
  const [plugins, setPlugins] = useState<RuntimeHarnessPlugin[]>([]);
  const [loading, setLoading] = useState(false);
  const [actionPending, setActionPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const resolvedWorkspace = workspacePath?.trim() || "";

  const reload = useCallback(
    async (query = memoryQuery) => {
      if (!resolvedWorkspace) {
        setPermissions(null);
        setGrants([]);
        setGrantsStorePath("");
        setMemoryNotes([]);
        setPlugins([]);
        setError(null);
        return;
      }

      setLoading(true);
      setError(null);

      try {
        const [permissionsResponse, grantsResponse, memoryResponse, pluginsResponse] =
          await Promise.all([
            getHarnessPermissions(resolvedWorkspace),
            getHarnessGrants(resolvedWorkspace),
            getHarnessMemory(resolvedWorkspace, {
              q: query.trim() || undefined,
              limit: 50,
            }),
            getHarnessPlugins(resolvedWorkspace),
          ]);

        setPermissions(permissionsResponse);
        setGrants(grantsResponse.grants ?? []);
        setGrantsStorePath(grantsResponse.store_path ?? "");
        setMemoryNotes(
          query.trim()
            ? (memoryResponse.hits ?? [])
            : (memoryResponse.notes ?? memoryResponse.hits ?? []),
        );
        setPlugins(pluginsResponse.plugins ?? []);
      } catch (loadError) {
        setError(
          errorMessage(loadError, "failed to load harness control plane"),
        );
      } finally {
        setLoading(false);
      }
    },
    [memoryQuery, resolvedWorkspace],
  );

  useEffect(() => {
    void reload();
  }, [reload]);

  const rememberGrant = useCallback(
    async (input: {
      tool: string;
      pattern?: string;
      scope?: string;
    }) => {
      if (!resolvedWorkspace) {
        return;
      }
      setActionPending(true);
      setError(null);
      try {
        const request: RuntimeHarnessGrantsUpdateRequest = {
          action: "remember",
          tool: input.tool,
          pattern: input.pattern,
          scope: input.scope || "project",
          workspace_path: resolvedWorkspace,
        };
        const response = await updateHarnessGrants(request, resolvedWorkspace);
        setGrants(response.grants ?? []);
        setGrantsStorePath(response.store_path ?? "");
      } catch (actionError) {
        setError(errorMessage(actionError, "failed to remember grant"));
        throw actionError;
      } finally {
        setActionPending(false);
      }
    },
    [resolvedWorkspace],
  );

  const revokeGrant = useCallback(
    async (input: { tool: string; pattern?: string }) => {
      if (!resolvedWorkspace) {
        return;
      }
      setActionPending(true);
      setError(null);
      try {
        const response = await updateHarnessGrants(
          {
            action: "revoke",
            tool: input.tool,
            pattern: input.pattern,
            match_empty_pattern: !input.pattern,
            workspace_path: resolvedWorkspace,
          },
          resolvedWorkspace,
        );
        setGrants(response.grants ?? []);
        setGrantsStorePath(response.store_path ?? "");
      } catch (actionError) {
        setError(errorMessage(actionError, "failed to revoke grant"));
        throw actionError;
      } finally {
        setActionPending(false);
      }
    },
    [resolvedWorkspace],
  );

  const searchMemory = useCallback(
    async (query: string) => {
      setMemoryQuery(query);
      if (!resolvedWorkspace) {
        return;
      }
      setLoading(true);
      setError(null);
      try {
        const response = await getHarnessMemory(resolvedWorkspace, {
          q: query.trim() || undefined,
          limit: 50,
        });
        setMemoryNotes(
          query.trim()
            ? (response.hits ?? [])
            : (response.notes ?? response.hits ?? []),
        );
      } catch (actionError) {
        setError(errorMessage(actionError, "failed to search memory"));
      } finally {
        setLoading(false);
      }
    },
    [resolvedWorkspace],
  );

  const appendMemory = useCallback(
    async (input: { text: string; tags?: string[] }) => {
      if (!resolvedWorkspace) {
        return;
      }
      setActionPending(true);
      setError(null);
      try {
        await appendHarnessMemory(
          {
            text: input.text,
            tags: input.tags,
            source: "settings",
            workspace_path: resolvedWorkspace,
          },
          resolvedWorkspace,
        );
        await searchMemory(memoryQuery);
      } catch (actionError) {
        setError(errorMessage(actionError, "failed to append memory note"));
        throw actionError;
      } finally {
        setActionPending(false);
      }
    },
    [memoryQuery, resolvedWorkspace, searchMemory],
  );

  const updatePlugin = useCallback(
    async (pluginId: string, action: RuntimeHarnessPluginAction) => {
      if (!resolvedWorkspace) {
        return;
      }
      setActionPending(true);
      setError(null);
      try {
        const response = await updateHarnessPlugin(
          pluginId,
          {
            action,
            workspace_path: resolvedWorkspace,
          },
          resolvedWorkspace,
        );
        if (response.plugins?.length) {
          setPlugins(response.plugins);
        } else if (response.plugin) {
          setPlugins((current) =>
            current.map((plugin) =>
              plugin.id === response.plugin?.id ? response.plugin! : plugin,
            ),
          );
        } else {
          const refreshed = await getHarnessPlugins(resolvedWorkspace);
          setPlugins(refreshed.plugins ?? []);
        }
      } catch (actionError) {
        setError(errorMessage(actionError, "failed to update plugin"));
        throw actionError;
      } finally {
        setActionPending(false);
      }
    },
    [resolvedWorkspace],
  );

  return {
    actionPending,
    appendMemory,
    error,
    grants,
    grantsStorePath,
    loading,
    memoryNotes,
    memoryQuery,
    permissions,
    plugins,
    reload,
    rememberGrant,
    revokeGrant,
    searchMemory,
    setMemoryQuery,
    updatePlugin,
    workspacePath: resolvedWorkspace,
  };
}
