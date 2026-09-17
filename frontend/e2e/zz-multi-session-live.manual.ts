/**
 * 多会话并发运行时 **live 实测**（真后端 + 真 provider，非验收闸门）。
 *
 * 验证假设（H1–H5）：
 *   H1 A 会话真流式：`data-active-turn` 出现且助手文本渐进增长；
 *   H2 切走后 A 仍在跑：新页面/新会话下，侧栏 A 行显示「运行中」（注册表后台投影）；
 *   H3 B 可并行提交：A 在途时 B 的回合仍能被接受并拿到回复（同会话内仍单飞）；
 *   H4 切回 A 无丢失：A 的正文长度不缩短、回合最终收口（或仍在增长）；
 *   H5 全过程无 console error；除客户端主动取消（`net::ERR_ABORTED`，导航/卸载
 *      触发的长连接断连）外，硬失败请求为 0。
 *
 * 运行（真实 dev server 5193 + runtime-server 8101）：
 *   $env:LIVE_BASE_URL="http://localhost:5193"
 *   npm run test:manual -- e2e/zz-multi-session-live.manual.ts --reporter=line
 *
 * 产物：`.artifacts/live-multi-session/<ts>.json`（逐步证据，含软失败）。
 * 说明：用例会真实调用 provider（成本 ≈ 2 个回合），并把 A 的长回合跑完。
 */
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { expect, test, type Page } from "./fixtures";

const BASE = (process.env.LIVE_BASE_URL ?? "http://localhost:5193").replace(/\/$/, "");
const RUNTIME_API = (process.env.LIVE_RUNTIME_API ?? "http://127.0.0.1:8101").replace(/\/$/, "");
const PROVIDER = process.env.LIVE_PROVIDER ?? "opencode.ai";
const MODEL = process.env.LIVE_MODEL ?? "deepseek-v4.1-flash";
const EFFORT = process.env.LIVE_EFFORT ?? "max";
const STAMP = Date.now();
const MARK_A = `LIVEA${STAMP}`;
const MARK_B = `LIVEB${STAMP}`;
const PROMPT_A =
  process.env.LIVE_PROMPT_A ??
  `请用中文输出 240 行编号短句，每行不超过 20 字，不要调用任何工具。首行附上标记 ${MARK_A}。`;
const PROMPT_B = process.env.LIVE_PROMPT_B ?? `只回复：B-OK-${MARK_B}（不要调用任何工具）。`;

type Phase = { phase: string; at: number; note?: string; [key: string]: unknown };

const phases: Phase[] = [];
const consoleErrors: string[] = [];
/** 既有 React 开发态告警（WorkspacePage 渲染期自更新）：单独留证，不参与 H5 判定。 */
const consoleKnownWarnings: string[] = [];
const failedRequests: Array<{ url: string; failure: string }> = [];
const abortedChatRequests: string[] = [];
/** 客户端主动取消（导航 / 卸载 / 重建）：只留证，不参与 H5 断言。 */
const abortedRequests: Array<{ url: string; failure: string; at: number }> = [];
const chatStreams: Array<{ url: string; startedAt: number }> = [];
const runtimeSubscriptions: Array<{ url: string; at: number }> = [];
const runtimePolls: Array<{ url: string; at: number }> = [];

function record(phase: string, payload: Record<string, unknown> = {}): void {
  phases.push({ phase, at: Date.now(), ...payload });
}

/**
 * `net::ERR_ABORTED` = **客户端**主动取消（导航卸载 / 组件卸载 / AbortController /
 * SSE 重建），不是服务端失败：Chromium 只在渲染进程取消请求时给出该错误。多会话
 * 注册表会随导航反复建连/断连，因此这类取消是预期行为，单独留证即可。
 */
function isClientAbort(failure: string): boolean {
  return /ERR_ABORTED|ABORTED|aborted/i.test(failure);
}

function writeArtifact(extra: Record<string, unknown>): string {
  const dir = join(process.cwd(), ".artifacts", "live-multi-session");
  mkdirSync(dir, { recursive: true });
  const file = join(dir, `${STAMP}.json`);
  writeFileSync(
    file,
    JSON.stringify(
      {
        stamp: STAMP,
        base: BASE,
        runtimeApi: RUNTIME_API,
        provider: PROVIDER,
        model: MODEL,
        effort: EFFORT,
        promptA: PROMPT_A,
        promptB: PROMPT_B,
        phases,
        consoleErrors,
        consoleKnownWarnings,
        failedRequests,
        abortedChatRequests,
        abortedRequests,
        chatStreams,
        runtimeSubscriptions,
        runtimePolls,
        ...extra,
      },
      null,
      2,
    ),
    "utf8",
  );
  return file;
}

