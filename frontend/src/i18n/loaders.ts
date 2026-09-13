import type { i18n as I18nInstance } from "i18next";

import { collectMissingKeys, type FallbackReporter } from "./fallback";
import type { ResolvedLocale } from "./locale";
import { resources } from "./resources";

// P0-6：语言包按 namespace 动态 import。
// 首屏仍由 `initI18n` 同步注入全部 namespace（避免闪烁与缺键）；这里的动态入口用于：
//   ① 语言切换 / 后续大体积 namespace 懒加载时按需补包；
//   ② 加载后做「回退缺口」审计（目标语言相对 zh 真源缺键 → dev 告警）。

export type NamespaceKey =
  | "common"
  | "landing"
  | "workspace"
  | "runtimeConfig"
  | "settings"
  | "logs"
  | "usageAnalytics";

export type NamespaceBundle = Record<string, unknown>;
export type NamespaceLoader = () => Promise<NamespaceBundle>;
export type NamespaceLoaderTable = Record<
  ResolvedLocale,
  Record<NamespaceKey, NamespaceLoader>
>;

// 与 `resources` 的真源保持一致；新增 namespace 时由类型系统提示补齐 loader。
export const namespaceKeys = Object.keys(resources["zh-CN"]) as NamespaceKey[];

export const namespaceLoaders: NamespaceLoaderTable = {
  "zh-CN": {
    common: async () => (await import("./resources/zh-CN/common")).zhCommon,
    landing: async () => (await import("./resources/zh-CN/landing")).zhLanding,
    workspace: async () => (await import("./resources/zh-CN/workspace")).zhWorkspace,
    runtimeConfig: async () =>
      (await import("./resources/zh-CN/runtime-config")).zhRuntimeConfig,
    settings: async () => (await import("./resources/zh-CN/settings")).zhSettings,
    logs: async () => (await import("./resources/zh-CN/logs")).zhLogs,
    usageAnalytics: async () =>
      (await import("./resources/zh-CN/usage-analytics")).zhUsageAnalytics,
  },
  "en-US": {
    common: async () => (await import("./resources/en-US/common")).enCommon,
    landing: async () => (await import("./resources/en-US/landing")).enLanding,
    workspace: async () => (await import("./resources/en-US/workspace")).enWorkspace,
    runtimeConfig: async () =>
      (await import("./resources/en-US/runtime-config")).enRuntimeConfig,
    settings: async () => (await import("./resources/en-US/settings")).enSettings,
    logs: async () => (await import("./resources/en-US/logs")).enLogs,
    usageAnalytics: async () =>
      (await import("./resources/en-US/usage-analytics")).enUsageAnalytics,
  },
};

export interface LoadNamespacesOptions {
  loaders?: NamespaceLoaderTable;
  reporter?: FallbackReporter;
  referenceLocale?: ResolvedLocale;
}

export async function loadNamespaces(
  locale: ResolvedLocale,
  namespaces: readonly NamespaceKey[],
  instance: I18nInstance,
  options: LoadNamespacesOptions = {},
): Promise<void> {
  const loaders = options.loaders ?? namespaceLoaders;
  const reporter = options.reporter;
  const referenceLocale = options.referenceLocale ?? "zh-CN";

  await Promise.all(
    namespaces.map(async (namespace) => {
      const bundle = await loaders[locale][namespace]();
      instance.addResourceBundle(locale, namespace, bundle, true, true);

      if (!reporter?.enabled || locale === referenceLocale) {
        return;
      }
      const reference = await loaders[referenceLocale][namespace]();
      const missing = collectMissingKeys(reference, bundle);
      if (missing.length > 0) {
        reporter.report({
          kind: "namespace-gap",
          locale,
          namespace,
          referenceLocale,
          keys: missing,
        });
      }
    }),
  );
}

export async function preloadNamespaces(
  locale: ResolvedLocale,
  instance: I18nInstance,
  options: LoadNamespacesOptions = {},
): Promise<void> {
  await loadNamespaces(locale, namespaceKeys, instance, options);
}
