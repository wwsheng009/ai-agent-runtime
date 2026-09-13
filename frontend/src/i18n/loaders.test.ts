import type { i18n as I18nInstance } from "i18next";
import { describe, expect, it, vi } from "vitest";

import { collectMissingKeys, createFallbackReporter } from "./fallback";
import {
  loadNamespaces,
  namespaceKeys,
  namespaceLoaders,
  preloadNamespaces,
  type NamespaceLoaderTable,
} from "./loaders";

const locales = ["zh-CN", "en-US"] as const;

describe("namespaceLoaders", () => {
  it("两种语言覆盖全部 namespace 且 bundle 非空", async () => {
    for (const locale of locales) {
      for (const namespace of namespaceKeys) {
        const bundle = await namespaceLoaders[locale][namespace]();
        expect(Object.keys(bundle).length, `${locale}:${namespace}`).toBeGreaterThan(0);
      }
    }
  });

  it("zh/en 键集合逐 namespace 双向一致（静态对齐的运行期复核）", async () => {
    for (const namespace of namespaceKeys) {
      const zh = await namespaceLoaders["zh-CN"][namespace]();
      const en = await namespaceLoaders["en-US"][namespace]();
      expect(collectMissingKeys(zh, en), `en 缺少 ${namespace} 键`).toEqual([]);
      expect(collectMissingKeys(en, zh), `zh 多余 ${namespace} 键`).toEqual([]);
    }
  });
});

describe("loadNamespaces", () => {
  const instance = () =>
    ({ addResourceBundle: vi.fn() }) as unknown as I18nInstance;

  it("把目标语言 bundle 深合并进实例，并对缺口发告警", async () => {
    const reporter = createFallbackReporter({ enabled: true, sink: () => {} });
    const target = instance();
    const loaders = {
      "zh-CN": {
        ...namespaceLoaders["zh-CN"],
        common: async () => ({ a: "x", b: "y" }),
      },
      "en-US": {
        ...namespaceLoaders["en-US"],
        common: async () => ({ a: "x" }),
      },
    } satisfies NamespaceLoaderTable;

    await loadNamespaces("en-US", ["common"], target, { loaders, reporter });

    expect(target.addResourceBundle).toHaveBeenCalledWith(
      "en-US",
      "common",
      { a: "x" },
      true,
      true,
    );
    expect(reporter.notices).toEqual([
      {
        kind: "namespace-gap",
        locale: "en-US",
        namespace: "common",
        referenceLocale: "zh-CN",
        keys: ["b"],
      },
    ]);
  });

  it("真源语言加载不触发缺口审计", async () => {
    const reporter = createFallbackReporter({ enabled: true, sink: () => {} });
    await preloadNamespaces("zh-CN", instance(), { reporter });
    expect(reporter.notices).toHaveLength(0);
  });

  it("reporter 关闭时不加载真源语言，也不产生告警", async () => {
    const reporter = createFallbackReporter({ enabled: false, sink: () => {} });
    const target = instance();
    await loadNamespaces("en-US", ["logs"], target, { reporter });
    expect(target.addResourceBundle).toHaveBeenCalledTimes(1);
    expect(reporter.notices).toHaveLength(0);
  });
});
