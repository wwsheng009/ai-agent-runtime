import { describe, expect, it, vi } from "vitest";

import { createI18nInstance } from "./index";
import {
  installLanguageCodeCache,
  memoizeLanguageCodeFormatting,
} from "./language-code-cache";

// 真环境回归：`formatLanguageCode` 每次调用都会走 `Intl.getCanonicalLocales`
// （~2.6µs/次），而每次 `t()` 至少触发 2 次。这里保证：
//   1. 语义不变（语言标签规范化结果一致）；
//   2. 重复 `t()` 不再反复进入 `Intl`。

describe("语言标签规范化记忆化", () => {
  it("保持语言标签规范化语义", async () => {
    const instance = await createI18nInstance("zh-CN");
    const languageUtils = instance.services.languageUtils as {
      formatLanguageCode: (code: string) => string;
    };
    const original = languageUtils.formatLanguageCode;
    const samples = ["zh-CN", "en-US", "zh", "en", "en-us", "zh-hans-cn", "not a lang"];

    const before = samples.map((code) => original.call(languageUtils, code));
    expect(memoizeLanguageCodeFormatting(instance)).toBe(true);
    const after = samples.map((code) => languageUtils.formatLanguageCode(code));

    expect(after).toEqual(before);
    // 规范化本身仍然生效（大小写按 BCP-47 规则）。
    expect(languageUtils.formatLanguageCode("en-us")).toBe("en-US");
    expect(languageUtils.formatLanguageCode("zh")).toBe("zh");
  });

  it("重复 t() 时不再反复调用 Intl.getCanonicalLocales", async () => {
    const instance = await createI18nInstance("zh-CN");
    installLanguageCodeCache(instance);
    const canonicalSpy = vi.spyOn(Intl, "getCanonicalLocales");

    try {
      for (let index = 0; index < 50; index += 1) {
        instance.t("workspace:shell.newChatTitle");
      }

      // 未记忆化时：每轮约 2 次（当前语言 + fallback）× 50 轮。
      expect(canonicalSpy.mock.calls.length).toBeLessThanOrEqual(6);
    } finally {
      canonicalSpy.mockRestore();
    }
  });

  it("重复安装是幂等的", async () => {
    const instance = await createI18nInstance("zh-CN");
    const languageUtils = instance.services.languageUtils as {
      formatLanguageCode: (code: string) => string;
    };

    expect(memoizeLanguageCodeFormatting(instance)).toBe(true);
    const installed = languageUtils.formatLanguageCode;
    expect(memoizeLanguageCodeFormatting(instance)).toBe(true);
    expect(languageUtils.formatLanguageCode).toBe(installed);
  });

  it("服务尚未就绪时退化为挂载 initialized 事件", () => {
    const handlers: Array<() => void> = [];
    const fake = {
      on: (event: string, handler: () => void) => {
        if (event === "initialized") {
          handlers.push(handler);
        }
        return fake;
      },
      services: {},
    };

    installLanguageCodeCache(fake as never);
    expect(handlers).toHaveLength(1);

    const languageUtils = { formatLanguageCode: (code: string) => code.toUpperCase() };
    (fake as { services: unknown }).services = { languageUtils };
    handlers[0]?.();

    expect(languageUtils.formatLanguageCode("zh-cn")).toBe("ZH-CN");
  });
});
