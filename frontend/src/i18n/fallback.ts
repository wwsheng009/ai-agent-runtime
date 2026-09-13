import type { i18n as I18nInstance } from "i18next";

// P0-6：运行时回退链（en 缺失键 → zh → key 文本）与 dev 告警。
// 回退本身由 i18next 的 `fallbackLng: "zh-CN"` 承担；本模块提供：
//   ① 命中所有语言都缺失时的 `parseMissingKeyHandler`（返回 key 文本而非空串），并在 dev 下报告；
//   ② 动态加载 namespace 后的「回退缺口」审计（目标语言相对 zh 真源缺哪些键）。

export type FallbackNotice =
  | {
      kind: "missing-key";
      locale: string;
      namespace: string | undefined;
      key: string;
    }
  | {
      kind: "namespace-gap";
      locale: string;
      namespace: string;
      referenceLocale: string;
      keys: string[];
    };

export type FallbackSink = (notice: FallbackNotice) => void;

export interface FallbackReporter {
  readonly enabled: boolean;
  readonly notices: readonly FallbackNotice[];
  report(notice: FallbackNotice): void;
  clear(): void;
}

const MAX_KEYS_IN_SUMMARY = 5;

const defaultSink: FallbackSink = (notice) => {
  if (notice.kind === "missing-key") {
    console.warn(
      `[i18n] 所有语言均缺失键，已回退 key 文本：ns=${notice.namespace ?? "-"} key=${notice.key}`,
    );
    return;
  }
  const preview = notice.keys.slice(0, MAX_KEYS_IN_SUMMARY).join(", ");
  const suffix = notice.keys.length > MAX_KEYS_IN_SUMMARY ? " …" : "";
  console.warn(
    `[i18n] ${notice.locale} 的 ${notice.namespace} 相对 ${notice.referenceLocale} 缺失 ${notice.keys.length} 个键（回退 ${notice.referenceLocale}）：${preview}${suffix}`,
  );
};

export function createFallbackReporter(
  options: { enabled?: boolean; sink?: FallbackSink } = {},
): FallbackReporter {
  const enabled = options.enabled ?? isDevEnvironment();
  const sink = options.sink ?? defaultSink;
  const notices: FallbackNotice[] = [];
  return {
    enabled,
    notices,
    report(notice) {
      if (!enabled) {
        return;
      }
      notices.push(notice);
      sink(notice);
    },
    clear() {
      notices.length = 0;
    },
  };
}

export function isDevEnvironment(): boolean {
  return Boolean(import.meta.env?.DEV);
}

// i18next 在所有语言都查不到键时调用；返回 key 文本保证 UI 不出现空白。
export function createMissingKeyHandler(reporter: FallbackReporter) {
  return (
    key: string,
    _defaultValue?: string,
    options?: { ns?: string | readonly string[]; lng?: string },
  ): string => {
    const nsOption = options?.ns;
    const namespace =
      typeof nsOption === "string" ? nsOption : nsOption?.[0];
    reporter.report({
      kind: "missing-key",
      locale: options?.lng ?? "unknown",
      namespace,
      key,
    });
    return key;
  };
}

export function installFallbackHandlers(
  instance: I18nInstance,
  reporter: FallbackReporter,
): void {
  if (!reporter.enabled) {
    return;
  }
  instance.options.parseMissingKeyHandler = createMissingKeyHandler(reporter);
}

// 递归比较 zh（key 真源）与目标语言 bundle：返回目标语言缺失或类型不符的键路径。
export function collectMissingKeys(
  reference: unknown,
  target: unknown,
  prefix = "",
): string[] {
  if (typeof reference === "string") {
    return typeof target === "string" ? [] : [prefix];
  }
  if (reference === null || typeof reference !== "object") {
    return [];
  }
  const missing: string[] = [];
  for (const [key, value] of Object.entries(reference as Record<string, unknown>)) {
    const path = prefix ? `${prefix}.${key}` : key;
    const targetValue = (target as Record<string, unknown> | null | undefined)?.[key];
    if (targetValue === undefined) {
      missing.push(path);
      continue;
    }
    missing.push(...collectMissingKeys(value, targetValue, path));
  }
  return missing;
}
