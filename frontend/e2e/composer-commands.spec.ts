import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedRuntimeModels } from "./support";

// P2-7（composer 内置命令执行器）：
// - `/` 菜单列出**真实可执行**的内置命令（export / rename），菜单点选与命令行提交同源；
// - `/rename`：缺标题前置失败（不产生空标题），错误以 alert 透出且可关闭；
// - `/export`：走与轨迹页导出同一实现（分页拉取 → JSONL → 下载），成功通知回报条数与文件名；
//   非法参数前置失败，不发起导出；
// - `/model`：候选来自宿主真实运行时目录（e2e 用 mock 注入目录），无参数打开弹窗、
//   带参数精确应用；目录未就绪时如实报「不可用」，不伪造模型名；
// - `/feedback`：log-only 入口，回执如实说明未上报；
// - 已认领的命令执行后清空命令行（未认领/被阻塞时保留草稿供修正）。
//
// 提交口径与 P1-4 一致：菜单打开时纯 Enter 归菜单所有，命令行提交走 Ctrl/Cmd+Enter（或发送按钮）。
// 断言只依赖 `data-composer-*` 稳定钩子与下载事件，不绑定内部 DOM。

const composer = (page: Page) => page.locator(".app-chat-input");
const commandResult = (page: Page) => page.locator("[data-composer-command-result]");

test.beforeEach(async ({ page }) => {
  await resetMockState(page.request);
  await page.goto("/workspace");
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
});

test("P2-7: the slash menu lists builtin commands and /rename without a title fails loudly", async ({
  page,
}) => {
  // `/` 菜单与 `+` 按钮同源：这里只验证内置命令清单确实进入菜单（此前为空）。
  await composer(page).fill("/");
  await expect(page.locator("[data-composer-menu]")).toBeVisible();
  await expect(page.locator('[data-composer-command-option="export"]')).toBeVisible();
  await expect(page.locator('[data-composer-command-option="rename"]')).toBeVisible();
  await expect(page.locator('[data-composer-command-option="feedback"]')).toBeVisible();
  await expect(page.locator('[data-composer-command-option="model"]')).toBeVisible();

  // 命令行提交仍走宿主执行，不降级为 prompt。
  await composer(page).fill("/rename");
  await composer(page).press("Control+Enter");

  const notice = commandResult(page);
  await expect(notice).toHaveAttribute("data-composer-command-result", "error");
  await expect(notice).toHaveAttribute("role", "alert");
  await expect(notice).toContainText(/provide a new title/i);
  // 已认领：命令行清空，避免把命令当草稿留在输入框里。
  await expect(composer(page)).toHaveValue("");

  // 回执可关闭，且不残留。
  await page.locator("[data-composer-command-result-dismiss]").click();
  await expect(notice).toHaveCount(0);
});

test("P2-7: /export downloads the session trajectory and rejects unknown flags first", async ({
  page,
}) => {
  await composer(page).fill("use the tool to look it up");
  await composer(page).press("Control+Enter");
  await expect(page.getByText("Paris is the capital of France.").first()).toBeVisible({
    timeout: 20_000,
  });

  // 非法参数：前置失败，不发起导出（也不产生下载）。
  await composer(page).fill("/export --json");
  await composer(page).press("Control+Enter");
  await expect(commandResult(page)).toHaveAttribute(
    "data-composer-command-result",
    "error",
  );
  await expect(commandResult(page)).toContainText("--json");

  // 合法路径：下载 JSONL 并以成功通知回报条数与文件名。
  const downloadPromise = page.waitForEvent("download");
  await composer(page).fill("/export");
  await composer(page).press("Control+Enter");
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toContain(".jsonl");

  await expect(commandResult(page)).toHaveAttribute(
    "data-composer-command-result",
    "success",
  );
  await expect(commandResult(page)).toHaveAttribute("role", "status");
  await expect(commandResult(page)).toContainText(/exported \d+ event/);
  await expect(composer(page)).toHaveValue("");
});

