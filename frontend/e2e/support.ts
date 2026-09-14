/**
 * e2e 测试基建（P0-7 测试基建三件套）。
 *
 * 三条纪律：
 * 1. **构建产物单一来源**：e2e 只跑 `dist/`，不跑 dev server；缺失即 fail-fast，
 *    不留白屏超时这种难读的失败形态（`requireDist`）。
 * 2. **端口不写死**：mock/preview 端口由 OS 分配（`probeFreePort`），并行跑两套
 *    e2e 或本机已有 5193/8111 占用时不再互相踩端口。
 * 3. **失败必留证据**：失败用例写全页截图到 `.artifacts/`（`saveFailureShot`），
 *    配合 playwright 的 trace 保留现场。
 *
 * 另提供 seed 能力（会话/主题/语言/历史），供用例与 global-setup 复用，
 * 避免每个 spec 各写一份 localStorage 与 mock 状态准备逻辑。
 */
import { existsSync, mkdirSync } from "node:fs";
import { createServer } from "node:net";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import type { APIRequestContext, Page } from "@playwright/test";

const FRONTEND_ROOT = fileURLToPath(new URL("..", import.meta.url));

export const DIST_INDEX = join(FRONTEND_ROOT, "dist", "index.html");

export const ARTIFACTS_DIR = join(FRONTEND_ROOT, ".artifacts");

/** 应用设置真源 key（与 `core/settings/local.ts` 的 APP_SETTINGS_STORAGE_KEY 同源）。 */
export const APP_SETTINGS_STORAGE_KEY = "ai-agent-runtime.workspace.settings";

/**
 * 浏览器 locale 固定为 en-US：应用默认设置 `localization.locale: "system"`
 * 会按系统语言解析 UI 文案，固定后才能让现有英文选择器与 mock 文案稳定；
 * 需要中文 UI 的用例用 `seedLanguage(page, "zh-CN")` 显式覆盖。
 */
export const DEFAULT_LOCALE = "en-US";

/** 固定时区：日期/相对时间断言不随 CI 机器漂移。 */
export const DEFAULT_TIMEZONE = "Asia/Shanghai";

/** 视口种子：1440×900（桌面三栏展开的稳定形态）。 */
export const DEFAULT_VIEWPORT = { width: 1440, height: 900 } as const;

/** 缺 dist 时抛出可执行提示，而不是让 webServer 起一个 404 的静态目录。 */
export function requireDist(): void {
  if (!existsSync(DIST_INDEX)) {
    throw new Error(
      "缺少 frontend/dist/index.html：e2e 基于构建产物运行。请先执行 `npm run build`（或 `npm run test:e2e` 前置构建）。",
    );
  }
}

/** 向 OS 申请一个空闲端口，随即释放；调用方拿到的是「刚空出来」的端口。 */
export function probeFreePort(): Promise<number> {
  return new Promise((resolvePort, reject) => {
    const probe = createServer();
    probe.once("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const address = probe.address();
      if (address === null || typeof address === "string") {
        probe.close(() => reject(new Error("port probe returned no address")));
        return;
      }
      probe.close(() => resolvePort(address.port));
    });
  });
}

function sanitizeName(name: string): string {
  return name
    .replace(/[^\w.-]+/g, "_")
    .replace(/^_+|_+$/g, "")
    .slice(0, 120);
}

/** 失败用例全页截图，落到 `.artifacts/e2e-failures/`（gitignored）。 */
export async function saveFailureShot(page: Page, name: string): Promise<string> {
  const dir = join(ARTIFACTS_DIR, "e2e-failures");
  mkdirSync(dir, { recursive: true });
  const file = join(dir, `${sanitizeName(name) || "failure"}-${Date.now()}.png`);
  await page.screenshot({ path: file, fullPage: true }).catch(() => undefined);
  return file;
}

/** 事件夹具：mock 的 `/api/_test/runtime-events` 契约（会话轨迹注入）。 */
export type SeedRuntimeEvent = {
  type: string;
  payload?: Record<string, unknown>;
  seq?: number;
};

