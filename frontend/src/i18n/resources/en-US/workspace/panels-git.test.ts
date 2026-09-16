// zh-CN 为真源：en-US 必须同结构（键集合一致），且同一键的占位符集合一致（漏 {{count}} 会在英文下丢数值）。
//
// 说明：这里只核对结构与占位符，不评价译文质量；注册表接线由父批次负责，因此测试只读两个模块。

import { describe, expect, it } from "vitest";

import { enWorkspacePanelsGit } from "@/i18n/resources/en-US/workspace/panels-git";
import { zhWorkspacePanelsGit } from "@/i18n/resources/zh-CN/workspace/panels-git";

type Dict = { [key: string]: string | Dict };

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
  return [...text.matchAll(/\{\{\s*([a-zA-Z0-9_]+)\s*\}\}/g)].map((match) => match[1]).sort();
}

describe("panels-git i18n 模块", () => {
  const zh = flatten(zhWorkspacePanelsGit as unknown as Dict);
  const en = flatten(enWorkspacePanelsGit as unknown as Dict);

  it("键集合一致（含 panels.git.* 根前缀）", () => {
    const zhKeys = [...zh.keys()].sort();
    expect(zhKeys.length).toBeGreaterThan(50);
    expect(zhKeys).toContain("ariaLabel");
    expect(zhKeys).toContain("diff.truncatedTitle");
    expect(zhKeys).toContain("commits.loadMore");
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

  it("增删 / 截断 / 二进制 / 解析失败等诚实口径文案两语都显式存在", () => {
    const required = [
      "diff.parseErrorTitle",
      "diff.parseErrorBody",
      "diff.truncatedTitle",
      "diff.truncatedReason",
      "diff.truncatedNoReason",
      "diff.copyRaw",
      "diff.downloadPatch",
      "diff.binaryTitle",
      "diff.binaryBody",
      "diff.gapUnknown",
      "diff.noNewlineMarker",
      "diff.emptyNewFile",
      "diff.emptyDeletedFile",
      "list.statUnknown",
      "scope.missing",
    ];
    for (const key of required) {
      expect(zh.get(key), `zh 缺少 ${key}`).toBeTruthy();
      expect(en.get(key), `en 缺少 ${key}`).toBeTruthy();
    }
  });

  it("不把「无改动」写成解析失败 / 截断的兜底文案", () => {
    expect(zh.get("diff.noChanges")).not.toBe(zh.get("diff.parseErrorTitle"));
    expect(en.get("diff.noChanges")).not.toBe(en.get("diff.parseErrorTitle"));
    expect(zh.get("diff.noChanges")).not.toBe(zh.get("diff.truncatedTitle"));
  });
});
