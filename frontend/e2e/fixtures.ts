/**
 * e2e 共享 test/expect（P0-7）。
 *
 * 在 Playwright 内置 `screenshot: only-on-failure`（viewport 截图）之上，
 * 额外补一张**全页**截图并挂到测试报告附件；页面超长时仍能一眼看到失败
 * 位置之前/之后的完整上下文。
 *
 * 用例统一 `import { expect, test } from "./fixtures";`
 */
import { expect, test as base } from "@playwright/test";

import { saveFailureShot } from "./support";

export const test = base.extend({
  // Playwright fixture 的第二个参数固定叫 `use`（值的生命周期回调），
  // 与 React Hook 规则同名但无关。
  page: async ({ page }, use, testInfo) => {
    // eslint-disable-next-line react-hooks/rules-of-hooks
    await use(page);

    if (testInfo.status !== testInfo.expectedStatus) {
      const file = await saveFailureShot(page, testInfo.titlePath.join("_"));
      testInfo.attachments.push({
        name: "full-page-screenshot",
        path: file,
        contentType: "image/png",
      });
    }
  },
});

export { expect };
