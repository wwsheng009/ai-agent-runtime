// zh-CN 为真源：模型发现（provider ops）子树的 en-US 必须同结构（键集合一致），
// 且同一键的占位符集合一致（漏 {{count}} 会在英文下丢数值）。
//
// 说明：只覆盖本次新增的 editor.providers.models 子树（其余子树不在本测试范围），
// 只核对结构与占位符，不评价译文质量。

import { describe, expect, it } from "vitest";

import { enRuntimeConfigEditorProviders } from "@/i18n/resources/en-US/runtime-config/editor-providers";
import { zhRuntimeConfigEditorProviders } from "@/i18n/resources/zh-CN/runtime-config/editor-providers";

type Dict = { [key: string]: string | Dict };

function modelsSubtree(root: Dict): Dict {
  const providers = root.providers as Dict;
  return providers.models as Dict;
}

function flatten(value: Dict, prefix = ""): Map<string, string> {
  const flat = new Map<string, string>();
  for (const [key, entry] of Object.entries(value)) {
    const path = prefix === "" ? key : `${prefix}.${key}`;
    if (typeof entry === "string") {
      flat.set(path, entry);
    } else {
      for (const [nestedKey, nestedValue] of flatten(entry, path)) {
        flat.set(nestedKey, nestedValue);
      }
    }
  }
  return flat;
}

function placeholders(text: string): string[] {
  return [...text.matchAll(/\{\{\s*([a-zA-Z0-9_]+)\s*\}\}/g)]
    .map((match) => match[1])
    .sort();
}

describe("editor.providers.models i18n 子树", () => {
  const zh = flatten(
    modelsSubtree(zhRuntimeConfigEditorProviders as unknown as Dict),
  );
  const en = flatten(
    modelsSubtree(enRuntimeConfigEditorProviders as unknown as Dict),
  );

  it("键集合一致，且包含本次接入的 provider ops 文案", () => {
    const zhKeys = [...zh.keys()].sort();
    expect(zhKeys.length).toBeGreaterThan(20);
    for (const required of [
      "fetch",
      "fetching",
      "fetchSuccess",
      "fetchEmpty",
      "autoImport",
      "autoImportSuccess",
      "probe",
      "probeSummary",
      "mergeAssumed",
      "mergeVerified",
      "warningsSuffix",
    ]) {
      expect(zhKeys).toContain(required);
    }
    expect([...en.keys()].sort()).toEqual(zhKeys);
  });

  it("每个键的占位符集合一致，且译文非空", () => {
    const mismatched: string[] = [];
    const emptyValues: string[] = [];
    for (const [key, zhText] of zh) {
      const enText = en.get(key) ?? "";
      if (enText.trim() === "") {
        emptyValues.push(key);
      }
      if (placeholders(zhText).join(",") !== placeholders(enText).join(",")) {
        mismatched.push(key);
      }
    }
    expect(emptyValues).toEqual([]);
    expect(mismatched).toEqual([]);
  });
});