function attachCollectors(page: Page): void {
  page.on("console", (message) => {
    if (message.type() !== "error") return;
    const text = message.text();
    if (text.includes("Cannot update a component")) {
      consoleKnownWarnings.push(text);
      return;
    }
    consoleErrors.push(text);
  });
  page.on("requestfailed", (request) => {
    const failure = request.failure()?.errorText ?? "unknown";
    const url = request.url();
    if (isClientAbort(failure)) {
      abortedRequests.push({ url, failure, at: Date.now() });
      if (url.includes("/api/agent/chat")) {
        abortedChatRequests.push(`${request.method()} ${url} :: ${failure}`);
      }
      return;
    }
    failedRequests.push({ url, failure });
  });
  page.on("request", (request) => {
    const url = request.url();
    if (url.includes("/api/agent/chat")) {
      chatStreams.push({ url: request.url(), startedAt: Date.now() });
    }
    // Batch 2/4 观测：后台会话的 live 建连（/runtime/stream）与降级轮询（/runtime）。
    if (/\/api\/runtime\/sessions\/[^/]+\/runtime\/stream/.test(url)) {
      runtimeSubscriptions.push({ url, at: Date.now() });
    } else if (/\/api\/runtime\/sessions\/[^/]+\/runtime(\?|$)/.test(url)) {
      runtimePolls.push({ url, at: Date.now() });
    }
  });
}

/** 强行把每个 chat 回合钉到 live 参数（provider/model/effort + 断线续跑）。 */
async function pinLiveTurnOptions(page: Page): Promise<void> {
  await page.route("**/api/agent/chat*", async (route) => {
    const request = route.request();
    let body: Record<string, unknown> = {};
    try {
      body = (request.postDataJSON() as Record<string, unknown>) ?? {};
    } catch {
      body = {};
    }
    body.provider = PROVIDER;
    body.model = MODEL;
    body.reasoning_effort = EFFORT;
    body.resume_on_disconnect = true;
    if (typeof body.stream !== "boolean") {
      body.stream = true;
    }
    await route.continue({
      postData: JSON.stringify(body),
      headers: { ...request.headers(), "content-type": "application/json" },
    });
  });
}

async function listSessionIds(page: Page): Promise<string[]> {
  const response = await page.request.get(`${RUNTIME_API}/api/runtime/sessions`);
  const json = (await response.json()) as { sessions?: Array<{ id?: string }> };
  return (json.sessions ?? []).map((item) => item.id ?? "").filter((id) => id.length > 0);
}

/** 轮询运行时会话列表，找出测试开始后才出现的新会话 id。 */
async function discoverNewSessionId(page: Page, known: Set<string>): Promise<string> {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    try {
      const ids = await listSessionIds(page);
      const fresh = ids.filter((id) => !known.has(id));
      if (fresh.length > 0) {
        return fresh[0];
      }
    } catch {
      // 列表接口偶发失败不致命，继续轮询。
    }
    await page.waitForTimeout(1000);
  }
  return "";
}

async function sendPrompt(page: Page, prompt: string): Promise<void> {
  const composer = page.locator("textarea.app-chat-input").first();
  await composer.waitFor({ state: "visible", timeout: 30_000 });
  const beforeUrl = page.url();
  await composer.click();
  await composer.fill(prompt);
  await composer.press("Control+Enter");
  // 首条消息会触发 SPA 路由切到 canonical 会话路由（无整页刷新）；等 URL 稳定再采样，
  // 否则 evaluate 会撞上 "Execution context was destroyed"。
  await page
    .waitForURL((url) => url.toString() !== beforeUrl, { timeout: 30_000 })
    .catch(() => undefined);
  await page.waitForTimeout(800);
}

type StreamSnapshot = {
  activeTurn: boolean;
  /** 助手正文长度（流式行 / 全部消息行的最大文本长度）。 */
  assistantChars: number;
  streamingChars: number;
  anchors: number;
  lastAnchorText: string;
};

