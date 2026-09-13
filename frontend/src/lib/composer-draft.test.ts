import { describe, expect, it } from "vitest";

import {
  COMPOSER_DRAFT_STORAGE_PREFIX,
  clearComposerDraft,
  composerDraftStorageKey,
  readComposerDraft,
  resolveComposerDraftThreadKey,
  writeComposerDraft,
  type ComposerDraftStorage,
} from "./composer-draft";

function createMemoryStorage(initial: Record<string, string> = {}) {
  const map = new Map(Object.entries(initial));
  const storage: ComposerDraftStorage = {
    getItem: (key) => map.get(key) ?? null,
    removeItem: (key) => {
      map.delete(key);
    },
    setItem: (key, value) => {
      map.set(key, value);
    },
  };
  return { map, storage };
}

describe("resolveComposerDraftThreadKey", () => {
  it("prefers the bound runtime sessionId over the thread id", () => {
    expect(
      resolveComposerDraftThreadKey({ id: "thread-1", sessionId: "s-1" }),
    ).toBe("s-1");
  });

  it("falls back to the thread id for draft threads", () => {
    expect(resolveComposerDraftThreadKey({ id: "new" })).toBe("new");
    expect(resolveComposerDraftThreadKey({ id: "  thread-2  " })).toBe(
      "thread-2",
    );
  });

  it("returns null when neither key is usable", () => {
    expect(resolveComposerDraftThreadKey(undefined)).toBeNull();
    expect(resolveComposerDraftThreadKey({ id: "   ", sessionId: "  " })).toBeNull();
  });
});

describe("composer draft storage", () => {
  it("round-trips a draft per thread key", () => {
    const { storage } = createMemoryStorage();

    writeComposerDraft(storage, "s-1", "hello draft");
    writeComposerDraft(storage, "s-2", "other draft");

    expect(readComposerDraft(storage, "s-1")).toBe("hello draft");
    expect(readComposerDraft(storage, "s-2")).toBe("other draft");
    expect(readComposerDraft(storage, "s-3")).toBe("");
  });

  it("namespaces entries under the versioned prefix", () => {
    const { map, storage } = createMemoryStorage();

    writeComposerDraft(storage, "s-1", "value");

    expect(map.get(composerDraftStorageKey("s-1"))).toBe("value");
    expect(composerDraftStorageKey("s-1")).toBe(
      `${COMPOSER_DRAFT_STORAGE_PREFIX}s-1`,
    );
  });

  it("treats blank drafts as removal so clears leave no empty keys", () => {
    const { map, storage } = createMemoryStorage();

    writeComposerDraft(storage, "s-1", "value");
    writeComposerDraft(storage, "s-1", "   ");

    expect(readComposerDraft(storage, "s-1")).toBe("");
    expect(map.size).toBe(0);

    writeComposerDraft(storage, "s-1", "value again");
    clearComposerDraft(storage, "s-1");
    expect(map.size).toBe(0);
  });

  it("no-ops without a storage or thread key", () => {
    const { storage } = createMemoryStorage();

    expect(readComposerDraft(null, "s-1")).toBe("");
    expect(() => writeComposerDraft(storage, null, "value")).not.toThrow();
    expect(() => clearComposerDraft(undefined, "s-1")).not.toThrow();
    expect(readComposerDraft(storage, null)).toBe("");
  });

  it("degrades silently when the storage throws", () => {
    const throwing: ComposerDraftStorage = {
      getItem: () => {
        throw new Error("denied");
      },
      removeItem: () => {
        throw new Error("denied");
      },
      setItem: () => {
        throw new Error("quota exceeded");
      },
    };

    expect(readComposerDraft(throwing, "s-1")).toBe("");
    expect(() => writeComposerDraft(throwing, "s-1", "value")).not.toThrow();
    expect(() => clearComposerDraft(throwing, "s-1")).not.toThrow();
  });
});
