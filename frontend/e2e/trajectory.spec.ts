import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { resetMockState, seedRuntimeEvents, waitForAssistantHistory } from "./support";

// Phase 2 acceptance coverage for the trajectory view:
// - P2-1: events appear row by row during/after streaming; filters apply;
//         search filters; detail panel shares the same item data.
// - P2-2: timeline overview exists and jumps to the matching row.
// - P2-3: 1000+ event session renders a bounded DOM (virtual rows) and can
//         scroll to the tail.
// - P2-4: chat <-> trajectory switching keeps both projections consistent.

const composer = (page: Page) => page.locator(".app-chat-input");

async function sendPrompt(page: Page, text: string) {
  await composer(page).fill(text);
  await composer(page).press("Control+Enter");
}

async function waitForPromptVisible(page: Page) {
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
}

const trajectoryTab = (page: Page) =>
  page.getByRole("tab", { name: "Trajectory" });
const chatTab = (page: Page) => page.getByRole("tab", { name: "Chat" });
const rows = (page: Page) => page.locator("[data-trajectory-row]");
const list = (page: Page) => page.locator("[data-trajectory-list]");

test.beforeEach(async ({ page }) => {
  // mock server 跨 spec 共享：每个用例前清空会话历史/事件/故障开关，
  // 否则 e2e-session-1 的轨迹会混入上一个用例的事件。
  await resetMockState(page.request);
  await page.goto("/workspace");
  await waitForPromptVisible(page);
});

test("P2-4/P2-1: chat -> trajectory switch keeps events and chat intact", async ({
  page,
}) => {
  await sendPrompt(page, "use the tool to look it up");
  await expect(page.getByText("Paris is the capital of France.").first()).toBeVisible({
    timeout: 20_000,
  });

  await trajectoryTab(page).click();
  // trajectory rows for the tool + answer are present
  await expect(rows(page).first()).toBeVisible({ timeout: 10_000 });
  await expect(rows(page).filter({ hasText: "web_search" }).first()).toBeVisible();
  await expect(rows(page).filter({ hasText: "Paris" }).first()).toBeVisible();

  // switching back keeps the chat message
  await chatTab(page).click();
  await expect(page.getByText("Paris is the capital of France.").first()).toBeVisible();
});

test("P2-1: tool filter keeps only tool rows", async ({ page }) => {
  await sendPrompt(page, "use the tool to look it up");
  await expect(page.getByText("Paris is the capital of France.").first()).toBeVisible({
    timeout: 20_000,
  });

  await trajectoryTab(page).click();
  await page.getByRole("button", { name: "Tools" }).click();

  await expect(rows(page)).toHaveCount(1, { timeout: 10_000 });
  await expect(rows(page).first()).toContainText("web_search");
});

test("P2-1: search filters rows by content", async ({ page }) => {
  await sendPrompt(page, "use the tool to look it up");
  await expect(page.getByText("Paris is the capital of France.").first()).toBeVisible({
    timeout: 20_000,
  });

  await trajectoryTab(page).click();
  const search = page.getByRole("textbox", { name: "Search trajectory" });
  // 检索词必须只命中「回答」：mock 的 web_search 入参是 "capital of France"，
  // 而 argsSummary 参与检索是既定口径（trajectoryItemText 含工具入参摘要），
  // 用 "capital" 检索本来就会命中工具行。改用只出现在回答正文里的词。
  await search.fill("gave us");
  await expect(rows(page).filter({ hasText: "gave us" }).first()).toBeVisible();
  await expect(rows(page).filter({ hasText: "web_search" })).toHaveCount(0);

  await search.fill("no-such-term");
  await expect(page.getByText(/No rows match/)).toBeVisible({ timeout: 5_000 });
});

test("P2-2: timeline jumps to the tool row and opens the detail panel", async ({
  page,
}) => {
  await sendPrompt(page, "use the tool to look it up");
  await expect(page.getByText("Paris is the capital of France.").first()).toBeVisible({
    timeout: 20_000,
  });

  await trajectoryTab(page).click();
  // 时间线泳道是可聚焦的交互区域（滚轮缩放 / 拖拽框选 / 键盘平移），
  // 因此角色是 group 而不是 img；aria-label 与无障碍提示保持不变。
  const timeline = page.getByRole("group", { name: "Trajectory timeline" });
  await expect(timeline).toBeVisible();

  // timeline blocks are labelled "<kind> <seq>"
  const toolBlock = page.locator('[aria-label^="tool "]').first();
  await expect(toolBlock).toBeVisible();
  await toolBlock.click();

  await expect(
    page.getByRole("button", { name: "Close trajectory detail" }),
  ).toBeVisible();
  await expect(page.locator("[data-trajectory-detail]")).toContainText("web_search");
});

test("P2-3: 1000+ event session virtualizes rows and scrolls to the tail", async ({
  page,
}) => {
  await sendPrompt(page, "burst events please");
  // wait for the stream to finish：以最终回复文本为准（确定性信号），
  // 不用 "Streaming" 这类同时命中消息元数据与流式徽标的模糊文案。
  await expect(page.getByText("burst complete")).toBeVisible({ timeout: 60_000 });
  await expect(page.getByText("Streaming response in progress")).not.toBeVisible();

  await trajectoryTab(page).click();
  await expect(rows(page).first()).toBeVisible({ timeout: 10_000 });

  // virtual window: rendered DOM rows stay far below the 1210+ item count
  const rendered = await rows(page).count();
  expect(rendered).toBeGreaterThan(5);
  expect(rendered).toBeLessThan(150);

  // tail events are not rendered until scrolled into view
  await expect(rows(page).filter({ hasText: "observation-1199" })).toHaveCount(0);

  await list(page).evaluate((element) => {
    element.scrollTop = element.scrollHeight;
  });
  await expect(rows(page).filter({ hasText: "observation-1199" }).first()).toBeVisible({
    timeout: 10_000,
  });
});