async function snapshotStream(page: Page): Promise<StreamSnapshot> {
  // 路由/历史加载会在采样间隙切换文档上下文：失败即退避重试，避免误判为功能缺陷。
  let lastError: unknown = null;
  for (let attempt = 1; attempt <= 6; attempt += 1) {
    try {
      return await page.evaluate(() => {
        const active = document.querySelector('[data-active-turn="true"]');
        const streamingRow = document.querySelector('[aria-busy="true"][data-message-id]');
        const rows = Array.from(document.querySelectorAll("[data-message-id]"));
        const texts = rows.map((row) => row.textContent ?? "");
        const maxChars = texts.reduce((max, text) => Math.max(max, text.length), 0);
        const streamingChars = (streamingRow?.textContent ?? "").length;
        const lastText = texts.length > 0 ? texts[texts.length - 1] : "";
        return {
          activeTurn: Boolean(active),
          assistantChars: Math.max(maxChars, streamingChars),
          streamingChars,
          anchors: rows.length,
          lastAnchorText: lastText.slice(-160),
        };
      });
    } catch (error) {
      lastError = error;
      await page.waitForTimeout(300 * attempt);
    }
  }
  throw lastError;
}

/** 服务端侧证据：该会话 runtime 投影（active_turn = 服务端仍在跑）。 */
async function sessionRuntimeDigest(page: Page, sessionId: string): Promise<string> {
  if (!sessionId) return "no-session-id";
  try {
    const response = await page.request.get(
      `${RUNTIME_API}/api/runtime/sessions/${encodeURIComponent(sessionId)}/runtime`,
    );
    const text = await response.text();
    return `status=${response.status()} ${text.slice(0, 700)}`;
  } catch (error) {
    return `request-failed: ${String(error).slice(0, 200)}`;
  }
}

type SidebarRow = { group: string; title: string; status: string };

/**
 * 侧栏可见性取证（2026-09-17 实测修正）：
 *  - 目录分组默认**折叠**（`openSessionDirectories[key] ?? false`），不展开则整组行不渲染；
 *  - 组内还有「显示前 N 条」上限，需要点组内 `sidebar-session-group-toggle` 才渲染全量；
 *  - 因此按 data-group-key 逐个展开组头 + 组内折叠，再读取行。
 * 空闲行本就不渲染状态徽标（`getSessionStatusIcon` 对 restored 返回 null），
 * 所以「没有徽标」≠ 取样失败；这里以 aria-label 是否存在为准。
 */
async function expandSidebarSessions(page: Page): Promise<void> {
  const keys = await page.evaluate(() =>
    Array.from(document.querySelectorAll('[data-testid="sidebar-session-group"]')).map(
      (group) => group.getAttribute("data-group-key") ?? "",
    ),
  );
  for (const key of keys) {
    const group = page
      .locator(`[data-testid="sidebar-session-group"][data-group-key="${key}"]`)
      .first();
    if ((await group.count()) === 0) continue;
    const header = group.locator("button[aria-expanded]").first();
    if ((await header.getAttribute("aria-expanded").catch(() => null)) === "false") {
      await header.click({ timeout: 5000 }).catch(() => undefined);
      await page.waitForTimeout(200);
    }
    const toggle = group.locator('[data-testid="sidebar-session-group-toggle"]');
    if ((await toggle.count()) > 0) {
      await toggle.first().click({ timeout: 5000 }).catch(() => undefined);
      await page.waitForTimeout(200);
    }
  }
}

/** 跨所有分组列表找目标会话行：返回状态徽标 aria-label 与行内停止按钮是否存在。 */
async function sessionRowProbe(
  page: Page,
  needle: string,
): Promise<{ titleAttr: string; status: string; hasStop: boolean } | null> {
  return page.evaluate((needleText) => {
    const rows = Array.from(
      document.querySelectorAll('[data-testid="sidebar-session-list"] [role="treeitem"]'),
    );
    for (const row of rows) {
      const button = row.querySelector("button");
      const titleAttr = button?.getAttribute("title") ?? "";
      if (!titleAttr.includes(needleText)) continue;
      const icon = row.querySelector("button span[aria-label]");
      const stop = row.querySelector(
        '[data-testid="session-row-actions-slot"] [aria-label*="停止"]',
      );
      return {
        titleAttr,
        status: icon?.getAttribute("aria-label") ?? "",
        hasStop: Boolean(stop),
      };
    }
    return null;
  }, needle);
}

