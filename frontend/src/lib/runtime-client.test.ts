import { describe, expect, it } from "vitest";

import {
  RUNTIME_CLIENT_STORAGE_KEY,
  buildRuntimeConsoleUserId,
  ensureStoredRuntimeClientId,
  getRuntimeClientIdentity,
  normalizeRuntimeClientId,
  normalizeRuntimeWorkspaceScope,
  readStoredRuntimeClientId,
  resetStoredRuntimeClientId,
} from "@/lib/runtime-client";

class MemoryStorage implements Storage {
  private values = new Map<string, string>();

  get length() {
    return this.values.size;
  }

  clear() {
    this.values.clear();
  }

  getItem(key: string) {
    return this.values.get(key) ?? null;
  }

  key(index: number) {
    return [...this.values.keys()][index] ?? null;
  }

  removeItem(key: string) {
    this.values.delete(key);
  }

  setItem(key: string, value: string) {
    this.values.set(key, value);
  }
}

describe("runtime client identity helpers", () => {
  it("normalizes workspace scopes and client identifiers", () => {
    expect(normalizeRuntimeWorkspaceScope("https://127.0.0.1:8101/runtime")).toBe(
      "127-0-0-1-8101-runtime",
    );
    expect(normalizeRuntimeClientId("Client 01")).toBe("client-01");
  });

  it("persists and reuses a generated runtime client id", () => {
    const storage = new MemoryStorage();

    const first = ensureStoredRuntimeClientId(storage, () => "client-alpha");
    const second = ensureStoredRuntimeClientId(storage, () => "client-beta");

    expect(first).toBe("client-alpha");
    expect(second).toBe("client-alpha");
    expect(storage.getItem(RUNTIME_CLIENT_STORAGE_KEY)).toBe("client-alpha");
    expect(readStoredRuntimeClientId(storage)).toBe("client-alpha");
  });

  it("removes the stored runtime client id when reset is requested", () => {
    const storage = new MemoryStorage();
    storage.setItem(RUNTIME_CLIENT_STORAGE_KEY, "client-alpha");

    resetStoredRuntimeClientId(storage);

    expect(storage.getItem(RUNTIME_CLIENT_STORAGE_KEY)).toBeNull();
    expect(readStoredRuntimeClientId(storage)).toBeNull();
  });

  it("builds a scoped runtime console user id", () => {
    expect(buildRuntimeConsoleUserId("workspace-main", "client-42")).toBe(
      "web-console:workspace-main:client-42",
    );
  });

  it("derives runtime client identity from storage and provided scope hints", () => {
    const storage = new MemoryStorage();
    storage.setItem(RUNTIME_CLIENT_STORAGE_KEY, "client-zeta");

    const identity = getRuntimeClientIdentity(storage, {
      scopeHint: "http://localhost:8101/api",
      workspacePath: "E:/projects/ai/ai-agent-runtime",
    });

    expect(identity).toEqual({
      clientId: "client-zeta",
      userId: "web-console:localhost-8101-api:client-zeta",
      workspacePath: "E:/projects/ai/ai-agent-runtime",
      workspaceScope: "localhost-8101-api",
    });
  });
});
