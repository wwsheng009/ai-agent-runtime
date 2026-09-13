import { describe, expect, it, vi } from "vitest";

import {
  collectMissingKeys,
  createFallbackReporter,
  createMissingKeyHandler,
} from "./fallback";

describe("collectMissingKeys", () => {
  it("缺失子树只报根路径，类型不符报到叶子", () => {
    const reference = { a: "x", b: { c: "y", d: { e: "z" } } };
    const target = { a: "x", b: { c: 1 } };
    // b.d 整棵缺失 → 只报 "b.d"（避免逐键刷屏）；b.c 类型不符 → 报到叶子。
    expect(collectMissingKeys(reference, target)).toEqual(["b.c", "b.d"]);
  });

  it("嵌套类型不符时继续下钻", () => {
    const reference = { b: { d: { e: "z", f: "w" } } };
    expect(collectMissingKeys(reference, { b: { d: { e: 1 } } })).toEqual([
      "b.d.e",
      "b.d.f",
    ]);
  });

  it("形状一致时返回空", () => {
    const reference = { a: "x", b: { c: "y" } };
    expect(collectMissingKeys(reference, { a: "A", b: { c: "C" } })).toEqual([]);
  });
});

describe("createFallbackReporter", () => {
  it("enabled=false 时不记录也不调用 sink", () => {
    const sink = vi.fn();
    const reporter = createFallbackReporter({ enabled: false, sink });
    reporter.report({
      kind: "missing-key",
      locale: "en-US",
      namespace: "workspace",
      key: "shell.title",
    });
    expect(sink).not.toHaveBeenCalled();
    expect(reporter.notices).toHaveLength(0);
  });

  it("enabled=true 时记录并透传 sink", () => {
    const sink = vi.fn();
    const reporter = createFallbackReporter({ enabled: true, sink });
    const notice = {
      kind: "namespace-gap" as const,
      locale: "en-US",
      namespace: "workspace",
      referenceLocale: "zh-CN",
      keys: ["a", "b"],
    };
    reporter.report(notice);
    expect(sink).toHaveBeenCalledWith(notice);
    expect(reporter.notices).toEqual([notice]);
    reporter.clear();
    expect(reporter.notices).toHaveLength(0);
  });
});

describe("createMissingKeyHandler", () => {
  it("返回 key 文本并报告命名空间与语言", () => {
    const reporter = createFallbackReporter({ enabled: true, sink: () => {} });
    const handler = createMissingKeyHandler(reporter);
    expect(handler("shell.title", undefined, { ns: ["workspace"], lng: "en-US" })).toBe(
      "shell.title",
    );
    expect(reporter.notices).toEqual([
      {
        kind: "missing-key",
        locale: "en-US",
        namespace: "workspace",
        key: "shell.title",
      },
    ]);
  });

  it("兼容 ns 为字符串与缺省的情况", () => {
    const reporter = createFallbackReporter({ enabled: true, sink: () => {} });
    const handler = createMissingKeyHandler(reporter);
    expect(handler("a.b", undefined, { ns: "common" })).toBe("a.b");
    expect(handler("c.d")).toBe("c.d");
    expect(reporter.notices).toEqual([
      { kind: "missing-key", locale: "unknown", namespace: "common", key: "a.b" },
      { kind: "missing-key", locale: "unknown", namespace: undefined, key: "c.d" },
    ]);
  });
});