test("P2-7: /model opens the runtime catalog dialog and applies the picked model", async ({
  page,
}) => {
  // 目录由宿主同一份运行时数据提供（这里 seed mock 的 `/api/runtime/models`）：
  // 候选、弹窗分组与校验集合都从这份目录派生，命令清单本身不内置模型名。
  await resetMockState(page.request);
  await seedRuntimeModels(page.request, {
    providers: [
      {
        name: "deepseek",
        default_model: "deepseek-chat",
        models: ["deepseek-chat", "deepseek-reasoner"],
      },
      { name: "openai", models: ["gpt-5"] },
    ],
    default_provider: "deepseek",
    default_model: "deepseek-chat",
  });
  await page.reload();
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });

  // 无参数提交：打开弹窗，候选按 provider 分组、当前项落在目录默认模型上。
  await composer(page).fill("/model");
  await composer(page).press("Control+Enter");

  const dialog = page.locator("[data-composer-model-dialog]");
  await expect(dialog).toBeVisible();
  await expect(page.locator('[data-composer-model-provider="deepseek"]')).toBeVisible();
  await expect(page.locator('[data-composer-model-provider="openai"]')).toBeVisible();
  await expect(page.locator("[data-composer-model-option]")).toHaveCount(3);
  await expect(page.locator("[data-composer-model-option-current]")).toHaveAttribute(
    "data-composer-model-option",
    "deepseek-chat",
  );

  // 点选即走常驻座位同一处理器：弹窗关闭，重开时 current 已移动（宿主状态确实更新）。
  await page.locator('[data-composer-model-option="gpt-5"]').click();
  await expect(dialog).toBeHidden();

  await composer(page).fill("/model");
  await composer(page).press("Control+Enter");
  await expect(page.locator("[data-composer-model-option-current]")).toHaveAttribute(
    "data-composer-model-option",
    "gpt-5",
  );
  await page.locator("[data-composer-model-dialog-close]").click();
  await expect(dialog).toBeHidden();

  // 带参数：精确 id 直接应用并回执（不经过弹窗）。
  await composer(page).fill("/model deepseek-reasoner");
  await composer(page).press("Control+Enter");
  await expect(commandResult(page)).toHaveAttribute(
    "data-composer-command-result",
    "success",
  );
  await expect(commandResult(page)).toContainText("deepseek-reasoner");

  // 未命中：报「目录里没有这个模型」并带原文，不改动当前选择。
  await composer(page).fill("/model nope");
  await composer(page).press("Control+Enter");
  await expect(commandResult(page)).toHaveAttribute(
    "data-composer-command-result",
    "error",
  );
  await expect(commandResult(page)).toContainText("nope");

  // 收尾：把 mock 目录复位，避免注入的目录泄漏给后续不 seed 的 spec
  //（页面内已取到的目录不再刷新，不影响本用例的断言）。
  await resetMockState(page.request);
});

test("P2-7: /model candidate surface searches locally and keeps no-match apart from an empty catalog", async ({
  page,
}) => {
  await resetMockState(page.request);
  await seedRuntimeModels(page.request, {
    providers: [
      {
        name: "deepseek",
        default_model: "deepseek-chat",
        models: ["deepseek-chat", "deepseek-reasoner"],
      },
      { name: "openai", models: ["gpt-5", "gpt-5-mini"] },
    ],
    default_provider: "deepseek",
    default_model: "deepseek-chat",
  });
  await page.reload();
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });

  await composer(page).fill("/model");
  await composer(page).press("Control+Enter");

  const dialog = page.locator("[data-composer-model-dialog]");
  const search = page.locator("[data-composer-model-search]");
  await expect(dialog).toBeVisible();
  // 弹层自持焦点：打开即可直接打字检索，不必先点输入框。
  await expect(search).toBeFocused();

  // 本地检索：按 provider 名命中该组全部模型，落空分组不留空壳。
  await search.fill("openai");
  await expect(page.locator('[data-composer-model-provider="deepseek"]')).toHaveCount(0);
  await expect(page.locator("[data-composer-model-option]")).toHaveCount(2);

  // 「检索无匹配」与「目录为空」是两种状态，各说各话。
  await search.fill("zzz");
  await expect(page.locator("[data-composer-model-dialog-no-match]")).toBeVisible();
  await expect(page.locator("[data-composer-model-dialog-empty]")).toHaveCount(0);
  await expect(page.locator("[data-composer-model-option]")).toHaveCount(0);

  // 方向键虚拟高亮（焦点仍在检索框）+ Enter 应用，随后重开可见 current 已移动。
  await search.fill("gpt");
  await search.press("ArrowDown");
  await expect(page.locator("[data-composer-model-option-active]")).toHaveAttribute(
    "data-composer-model-option",
    "gpt-5-mini",
  );
  await search.press("Enter");
  await expect(dialog).toBeHidden();

  await composer(page).fill("/model");
  await composer(page).press("Control+Enter");
  await expect(page.locator("[data-composer-model-option-current]")).toHaveAttribute(
    "data-composer-model-option",
    "gpt-5-mini",
  );
  await page.locator("[data-composer-model-dialog-close]").click();
  await expect(dialog).toBeHidden();

  await resetMockState(page.request);
});

