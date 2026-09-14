// P2-7 子片 3：`/model` 候选组装单测（纯函数，无 DOM / 无网络）。
//
// 覆盖口径：
// - 目录未就绪（null / 无 provider / 无模型 / 空模型名）→ 空集合，不补占位项；
// - 顺序与原文：provider 与模型名保持目录顺序与原文（不翻译、不排序、不截断）；
// - 去重口径：组内去重；命令候选按模型 id 跨 provider 去重（保留首条），
//   校验集合与候选同口径。

import { describe, expect, it } from "vitest";

import type { RuntimeModelsResponse } from "@/types/runtime";

import {
  composerModelCatalogGroups,
  composerModelCommandOptions,
  composerModelIds,
} from "./composer-model-options";

function catalog(
  providers: RuntimeModelsResponse["providers"],
  overrides: Partial<RuntimeModelsResponse> = {},
): RuntimeModelsResponse {
  return {
    providers,
    count: providers.length,
    default_provider: providers[0]?.name,
    default_model: providers[0]?.default_model,
    ...overrides,
  };
}

const twoProviders = catalog([
  {
    name: "deepseek",
    default_model: "deepseek-chat",
    models: ["deepseek-chat", "deepseek-reasoner"],
  },
  { name: "openai", models: ["gpt-5", "gpt-5-mini"] },
]);

describe("composerModelCatalogGroups", () => {
  it("目录未就绪时为空（不伪造默认模型）", () => {
    expect(composerModelCatalogGroups(null)).toEqual([]);
    expect(composerModelCatalogGroups(undefined)).toEqual([]);
    expect(composerModelCatalogGroups(catalog([]))).toEqual([]);
  });

  it("按 provider 分组并保持目录顺序与原文", () => {
    expect(composerModelCatalogGroups(twoProviders)).toEqual([
      { provider: "deepseek", models: ["deepseek-chat", "deepseek-reasoner"] },
      { provider: "openai", models: ["gpt-5", "gpt-5-mini"] },
    ]);
  });

  it("空 provider 与空模型名都不产出条目", () => {
    const groups = composerModelCatalogGroups(
      catalog([
        { name: "empty-provider", models: [] },
        { name: "blank-models", models: ["", "   "] },
        { name: "usable", models: [" m1 ", "m1"] },
      ]),
    );
    // 模型名不做 trim：只过滤纯空白项，`" m1 "` 与 `"m1"` 是两条不同原文。
    expect(groups).toEqual([{ provider: "usable", models: [" m1 ", "m1"] }]);
  });

  it("组内重复模型只保留首条（保持首次出现的位置）", () => {
    const groups = composerModelCatalogGroups(
      catalog([{ name: "p", models: ["a", "b", "a"] }]),
    );
    expect(groups).toEqual([{ provider: "p", models: ["a", "b"] }]);
  });
});

describe("composerModelCommandOptions", () => {
  it("候选的 value 是模型 id，description 是所属 provider", () => {
    expect(composerModelCommandOptions(twoProviders)).toEqual([
      {
        value: "deepseek-chat",
        label: "deepseek-chat",
        description: "deepseek",
      },
      {
        value: "deepseek-reasoner",
        label: "deepseek-reasoner",
        description: "deepseek",
      },
      { value: "gpt-5", label: "gpt-5", description: "openai" },
      { value: "gpt-5-mini", label: "gpt-5-mini", description: "openai" },
    ]);
  });

  it("跨 provider 的同名模型只保留首条（候选身份是模型 id）", () => {
    const options = composerModelCommandOptions(
      catalog([
        { name: "gateway", models: ["shared-model"] },
        { name: "direct", models: ["shared-model", "own-model"] },
      ]),
    );
    expect(options).toEqual([
      { value: "shared-model", label: "shared-model", description: "gateway" },
      { value: "own-model", label: "own-model", description: "direct" },
    ]);
  });
});

describe("composerModelIds", () => {
  it("与候选同口径：跨 provider 去重后的全部模型 id", () => {
    expect(
      composerModelIds(
        catalog([
          { name: "gateway", models: ["shared-model", "a"] },
          { name: "direct", models: ["shared-model", "b"] },
        ]),
      ),
    ).toEqual(["shared-model", "a", "b"]);
  });

  it("目录未就绪时为空（调用方据此走「目录未就绪」而非「模型不存在」）", () => {
    expect(composerModelIds(null)).toEqual([]);
    expect(composerModelIds(catalog([{ name: "p", models: [] }]))).toEqual([]);
  });
});