/** 侧栏行样本（只作证据）：展开后跨组取前两行，避免把 400+ 行写进产物。 */
async function sidebarRows(page: Page): Promise<SidebarRow[]> {
  await expandSidebarSessions(page);
  return page.evaluate(() => {
    const out: Array<{ group: string; title: string; status: string }> = [];
    document.querySelectorAll('[data-testid="sidebar-session-group"]').forEach((group) => {
      const list = group.querySelector('[data-testid="sidebar-session-list"]');
      if (!list) return;
      const rows = Array.from(list.querySelectorAll('[role="treeitem"]')).slice(0, 2);
      for (const row of rows) {
        const button = row.querySelector("button");
        const icon = row.querySelector("button span[aria-label]");
        out.push({
          group: (group.getAttribute("data-group-key") ?? "").slice(0, 16),
          title: (button?.getAttribute("title") ?? "").slice(0, 32),
          status: icon?.getAttribute("aria-label") ?? "",
        });
      }
    });
    return out;
  });
}

/**
 * 侧栏行「在途」判定的渲染标签：徽标文案随界面语言变化（i18n
 * `sidebar.sessionStatuses.*`），这里同时认 zh-CN / en-US，避免整套断言隐式依赖
 * 浏览器语言（本机 Playwright 默认 en-US，只认中文会得到假阴性）。
 */
const IN_FLIGHT_STATUS_LABELS = [
  "运行中",
  "Running",
  "子代理运行中",
  "Subagents running",
  "等待回答",
  "Waiting for your answer",
  "等待审批",
  "Waiting for approval",
  "计划待审",
  "Plan awaiting review",
] as const;

function isInFlightRow(
  row: { status: string; hasStop: boolean } | null,
): boolean {
  if (!row) {
    return false;
  }
  return (
    row.hasStop ||
    IN_FLIGHT_STATUS_LABELS.some((label) => row.status.includes(label))
  );
}

async function gotoNewThread(page: Page): Promise<void> {
  await page.goto(`${BASE}/workspace/chats/new`, { waitUntil: "domcontentloaded" });
  await page.locator("textarea.app-chat-input").first().waitFor({ state: "visible", timeout: 45_000 });
}


// 失败也要留下证据：afterEach 兜底写产物（成功路径已显式写过，同 stamp 覆盖）。
test.afterEach(({ page }, testInfo) => {
  try {
    record("afterEach", { status: testInfo.status, url: page.url() });
    writeArtifact({ finalStatus: testInfo.status });
  } catch {
    // 证据写入失败不影响用例判定。
  }
});