/**
 * 重置 mock 运行时状态（会话/事件/故障开关）。
 *
 * 每个用例的前置动作，保证「上一个用例留下的会话」不会改变列表断言。
 */
export async function resetMockState(request: APIRequestContext): Promise<void> {
  const response = await request.post("/api/_test/reset");
  if (!response.ok()) {
    throw new Error(`mock reset failed: ${response.status()} ${await response.text()}`);
  }
}

/** seed 会话：返回 mock 生成的 session id。 */
export async function seedSession(
  request: APIRequestContext,
  input: {
    id?: string;
    title?: string;
    /** P2-1A：检索维度（`POST /api/runtime/sessions/search` 的服务端过滤）。 */
    state?: string;
    userId?: string;
    tags?: string[];
    metadata?: Record<string, unknown>;
  } = {},
): Promise<string> {
  const response = await request.post("/api/runtime/sessions", {
    data: {
      ...(input.id ? { session_id: input.id, id: input.id } : {}),
      ...(input.title ? { title: input.title } : {}),
      ...(input.state ? { state: input.state } : {}),
      ...(input.userId ? { user_id: input.userId } : {}),
      ...(input.tags ? { tags: input.tags } : {}),
      ...(input.metadata ? { metadata: input.metadata } : {}),
    },
  });
  if (!response.ok()) {
    throw new Error(`seedSession failed: ${response.status()} ${await response.text()}`);
  }
  const body = (await response.json()) as Record<string, unknown>;
  const sessionId =
    (typeof body.session_id === "string" && body.session_id) ||
    (typeof body.id === "string" && body.id) ||
    input.id;
  if (!sessionId) {
    throw new Error(`seedSession response has no session id: ${JSON.stringify(body)}`);
  }
  return sessionId;
}

/** seed 会话历史事件（`runtime/events` 增量接口的数据源）；返回最后一条事件的 seq。 */
export async function seedRuntimeEvents(
  request: APIRequestContext,
  sessionId: string,
  events: SeedRuntimeEvent[],
): Promise<number> {
  if (events.length === 0) return 0;
  const response = await request.post("/api/_test/runtime-events", {
    data: { session_id: sessionId, events },
  });
  if (!response.ok()) {
    throw new Error(`seedRuntimeEvents failed: ${response.status()} ${await response.text()}`);
  }
  const body = (await response.json()) as { seq?: unknown };
  if (typeof body.seq !== "number") {
    throw new Error(`seedRuntimeEvents response has no seq: ${JSON.stringify(body)}`);
  }
  return body.seq;
}

/**
 * 等 mock 把 assistant 回复落进会话历史（与后端同口径：turn 收尾的 `done` 帧才写库）。
 *
 * 正文 chunk 先于 `done` 到达：对着「刚看到正文」就 `reload()` 的用例，刷新可能落在
 * 落库窗口内，恢复出的历史只有用户消息（P3-1 曾因此抖动）。用例应在刷新前显式等待。
 */
