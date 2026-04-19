import { describe, expect, it } from "vitest";

import type { RuntimeModelsResponse } from "@/types/runtime";
import {
  findProviderForModel,
  readStoredRuntimeModelSelection,
  resolveRuntimeModelSelection,
  writeStoredRuntimeModelSelection,
} from "@/hooks/workspace/use-runtime-model-catalog";

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

const runtimeModelsCatalog: RuntimeModelsResponse = {
  default_provider: "openai",
  default_model: "gpt-4o",
  providers: [
    {
      name: "openai",
      default_model: "gpt-4o",
      models: ["gpt-4.1", "gpt-4o"],
      model_count: 2,
    },
    {
      name: "anthropic",
      default_model: "claude-3-7-sonnet",
      models: ["claude-3-7-sonnet"],
      model_count: 1,
    },
  ],
  count: 3,
};

describe("runtime model catalog helpers", () => {
  it("resolves the provider from a stored model alias", () => {
    expect(findProviderForModel(runtimeModelsCatalog.providers, "gpt-4.1")).toBe(
      "openai",
    );
    expect(
      resolveRuntimeModelSelection(runtimeModelsCatalog, { model: "gpt-4.1" }),
    ).toEqual({
      provider: "openai",
      model: "gpt-4.1",
    });
  });

  it("falls back to the provider default model when the current model does not belong to that provider", () => {
    expect(
      resolveRuntimeModelSelection(runtimeModelsCatalog, {
        provider: "anthropic",
        model: "gpt-4o",
      }),
    ).toEqual({
      provider: "openai",
      model: "gpt-4o",
    });

    expect(
      resolveRuntimeModelSelection(runtimeModelsCatalog, {
        provider: "anthropic",
      }),
    ).toEqual({
      provider: "anthropic",
      model: "claude-3-7-sonnet",
    });
  });

  it("round-trips selection through storage", () => {
    const storage = new MemoryStorage();

    writeStoredRuntimeModelSelection(storage, {
      provider: "openai",
      model: "gpt-4o",
    });

    expect(readStoredRuntimeModelSelection(storage)).toEqual({
      provider: "openai",
      model: "gpt-4o",
    });
  });

  it("tolerates providers that return null model lists", () => {
    const unsafeCatalog = {
      ...runtimeModelsCatalog,
      providers: [
        {
          name: "broken-provider",
          default_model: "broken-default",
          models: null,
          model_count: 0,
        },
      ],
    } as unknown as RuntimeModelsResponse;

    expect(() =>
      resolveRuntimeModelSelection(unsafeCatalog, { provider: "broken-provider" }),
    ).not.toThrow();
    expect(
      resolveRuntimeModelSelection(unsafeCatalog, { provider: "broken-provider" }),
    ).toEqual({
      provider: "broken-provider",
      model: "broken-default",
    });
  });
});