test("多会话并发运行时：A 长回合 × 切走续跑 × B 并行提交 × 切回无损", async ({ page }) => {
  test.setTimeout(1_200_000);
  attachCollectors(page);
  await pinLiveTurnOptions(page);

  // ── Phase 0：基线快照（已知会话集合 + 侧栏行） ────────────────────────────
  try {
    await gotoNewThread(page);
  } catch {
    await page.goto(`${BASE}/workspace`, { waitUntil: "domcontentloaded" });
    await page.locator("textarea.app-chat-input").first().waitFor({ state: "visible", timeout: 45_000 });
  }
  await page.waitForTimeout(1500);
  const beforeIds = new Set(await listSessionIds(page).catch(() => [] as string[]));
  record("baseline", {
    url: page.url(),
    knownSessions: beforeIds.size,
    rows: await sidebarRows(page),
  });

  // ── Phase 1：A 长回合（真流式） ──────────────────────────────────────────
  await sendPrompt(page, PROMPT_A);
  record("A.sent", { url: page.url() });

  // H1a：客户端真流式信号（data-active-turn）——只等它出现，不做长采样：
  // 采样窗口必须留给 Phase 2（切走时回合**必须仍在途**，否则 H2 无意义）。
  let a1 = await snapshotStream(page);
  const activeDeadline = Date.now() + 60_000;
  while (Date.now() < activeDeadline && !a1.activeTurn) {
    await page.waitForTimeout(500);
    a1 = await snapshotStream(page);
  }
  expect(a1.activeTurn, "H1: A 回合应出现 data-active-turn（真流式）").toBe(true);

  // 会话 id 提前定位：H1b / H2 的服务端证据都依赖它。
  const urlSessionId =
    page.url().match(/\/workspace\/sessions\/([^/?#]+)/)?.[1] ?? "";
  const sessionA =
    decodeURIComponent(urlSessionId) ||
    (await discoverNewSessionId(page, beforeIds)) ||
    page.url().split("/").pop() ||
    "";
  expect(sessionA.length, "H1: 应能定位 A 的运行时会话 id").toBeGreaterThan(0);

  // H1b：服务端权威信号——`active_turn` 非空（切走后仍能续跑的前提）。
  let serverActiveSeen = false;
  let serverDigest = "";
  const serverActiveDeadline = Date.now() + 30_000;
  while (Date.now() < serverActiveDeadline && !serverActiveSeen) {
    serverDigest = await sessionRuntimeDigest(page, sessionA);
    serverActiveSeen = /"active_turn":\s*\{/.test(serverDigest);
    if (!serverActiveSeen) {
      await page.waitForTimeout(1000);
    }
  }
  expect(
    serverActiveSeen,
    `H1: 服务端应有在途回合（active_turn 非空），实际=${serverDigest}`,
  ).toBe(true);

  // 正文出现（两次采样即可；不把在途窗口耗在等固定字数上——上一轮就是因为
  // 等 200 字把切走时机推迟到回合结束之后，H2 才出现假阴性）。
  await page.waitForTimeout(2500);
  const a2 = await snapshotStream(page);
  record("A.streaming", { a1, a2, sessionA, url: page.url(), serverDigest });
  expect(a2.assistantChars, "H1: A 助手正文应出现并增长").toBeGreaterThan(0);

  // ── Phase 2：切走（新线程页）→ A 应转入后台但仍显示「运行中」 ────────────
  const switchedAt = Date.now();
  await gotoNewThread(page);
  // A 的目录归属由服务端 metadata.context.workspace_path 决定；本用例的会话没有
  // workspace_path，因此落在「未绑定目录」大组（229+ 行，默认折叠 + 只渲染前 5 行）。
  // 用提示词前缀（行标题 = 首条用户消息派生标题）定位 A 的行。
  const rowNeedle = PROMPT_A.slice(0, 12);
  await expandSidebarSessions(page);
  let rowA = await sessionRowProbe(page, rowNeedle);
  let aRunningSeen = isInFlightRow(rowA);
  // 同一轮询里同时取证「行显示运行中」与「服务端仍在途」：两者都要在切走后
  // 被观测到（前者=注册表后台投影，后者=后台续跑的服务端权威证据）。
  let backgroundActiveSeen = false;
  let detachedSeen = false;
  let lastBackgroundDigest = "";
  const backgroundDeadline = Date.now() + 90_000;
  while (Date.now() < backgroundDeadline) {
    lastBackgroundDigest = await sessionRuntimeDigest(page, sessionA);
    if (/"active_turn":\s*\{/.test(lastBackgroundDigest)) {
      backgroundActiveSeen = true;
    }
    if (lastBackgroundDigest.includes('"detached":true')) {
      detachedSeen = true;
    }
    rowA = await sessionRowProbe(page, rowNeedle);
    aRunningSeen = isInFlightRow(rowA);
    if (aRunningSeen && backgroundActiveSeen) {
      break;
    }
    await page.waitForTimeout(2000);
  }
  record("A.background", {
    switchedAt,
    aRunningSeen,
    backgroundActiveSeen,
    detachedSeen,
    rowA,
    subscriptions: runtimeSubscriptions
      .filter((item) => item.url.includes(sessionA))
      .map((item) => item.at),
    polls: runtimePolls.filter((item) => item.url.includes(sessionA)).map((item) => item.at),
    rowsSample: await sidebarRows(page),
    serverA: lastBackgroundDigest,
  });
  expect(rowA, "H2: 切走后侧栏应能找到 A 的行（分组展开后）").not.toBeNull();
  expect(
    aRunningSeen,
    `H2: 切走后 A 行应显示在途徽标（${IN_FLIGHT_STATUS_LABELS.join(" / ")}）或提供行内停止（注册表后台投影），实际=${JSON.stringify(rowA)}`,
  ).toBe(true);
  expect(
    backgroundActiveSeen,
    `H2: 切走后服务端应仍在途（后台续跑），实际=${lastBackgroundDigest}`,
  ).toBe(true);

  // ── Phase 3：A 在途时在 B 提交并拿到回复（同会话单飞不受影响） ─────────────
  await sendPrompt(page, PROMPT_B);
  record("B.sent", {
    rowAAfterSwitch: await sessionRowProbe(page, rowNeedle),
    rowsBeforeB: await sidebarRows(page),
    url: page.url(),
  });

  await page.waitForFunction(
    (needle) => document.body.innerText.includes(needle),
    `B-OK-${MARK_B}`,
    { timeout: 300_000 },
  );
  const rowsAfterB = await sidebarRows(page);
  record("B.replied", { rowsAfterB, url: page.url() });
  const bodyText = await page.evaluate(() => document.body.innerText);
  expect(bodyText, "H3: B 应拿到自己的回复").toContain(`B-OK-${MARK_B}`);

  // ── Phase 4：切回 A（canonical 会话路由）→ 正文无损、回合收口 ────────────
  await page.goto(`${BASE}/workspace/sessions/${encodeURIComponent(sessionA)}`, {
    waitUntil: "domcontentloaded",
  });
  await page.locator("textarea.app-chat-input").first().waitFor({ state: "visible", timeout: 45_000 });
  // 历史加载可能滞后于 composer：轮询等待 A 的消息行落地（≤30s）。
  const historyDeadline = Date.now() + 30_000;
  let back1 = await snapshotStream(page);
  while (Date.now() < historyDeadline && back1.anchors === 0) {
    await page.waitForTimeout(1000);
    back1 = await snapshotStream(page);
  }
  await expandSidebarSessions(page);
  const backRowA = await sessionRowProbe(page, rowNeedle);
  record("A.back", { back1, rowA: backRowA, url: page.url(), rows: await sidebarRows(page) });
  expect(back1.anchors, "H4: 切回 A 应能看到消息行").toBeGreaterThan(0);

  // 切回后 A 的回合应继续到收口：行徽标/行内停止入口从「在途」变为空。
  let aStillRunningSeen = isInFlightRow(backRowA);
  let settledRow = backRowA;
  const settleDeadline = Date.now() + 300_000;
  while (Date.now() < settleDeadline) {
    await page.waitForTimeout(2000);
    settledRow = await sessionRowProbe(page, rowNeedle);
    const running = isInFlightRow(settledRow);
    if (!running) {
      break;
    }
    aStillRunningSeen = true;
  }
  await page.waitForTimeout(2000);
  const back2 = await snapshotStream(page);
  const bodyTextA = await page.evaluate(() => document.body.innerText);
  record("A.settled", {
    back2,
    aStillRunningSeen,
    settledRow,
    hasMarker: bodyTextA.includes(MARK_A),
    tail: back2.lastAnchorText.slice(-200),
    rows: await sidebarRows(page),
    serverA: await sessionRuntimeDigest(page, sessionA),
  });
  expect(
    back2.assistantChars,
    "H4: 切回后 A 正文不应缩水（增量无损）",
  ).toBeGreaterThanOrEqual(back1.assistantChars);

  // ── Phase 5：健康度 ──────────────────────────────────────────────────────
  // 失败请求分类：客户端取消（导航卸载 / 重建）单独留证；H5 只对硬失败与
  // console error 断言。
  const abortedByPath = abortedRequests.reduce<Record<string, number>>(
    (acc, item) => {
      const key = new URL(item.url).pathname.replace(
        /\/sessions\/[^/]+\//,
        "/sessions/*/",
      );
      acc[key] = (acc[key] ?? 0) + 1;
      return acc;
    },
    {},
  );
  const artifact = writeArtifact({ sessionA, abortedByPath });
  record("artifact", { file: artifact });
  record("H5", {
    hardFailures: failedRequests,
    abortedCount: abortedRequests.length,
    abortedByPath,
  });
  expect(
    failedRequests,
    "H5: 不应有硬失败请求（客户端主动取消的 ERR_ABORTED 不计）",
  ).toEqual([]);
  expect(consoleErrors, "H5: 不应有 console error").toEqual([]);
});
