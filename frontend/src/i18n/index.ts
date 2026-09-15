import i18next, {
  createInstance,
  type i18n as I18nInstance,
} from "i18next";
import { initReactI18next } from "react-i18next";

import {
  collectMissingKeys,
  createFallbackReporter,
  createMissingKeyHandler,
  type FallbackReporter,
} from "./fallback";
import { installLanguageCodeCache } from "./language-code-cache";
import type { ResolvedLocale } from "./locale";
import { defaultNS, resources } from "./resources";

// P0-6：i18n 装配。zh-CN 为 key 真源，en-US 缺失键经 `fallbackLng` 回退 zh，
// 语言包全部缺失时由 `parseMissingKeyHandler` 回退 key 文本并在 dev 下告警。

const supportedLocales: readonly ResolvedLocale[] = ["zh-CN", "en-US"];
const fallbackLocale: ResolvedLocale = "zh-CN";

export const fallbackReporter: FallbackReporter = createFallbackReporter();

function isSupportedLocale(language: string): language is ResolvedLocale {
  return (supportedLocales as readonly string[]).includes(language);
}

// HTML `lang` 静态为 en；运行时按实际 locale 回写（含语言切换）。
export function applyDocumentLang(language: string): void {
  if (typeof document === "undefined" || !isSupportedLocale(language)) {
    return;
  }
  document.documentElement.lang = language;
}

function buildInitOptions(
  initialLocale: ResolvedLocale,
  reporter: FallbackReporter,
) {
  return {
    lng: initialLocale,
    fallbackLng: fallbackLocale,
    supportedLngs: [...supportedLocales],
    defaultNS,
    ns: Object.keys(resources["zh-CN"]),
    resources,
    interpolation: {
      escapeValue: false,
    },
    react: {
      useSuspense: false,
    },
    ...(reporter.enabled
      ? { parseMissingKeyHandler: createMissingKeyHandler(reporter) }
      : {}),
  };
}

// 独立实例（测试 / 非 React 场景）：不注册 react 插件，避免替换全局默认实例。
export async function createI18nInstance(
  initialLocale: ResolvedLocale,
  options: { reporter?: FallbackReporter } = {},
): Promise<I18nInstance> {
  const reporter = options.reporter ?? createFallbackReporter();
  const instance = createInstance();
  instance.on("languageChanged", (language) => applyDocumentLang(language));
  await instance.init(buildInitOptions(initialLocale, reporter));
  installLanguageCodeCache(instance);
  return instance;
}

export function initI18n(initialLocale: ResolvedLocale) {
  if (i18next.isInitialized) {
    if (i18next.language !== initialLocale) {
      void i18next.changeLanguage(initialLocale);
    }
    applyDocumentLang(initialLocale);
    return i18next;
  }

  void i18next
    .use(initReactI18next)
    .init(buildInitOptions(initialLocale, fallbackReporter));
  installLanguageCodeCache(i18next);
  i18next.on("languageChanged", (language) => applyDocumentLang(language));
  applyDocumentLang(initialLocale);

  return i18next;
}

export { i18next as i18n };
export { collectMissingKeys, createFallbackReporter };
export type { FallbackNotice, FallbackReporter } from "./fallback";
export { loadNamespaces, namespaceKeys, preloadNamespaces } from "./loaders";
export type { NamespaceKey, NamespaceLoaderTable } from "./loaders";
