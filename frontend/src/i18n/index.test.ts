import { afterEach, describe, expect, it } from "vitest";

import { createFallbackReporter } from "./fallback";
import { applyDocumentLang, createI18nInstance, i18n, initI18n } from "./index";

afterEach(() => {
  applyDocumentLang("zh-CN");
});

describe("createI18nInstance 回退链", () => {
  it("en 命中自身文案；缺失时回退 zh；全部缺失时回退 key 文本并告警", async () => {
    const reporter = createFallbackReporter({ enabled: true, sink: () => {} });
    const instance = await createI18nInstance("en-US", { reporter });

    expect(instance.t("workspace:shell.newChatTitle")).toBe(
      "What do you want to accomplish?",
    );

    instance.removeResourceBundle("en-US", "workspace");
    expect(instance.t("workspace:shell.newChatTitle")).toBe("今天想完成什么？");

    instance.removeResourceBundle("zh-CN", "workspace");
    expect(instance.t("workspace:shell.newChatTitle")).toContain("shell.newChatTitle");
    expect(
      reporter.notices.some(
        (notice) =>
          notice.kind === "missing-key" && notice.key.includes("shell.newChatTitle"),
      ),
    ).toBe(true);
  });

  it("reporter 关闭时不接收告警但仍回退 key 文本", async () => {
    const reporter = createFallbackReporter({ enabled: false, sink: () => {} });
    const instance = await createI18nInstance("en-US", { reporter });
    // 故意查询不存在的键：类型化 t() 会拒绝，这里按运行时行为验证回退链。
    const looseTranslate = instance.t as unknown as (key: string) => string;
    expect(looseTranslate("workspace:missing.key.path")).toContain("missing.key.path");
    expect(reporter.notices).toHaveLength(0);
  });
});

describe("document.lang 回写", () => {
  it("按 locale 回写 html lang，未支持语言忽略", () => {
    applyDocumentLang("en-US");
    expect(document.documentElement.lang).toBe("en-US");
    applyDocumentLang("fr-FR");
    expect(document.documentElement.lang).toBe("en-US");
  });

  it("initI18n 切换语言时同步回写", () => {
    initI18n("en-US");
    expect(i18n.language).toBe("en-US");
    expect(document.documentElement.lang).toBe("en-US");

    initI18n("zh-CN");
    expect(i18n.language).toBe("zh-CN");
    expect(document.documentElement.lang).toBe("zh-CN");
  });
});
