import type { i18n as I18nInstance } from "i18next";

// i18next 的语言标签规范化记忆化。
//
// 背景（生产构建 CPU profile 实测）：`LanguageUtils.formatLanguageCode` 对每个含 "-"
// 的语言标签都会调用 `Intl.getCanonicalLocales`（Node 下实测 ~2.6µs/次，是
// `String.prototype.toLowerCase` 的 ~175 倍）；而 `Translator.resolve` 在每次 `t()`
// 调用中都会走 `toResolveHierarchy`，仅当前语言 + fallback 就触发 2 次。
// 工作区一次流式回复期间每帧有上百次 `t()`（探针实测 ~175 次/帧），累计后它成为
// 生产构建里最热的**非 idle** 函数（占非 idle 采样约 10%）。
//
// 该函数是纯函数：输出只取决于入参标签与 init 时的静态选项（`lowerCaseLng` /
// `cleanCode` / `load`），不随 `changeLanguage` 变化，因此可以安全按标签记忆化。
// 可缓存的语言标签数量是有限且稳定的（zh-CN / en-US / zh / en / en-US 变体等）。

/** 记忆化标记：重复安装（HMR / 多次 init）时用于幂等短路。 */
const MEMO_MARKER = "__aiAgentRuntimeLanguageCodeMemoized";

/** 缓存上限。超出即整体清空（标签集合极小，不会频繁触发）。 */
const MAX_CACHED_CODES = 64;

type LanguageUtilsLike = {
  formatLanguageCode?: ((code: string) => string) & {
    [MEMO_MARKER]?: boolean;
  };
};

/**
 * 给实例的语言标签规范化加一层缓存。
 *
 * @returns 是否已就绪（服务尚未创建或该方法不存在时返回 false，调用方可稍后重试）。
 */
export function memoizeLanguageCodeFormatting(instance: I18nInstance): boolean {
  const languageUtils = (instance?.services as { languageUtils?: LanguageUtilsLike })
    ?.languageUtils;
  const original = languageUtils?.formatLanguageCode;
  if (!languageUtils || typeof original !== "function") {
    return false;
  }
  if (original[MEMO_MARKER]) {
    return true;
  }

  const cache = new Map<string, string>();
  const memoized = ((code: string) => {
    // 非字符串入参（i18next 内部会做 isString 判断）不进入缓存，直接透传。
    if (typeof code !== "string") {
      return original.call(languageUtils, code);
    }

    const cached = cache.get(code);
    if (cached !== undefined) {
      return cached;
    }

    const formatted = original.call(languageUtils, code);
    if (cache.size >= MAX_CACHED_CODES) {
      cache.clear();
    }
    cache.set(code, formatted);
    return formatted;
  }) as LanguageUtilsLike["formatLanguageCode"];

  if (!memoized) {
    return false;
  }
  memoized[MEMO_MARKER] = true;
  languageUtils.formatLanguageCode = memoized;
  return true;
}

/**
 * 安装记忆化；服务尚未就绪时挂到 `initialized` 事件上重试。
 */
export function installLanguageCodeCache(instance: I18nInstance): void {
  if (memoizeLanguageCodeFormatting(instance)) {
    return;
  }

  instance.on("initialized", () => {
    memoizeLanguageCodeFormatting(instance);
  });
}