test("P2-7: /model candidates drill down inside the slash menu and dispatch the picked model", async ({
  page,
}) => {
  await resetMockState(page.request);
  await seedRuntimeModels(page.request, {
    providers: [
      {
        name: "deepseek",
        default_model: "deepseek-chat",
        models: ["deepseek-chat", "deepseek-reasoner"],
      },
      { name: "openai", models: ["gpt-5"] },
    ],
    default_provider: "deepseek",
    default_model: "deepseek-chat",
  });
  await page.reload();
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });

  // 真实键盘输入（不用 fill）：候选层键盘路径与真实打字同源。
  await composer(page).click();
  await page.keyboard.type("/model");
  const menu = page.locator("[data-composer-menu]");
  await expect(menu).toBeVisible();

  // 第一层只有命令本身：Enter 下钻到该命令的专属候选（候选来自运行时目录，命令清单不内置模型名）。
  await page.keyboard.press("Enter");
  await expect(page.locator("[data-composer-command-option-value]")).toHaveCount(3);
  await expect(
    page.locator('[data-composer-command-option-value="deepseek-reasoner"]'),
  ).toBeVisible();

  // 方向键移动虚拟高亮，Enter 点选即派发（与命令行提交同语义）：命令行清空、菜单关闭。
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(menu).toHaveCount(0);
  await expect(composer(page)).toHaveValue("");

  // 座位确实移动：重开目录弹窗可见 current 落在所选模型上。
  await composer(page).fill("/model");
  await composer(page).press("Control+Enter");
  await expect(page.locator("[data-composer-model-option-current]")).toHaveAttribute(
    "data-composer-model-option",
    "deepseek-reasoner",
  );
  await page.locator("[data-composer-model-dialog-close]").click();

  await resetMockState(page.request);
});

test("P2-7: /model without a catalog reports not-ready and never fabricates models", async ({
  page,
}) => {
  // 本用例不 seed 目录：mock 的默认 `/api/runtime/models` 是空目录。
  await composer(page).fill("/model");
  await composer(page).press("Control+Enter");

  const dialog = page.locator("[data-composer-model-dialog]");
  await expect(dialog).toBeVisible();
  await expect(page.locator("[data-composer-model-dialog-empty]")).toBeVisible({
    timeout: 15_000,
  });
  await expect(page.locator("[data-composer-model-option]")).toHaveCount(0);

  await page.locator("[data-composer-model-dialog-close]").click();
  await expect(dialog).toBeHidden();

  // 目录未就绪不是「模型不存在」：文案必须区分，避免误导。
  await composer(page).fill("/model gpt-5");
  await composer(page).press("Control+Enter");
  await expect(commandResult(page)).toHaveAttribute(
    "data-composer-command-result",
    "error",
  );
  await expect(commandResult(page)).toContainText(/catalog is not ready/i);
  await expect(composer(page)).toHaveValue("");
});

test("P2-7: /feedback is log-only and says so in its receipt", async ({ page }) => {
  await composer(page).fill("/feedback the slash menu is handy");
  await composer(page).press("Control+Enter");

  const notice = commandResult(page);
  await expect(notice).toHaveAttribute("data-composer-command-result", "success");
  await expect(notice).toHaveAttribute("role", "status");
  // 回执如实说明「只写本地日志、本版本没有上报渠道」，不制造已提交的假象。
  await expect(notice).toContainText(/local log/i);
  await expect(notice).toContainText(/nothing was sent/i);
  await expect(composer(page)).toHaveValue("");

  // 空正文前置失败：不写日志、不产生空回执。
  await composer(page).fill("/feedback   ");
  await composer(page).press("Control+Enter");
  await expect(notice).toHaveAttribute("data-composer-command-result", "error");
  await expect(notice).toContainText(/provide feedback text/i);
});