test("P3-1: trajectory recovers from EventStore after page reload", async ({
  page,
}) => {
  await sendPrompt(page, "use the tool to look it up");
  await expect(page.getByText("Paris is the capital of France.").first()).toBeVisible({
    timeout: 20_000,
  });
  // mock（与后端同口径）在 turn 收尾的 `done` 帧才把 assistant 落库，正文 chunk 先到；
  // 若此刻刷新，恢复出的历史只有用户消息 → 先显式等落库，去掉恢复断言的竞态。
  await waitForAssistantHistory(
    page.request,
    "e2e-session-1",
    "Paris is the capital of France.",
  );

  // Q4：注入一条 runtime 生命周期事件（approval_requested）进入 EventStore，
  // 验证恢复路径把它映射为轨迹 system 行（与 chat.sse.* 共用 seq 序列）。
  const seq = await seedRuntimeEvents(page.request, "e2e-session-1", [
    {
      type: "approval_requested",
      payload: { tool_name: "shell", request_id: "req-e2e" },
    },
  ]);
  expect(seq).toBeGreaterThan(0);

  // 刷新页面：chat 消息经 history sync 恢复，轨迹经 EventStore 增量拉取恢复。
  await page.reload();
  await waitForPromptVisible(page);
  await expect(page.getByText("Paris is the capital of France.").first()).toBeVisible({
    timeout: 20_000,
  });

  await trajectoryTab(page).click();
  await expect(rows(page).first()).toBeVisible({ timeout: 10_000 });
  // 与中断前逐条一致：工具行 + 文本行都在。
  await expect(
    rows(page).filter({ hasText: "web_search" }).first(),
  ).toBeVisible();
  await expect(
    rows(page).filter({ hasText: "Paris is the capital" }).first(),
  ).toBeVisible();
  // runtime 生命周期事件恢复映射：approval 行存在且可定位（滚动到末尾）。
  // 注：恢复完成后初始视口停在顶部（虚拟滚动保留 P2-3 有界语义），
  // 因此先显式定位到末尾再断言行内容。
  await list(page).evaluate((element) => {
    element.scrollTop = element.scrollHeight;
  });
  await expect(
    rows(page).filter({ hasText: "approval requested: shell" }).first(),
  ).toBeVisible();
});

test("P3-2: export downloads JSONL matching EventStore events", async ({
  page,
}) => {
  await sendPrompt(page, "use the tool to look it up");
  await expect(page.getByText("Paris is the capital of France.").first()).toBeVisible({
    timeout: 20_000,
  });

  await trajectoryTab(page).click();
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export trajectory" }).click();
  const download = await downloadPromise;
  const stream = await download.createReadStream();
  const chunks: Buffer[] = [];
  for await (const chunk of stream) {
    chunks.push(Buffer.from(chunk));
  }
  const jsonl = Buffer.concat(chunks).toString("utf8");
  const lines = jsonl.split("\n").filter((line) => line.trim().length > 0);

  // 与 EventStore 拉取结果逐条一致：meta 打头、含工具事件、每行带 seq。
  expect(lines.length).toBeGreaterThan(3);
  const entries = lines.map((line) => JSON.parse(line));
  expect(entries[0]?.kind).toBe("meta");
  expect(entries.some((entry) => entry.kind === "tool_start")).toBe(true);
  expect(entries.some((entry) => entry.kind === "done")).toBe(true);
  expect(entries.every((entry) => typeof entry.seq === "number")).toBe(true);
  expect(entries.every((entry) => typeof entry.ts === "string")).toBe(true);

  // 脱敏导出（R4）：工具参数值被掩码、身份字段保留、正文保留。
  await page.getByRole("button", { name: "Toggle export redaction" }).click();
  const redactedDownloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export trajectory" }).click();
  const redactedDownload = await redactedDownloadPromise;
  const redactedStream = await redactedDownload.createReadStream();
  const redactedChunks: Buffer[] = [];
  for await (const chunk of redactedStream) {
    redactedChunks.push(Buffer.from(chunk));
  }
  const redactedLines = Buffer.concat(redactedChunks)
    .toString("utf8")
    .split("\n")
    .filter((line) => line.trim().length > 0);
  const redactedEntries = redactedLines.map((line) => JSON.parse(line));
  const toolStart = redactedEntries.find((entry) => entry.kind === "tool_start");
  expect(toolStart.payload.tool.args).toEqual({ query: "<redacted>" });
  expect(toolStart.payload.tool.name).toBe("web_search");
  // 正文 chunk 不脱敏（脱敏只作用于工具参数/输出）。
  const parisChunk = redactedEntries.find(
    (entry) =>
      entry.kind === "chunk" &&
      typeof entry.payload.content === "string" &&
      entry.payload.content.includes("Paris"),
  );
  expect(parisChunk).toBeTruthy();
  expect(parisChunk.payload.content).not.toBe("<redacted>");
});
