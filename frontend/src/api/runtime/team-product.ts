import type {
  RuntimeCreateTeamDefinitionRequest,
  RuntimeCreateTeamRunDraftOptions,
  RuntimeCreateTeamRunDraftRequest,
  RuntimeStartTeamRunOptions,
  RuntimeStartTeamRunRequest,
  RuntimeTeamDefinitionResponse,
  RuntimeTeamDefinitionsResponse,
  RuntimeTeamProductCapabilities,
  RuntimeTeamProductListQuery,
  RuntimeTeamRunResponse,
  RuntimeTeamRunPreflightResponse,
  RuntimeTeamRunsResponse,
  RuntimeUpdateTeamDefinitionRequest,
} from "@/types/runtime";

import {
  buildRuntimeUrl,
  buildRuntimeUrlWithQuery,
  fetchRuntimeJson,
} from "./shared";

const jsonMutationHeaders = {
  Accept: "application/json",
  "Content-Type": "application/json",
};

/**
 * Product Definition/Run resources deliberately have a separate client facade
 * from legacy `/teams` records. Lifecycle methods are added only when the
 * Runtime exposes a verified operation contract.
 */
export async function listRuntimeTeamDefinitions(
  query: RuntimeTeamProductListQuery = {},
): Promise<RuntimeTeamDefinitionsResponse> {
  return fetchRuntimeJson<RuntimeTeamDefinitionsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/team-definitions", {
      limit: query.limit,
    }),
    {
      headers: { Accept: "application/json" },
      signal: query.signal,
    },
  );
}

export async function createRuntimeTeamDefinition(
  request: RuntimeCreateTeamDefinitionRequest,
  options: { signal?: AbortSignal } = {},
): Promise<RuntimeTeamDefinitionResponse> {
  return fetchRuntimeJson<RuntimeTeamDefinitionResponse>(
    buildRuntimeUrl("/api/runtime/team-definitions"),
    {
      method: "POST",
      headers: jsonMutationHeaders,
      body: JSON.stringify({
        name: request.name,
        description: request.description,
        config: request.config,
      }),
      signal: options.signal,
    },
  );
}

export async function getRuntimeTeamDefinition(
  definitionId: string,
  options: { signal?: AbortSignal } = {},
): Promise<RuntimeTeamDefinitionResponse> {
  return fetchRuntimeJson<RuntimeTeamDefinitionResponse>(
    buildRuntimeUrl(
      `/api/runtime/team-definitions/${encodeURIComponent(definitionId)}`,
    ),
    {
      headers: { Accept: "application/json" },
      signal: options.signal,
    },
  );
}

export async function updateRuntimeTeamDefinition(
  definitionId: string,
  request: RuntimeUpdateTeamDefinitionRequest,
  options: { signal?: AbortSignal } = {},
): Promise<RuntimeTeamDefinitionResponse> {
  return fetchRuntimeJson<RuntimeTeamDefinitionResponse>(
    buildRuntimeUrl(
      `/api/runtime/team-definitions/${encodeURIComponent(definitionId)}`,
    ),
    {
      method: "PATCH",
      headers: jsonMutationHeaders,
      body: JSON.stringify({
        expected_version: request.expected_version,
        name: request.name,
        description: request.description,
        config: request.config,
      }),
      signal: options.signal,
    },
  );
}

export async function listRuntimeTeamRuns(
  query: RuntimeTeamProductListQuery = {},
): Promise<RuntimeTeamRunsResponse> {
  return fetchRuntimeJson<RuntimeTeamRunsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/team-runs", {
      limit: query.limit,
    }),
    {
      headers: { Accept: "application/json" },
      signal: query.signal,
    },
  );
}

export async function createRuntimeTeamRunDraft(
  request: RuntimeCreateTeamRunDraftRequest,
  options: RuntimeCreateTeamRunDraftOptions,
): Promise<RuntimeTeamRunResponse> {
  const idempotencyKey = options.idempotencyKey.trim();
  if (!idempotencyKey) {
    throw new TypeError("idempotencyKey is required to create a Team Run draft");
  }
  return fetchRuntimeJson<RuntimeTeamRunResponse>(
    buildRuntimeUrl("/api/runtime/team-runs"),
    {
      method: "POST",
      headers: {
        ...jsonMutationHeaders,
        "Idempotency-Key": idempotencyKey,
      },
      body: JSON.stringify({
        definition_id: request.definition_id,
        expected_definition_version: request.expected_definition_version,
        goal: request.goal,
        config: request.config,
      }),
      signal: options.signal,
    },
  );
}

export async function getRuntimeTeamRun(
  runId: string,
  options: { signal?: AbortSignal } = {},
): Promise<RuntimeTeamRunResponse> {
  return fetchRuntimeJson<RuntimeTeamRunResponse>(
    buildRuntimeUrl(`/api/runtime/team-runs/${encodeURIComponent(runId)}`),
    {
      headers: { Accept: "application/json" },
      signal: options.signal,
    },
  );
}

export async function getRuntimeTeamRunPreflight(
  runId: string,
  options: { signal?: AbortSignal } = {},
): Promise<RuntimeTeamRunPreflightResponse> {
  return fetchRuntimeJson<RuntimeTeamRunPreflightResponse>(
    buildRuntimeUrl(
      `/api/runtime/team-runs/${encodeURIComponent(runId)}/preflight`,
    ),
    {
      headers: { Accept: "application/json" },
      signal: options.signal,
    },
  );
}

export async function startRuntimeTeamRun(
  runId: string,
  request: RuntimeStartTeamRunRequest,
  options: RuntimeStartTeamRunOptions,
): Promise<RuntimeTeamRunResponse> {
  const idempotencyKey = options.idempotencyKey.trim();
  if (!idempotencyKey) {
    throw new TypeError("idempotencyKey is required to start a Team Run");
  }
  return fetchRuntimeJson<RuntimeTeamRunResponse>(
    buildRuntimeUrl(
      `/api/runtime/team-runs/${encodeURIComponent(runId)}/start`,
    ),
    {
      method: "POST",
      headers: {
        ...jsonMutationHeaders,
        "Idempotency-Key": idempotencyKey,
      },
      body: JSON.stringify({
        expected_version: request.expected_version,
      }),
      signal: options.signal,
    },
  );
}

export function isRuntimeTeamProductCapabilitySupported(
  capabilities: RuntimeTeamProductCapabilities | undefined,
  capability: string,
): boolean {
  return capabilities?.[capability] === "supported";
}
