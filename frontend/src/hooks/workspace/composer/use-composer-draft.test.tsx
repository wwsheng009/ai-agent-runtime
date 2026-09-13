// @vitest-environment jsdom

import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import {
  composerDraftStorageKey,
  type ComposerDraftStorage,
} from "@/lib/composer-draft";

import {
  useComposerDraft,
  type ComposerDraftController,
  type UseComposerDraftOptions,
} from "./use-composer-draft";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function createMemoryStorage() {
  const map = new Map<string, string>();
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

describe("useComposerDraft", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let controller: ComposerDraftController | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    controller = null;
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderDraft(options: UseComposerDraftOptions) {
    function DraftProbe(props: UseComposerDraftOptions) {
      const next = useComposerDraft(props);
      useEffect(() => {
        controller = next;
      });
      return null;
    }
    act(() => root?.render(<DraftProbe {...options} />));
    return controller as ComposerDraftController;
  }

  it("starts from the persisted draft of the active session", () => {
    const { map, storage } = createMemoryStorage();
    map.set(composerDraftStorageKey("s-1"), "restored draft");

    const draft = renderDraft({ thread: { sessionId: "s-1" }, storage });

    expect(draft.draft).toBe("restored draft");
    expect(draft.threadKey).toBe("s-1");
  });

  it("writes every edit through to storage keyed by the session", () => {
    const { map, storage } = createMemoryStorage();
    const draft = renderDraft({ thread: { sessionId: "s-1" }, storage });

    act(() => draft.setDraft("hello"));
    expect(map.get(composerDraftStorageKey("s-1"))).toBe("hello");

    act(() => draft.setDraft(""));
    expect(map.has(composerDraftStorageKey("s-1"))).toBe(false);
  });

  it("restores each session draft independently across switches", () => {
    const { storage } = createMemoryStorage();
    const draftA = renderDraft({ thread: { sessionId: "s-1" }, storage });
    act(() => draftA.setDraft("draft one"));

    const draftB = renderDraft({ thread: { sessionId: "s-2" }, storage });
    expect(draftB.draft).toBe("");
    act(() => draftB.setDraft("draft two"));

    const backToA = renderDraft({ thread: { sessionId: "s-1" }, storage });
    expect(backToA.draft).toBe("draft one");

    const backToB = renderDraft({ thread: { sessionId: "s-2" }, storage });
    expect(backToB.draft).toBe("draft two");
  });

  it("keeps writing to the new session key right after a switch", () => {
    const { map, storage } = createMemoryStorage();
    const first = renderDraft({ thread: { id: "new" }, storage });
    act(() => first.setDraft("unsent"));

    const second = renderDraft({ thread: { sessionId: "s-9" }, storage });
    act(() => second.setDraft("typed after switch"));

    expect(map.get(composerDraftStorageKey("s-9"))).toBe("typed after switch");
    expect(map.get(composerDraftStorageKey("new"))).toBe("unsent");
  });

  it("degrades to an in-memory draft when storage is unavailable", () => {
    const draft = renderDraft({ thread: { id: "new" }, storage: null });

    expect(draft.draft).toBe("");
    act(() => draft.setDraft("still editable"));
    expect(controller?.draft).toBe("still editable");
  });
});
