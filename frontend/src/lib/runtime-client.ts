import { useState } from "react";

import { getRuntimeBaseUrl } from "@/api/runtime/shared";

export const RUNTIME_CLIENT_STORAGE_KEY =
  "ai-agent-runtime.runtime.client";

export type RuntimeClientIdentity = {
  clientId: string;
  userId: string;
  workspacePath: string;
  workspaceScope: string;
};

type RuntimeClientIdentityOptions = {
  clientIdFactory?: () => string;
  scopeHint?: string;
  workspacePath?: string;
};

function getBrowserStorage() {
  if (typeof window === "undefined") {
    return null;
  }

  return window.localStorage;
}

function normalizeToken(value: string, fallback: string) {
  const normalized = value
    .trim()
    .toLowerCase()
    .replace(/^[a-z]+:\/\//, "")
    .replace(/[?#].*$/, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");

  return normalized || fallback;
}

export function normalizeRuntimeWorkspaceScope(value: string) {
  return normalizeToken(value, "default");
}

export function normalizeRuntimeClientId(value: string) {
  return normalizeToken(value, "client");
}

export function createRuntimeClientId() {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return normalizeRuntimeClientId(crypto.randomUUID());
  }

  return normalizeRuntimeClientId(
    `client-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`,
  );
}

export function readStoredRuntimeClientId(
  storage: Storage | null | undefined,
) {
  if (!storage) {
    return null;
  }

  try {
    const raw = storage.getItem(RUNTIME_CLIENT_STORAGE_KEY);
    if (!raw) {
      return null;
    }

    const normalized = normalizeRuntimeClientId(raw);
    return normalized === "client" ? null : normalized;
  } catch {
    return null;
  }
}

export function writeStoredRuntimeClientId(
  storage: Storage | null | undefined,
  clientId: string,
) {
  if (!storage) {
    return;
  }

  storage.setItem(RUNTIME_CLIENT_STORAGE_KEY, normalizeRuntimeClientId(clientId));
}

export function resetStoredRuntimeClientId(
  storage: Storage | null | undefined,
) {
  if (!storage) {
    return;
  }

  storage.removeItem(RUNTIME_CLIENT_STORAGE_KEY);
}

export function ensureStoredRuntimeClientId(
  storage: Storage | null | undefined,
  clientIdFactory: () => string = createRuntimeClientId,
) {
  const existing = readStoredRuntimeClientId(storage);
  if (existing) {
    return existing;
  }

  const next = normalizeRuntimeClientId(clientIdFactory());
  if (storage) {
    writeStoredRuntimeClientId(storage, next);
  }
  return next;
}

export function buildRuntimeConsoleUserId(
  workspaceScope: string,
  clientId: string,
) {
  return `web-console:${normalizeRuntimeWorkspaceScope(workspaceScope)}:${normalizeRuntimeClientId(clientId)}`;
}

export function resolveRuntimeWorkspaceScopeHint() {
  const configuredScope = import.meta.env.VITE_RUNTIME_WORKSPACE_SCOPE?.trim();
  if (configuredScope) {
    return configuredScope;
  }

  const runtimeBaseUrl = getRuntimeBaseUrl().trim();
  if (runtimeBaseUrl) {
    return runtimeBaseUrl;
  }

  if (typeof window !== "undefined" && window.location.origin) {
    return window.location.origin;
  }

  return "default";
}

export function resolveRuntimeWorkspacePath() {
  return import.meta.env.VITE_RUNTIME_WORKSPACE_PATH?.trim() ?? "";
}

export function getRuntimeClientIdentity(
  storage: Storage | null | undefined = getBrowserStorage(),
  options: RuntimeClientIdentityOptions = {},
): RuntimeClientIdentity {
  const workspaceScope = normalizeRuntimeWorkspaceScope(
    options.scopeHint ?? resolveRuntimeWorkspaceScopeHint(),
  );
  const clientId = ensureStoredRuntimeClientId(storage, options.clientIdFactory);
  const workspacePath =
    options.workspacePath?.trim() || resolveRuntimeWorkspacePath();

  return {
    clientId,
    userId: buildRuntimeConsoleUserId(workspaceScope, clientId),
    workspacePath,
    workspaceScope,
  };
}

export function useRuntimeClientIdentity() {
  const [identity] = useState(() => getRuntimeClientIdentity());
  return identity;
}
