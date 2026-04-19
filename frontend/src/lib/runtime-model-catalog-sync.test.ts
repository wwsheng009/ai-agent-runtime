// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";

import {
  notifyRuntimeModelCatalogChanged,
  subscribeRuntimeModelCatalogInvalidation,
} from "@/lib/runtime-model-catalog-sync";

describe("runtime model catalog sync", () => {
  it("notifies current document subscribers immediately", () => {
    const listener = vi.fn();
    const unsubscribe = subscribeRuntimeModelCatalogInvalidation(listener);

    notifyRuntimeModelCatalogChanged();

    expect(listener).toHaveBeenCalledTimes(1);
    unsubscribe();
  });
});