export async function waitForAssistantHistory(
  request: APIRequestContext,
  sessionId: string,
  text: string,
): Promise<void> {
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    const response = await request.get(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/history`,
    );
    if (response.ok()) {
      const body = (await response.json()) as {
        history?: Array<{ role?: string; content?: string }>;
      };
      const recorded = (body.history ?? []).some(
        (entry) => entry.role === "assistant" && (entry.content ?? "").includes(text),
      );
      if (recorded) return;
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`timed out waiting for assistant history to include: ${text}`);
}

/**
 * seed 后台任务（P2-1A）：jobs 数组按后端 `background.Job` 的序列化字段名
 * （`ID` / `Status` / `Command` / `StartedAt` / `FinishedAt` / `ExitCode`，可选 `Output`）
 * 提供；mock 按会话保存，列表端点按 `session_id` 过滤。
 */
export async function seedJobs(
  request: APIRequestContext,
  sessionId: string,
  jobs: Array<Record<string, unknown>>,
): Promise<void> {
  const response = await request.post("/api/_test/jobs", {
    data: { session_id: sessionId, jobs },
  });
  if (!response.ok()) {
    throw new Error(`seedJobs failed: ${response.status()} ${await response.text()}`);
  }
}

/**
 * seed 会话历史（`GET /api/runtime/sessions/:id/history` 的数据源）。
 *
 * 条目按后端 `types.Message` 的 JSON 形态提供（`role` / `content` /
 * `tool_call_id` / `tool_calls` / `metadata`）；工具回执条目（`role: "tool"`）
 * 只有配上对应的 `tool_calls` 才能还原出具体工具名与入参，因此用例需成对注入。
 */
export async function seedSessionHistory(
  request: APIRequestContext,
  sessionId: string,
  history: Array<Record<string, unknown>>,
): Promise<void> {
  const response = await request.post("/api/_test/history", {
    data: { session_id: sessionId, history },
  });
  if (!response.ok()) {
    throw new Error(`seedSessionHistory failed: ${response.status()} ${await response.text()}`);
  }
}

/**
 * seed 运行时模型目录（P2-7 子片 3）：`GET /api/runtime/models` 的 mock 数据源。
 *
 * 目录原文透传（不排序、不补默认模型）；未 seed 时 mock 返回空目录，前端据此
 * 走「目录未就绪」路径（`/model` 无候选、弹窗空态），不伪造模型名。
 */
export async function seedRuntimeModels(
  request: APIRequestContext,
  catalog: {
    providers: Array<Record<string, unknown>>;
    default_provider?: string;
    default_model?: string;
  },
): Promise<void> {
  const response = await request.post("/api/_test/models", { data: catalog });
  if (!response.ok()) {
    throw new Error(`seedRuntimeModels failed: ${response.status()} ${await response.text()}`);
  }
  // mock 对未知 API 会回 200 `{}`：缺 `ok` 即说明注入端点缺失，必须报错而不是静默空跑
  // （否则用例会退化成「目录为空」的假绿）。
  const body = (await response.json()) as { ok?: unknown };
  if (body?.ok !== true) {
    throw new Error(`seedRuntimeModels endpoint missing: ${JSON.stringify(body)}`);
  }
}

/**
 * seed 运行时文件（P2-1A）：`POST /api/runtime/fs/read-file` 的 mock 数据源。
 *
 * `content` 为 UTF-8 文本便捷写法；二进制/坏编码场景用 `dataBase64`；
 * 未登记的路径 mock 返回 404，用于覆盖「读取失败如实呈现」。
 */
export async function seedRuntimeFiles(
  request: APIRequestContext,
  files: Array<{
    path: string;
    content?: string;
    dataBase64?: string;
    byteCount?: number;
  }>,
): Promise<void> {
  const response = await request.post("/api/_test/files", {
    data: {
      files: files.map((file) => ({
        path: file.path,
        ...(file.content !== undefined ? { content: file.content } : {}),
        ...(file.dataBase64 !== undefined ? { data_base64: file.dataBase64 } : {}),
        ...(file.byteCount !== undefined ? { byte_count: file.byteCount } : {}),
      })),
    },
  });
  if (!response.ok()) {
    throw new Error(`seedRuntimeFiles failed: ${response.status()} ${await response.text()}`);
  }
}

/** seed 设置：顶层浅合并进 localStorage 记录（随首屏启动脚本生效）。 */
export async function seedSettings(
  page: Page,
  patch: Record<string, unknown>,
  storageKey = APP_SETTINGS_STORAGE_KEY,
): Promise<void> {
  await page.addInitScript(
    ([key, value]) => {
      const raw = window.localStorage.getItem(key!);
      const current = raw ? (JSON.parse(raw) as Record<string, unknown>) : {};
      window.localStorage.setItem(key!, JSON.stringify({ ...current, ...(value as object) }));
    },
    [storageKey, patch] as const,
  );
}

/** seed 主题/字号/强调色：合并进设置的 `appearance` 子对象，不动其它键。 */
export async function seedAppearance(
  page: Page,
  appearance: Record<string, unknown>,
  storageKey = APP_SETTINGS_STORAGE_KEY,
): Promise<void> {
  await page.addInitScript(
    ([key, value]) => {
      const raw = window.localStorage.getItem(key!);
      const current = raw ? (JSON.parse(raw) as Record<string, unknown>) : {};
      const currentAppearance =
        (current.appearance as Record<string, unknown> | undefined) ?? {};
      window.localStorage.setItem(
        key!,
        JSON.stringify({
          ...current,
          appearance: { ...currentAppearance, ...(value as object) },
        }),
      );
    },
    [storageKey, appearance] as const,
  );
}

/** seed 语言：合并进设置的 `localization.locale`（与 `resolveLocalePreference` 同源）。 */
export async function seedLanguage(
  page: Page,
  locale: string,
  storageKey = APP_SETTINGS_STORAGE_KEY,
): Promise<void> {
  await page.addInitScript(
    ([key, value]) => {
      const raw = window.localStorage.getItem(key!);
      const current = raw ? (JSON.parse(raw) as Record<string, unknown>) : {};
      const currentLocalization =
        (current.localization as Record<string, unknown> | undefined) ?? {};
      window.localStorage.setItem(
        key!,
        JSON.stringify({
          ...current,
          localization: { ...currentLocalization, locale: value },
        }),
      );
    },
    [storageKey, locale] as const,
  );
}

/**
 * 在「当前文档」写入设置（顶层浅合并），调用方随后 `reload()` 生效。
 *
 * 与 `seedSettings` 的分工：seed* 走 `addInitScript`，用于「下一次导航」；
 * apply* 立即落盘，用于同一用例内的多轮切换（例如主题矩阵逐档 reload），
 * 不会把上一轮的值在后续导航里反复注入。
 */
export async function applySettings(
  page: Page,
  patch: Record<string, unknown>,
  storageKey = APP_SETTINGS_STORAGE_KEY,
): Promise<void> {
  await page.evaluate(
    ({ key, value }) => {
      const raw = window.localStorage.getItem(key);
      const current = raw ? (JSON.parse(raw) as Record<string, unknown>) : {};
      window.localStorage.setItem(key, JSON.stringify({ ...current, ...(value as object) }));
    },
    { key: storageKey, value: patch },
  );
}

/** 在「当前文档」合并 `appearance` 子对象（同测内多轮主题/字号切换用）。 */
export async function applyAppearance(
  page: Page,
  appearance: Record<string, unknown>,
  storageKey = APP_SETTINGS_STORAGE_KEY,
): Promise<void> {
  await page.evaluate(
    ({ key, value }) => {
      const raw = window.localStorage.getItem(key);
      const current = raw ? (JSON.parse(raw) as Record<string, unknown>) : {};
      const currentAppearance =
        (current.appearance as Record<string, unknown> | undefined) ?? {};
      window.localStorage.setItem(
        key,
        JSON.stringify({
          ...current,
          appearance: { ...currentAppearance, ...(value as object) },
        }),
      );
    },
    { key: storageKey, value: appearance },
  );
}

/** 清空设置记录（当前文档；随后 `reload()` 回到应用默认值）。 */
export async function clearStoredSettings(
  page: Page,
  storageKey = APP_SETTINGS_STORAGE_KEY,
): Promise<void> {
  await page.evaluate((key) => window.localStorage.removeItem(key), storageKey);
}

/** `.artifacts` 下的路径工具（供 global-setup 与用例写固定命名的产物）。 */
export function artifactPath(...segments: string[]): string {
  return join(ARTIFACTS_DIR, ...segments);
}

/** frontend 根目录（供需要读 dist/package.json 的调用方）。 */
export function frontendRoot(): string {
  return FRONTEND_ROOT;
}

/** 目录创建工具（保证 `.artifacts` 子目录存在）。 */
export function ensureDir(path: string): string {
  mkdirSync(dirname(path), { recursive: true });
  return path;
}
