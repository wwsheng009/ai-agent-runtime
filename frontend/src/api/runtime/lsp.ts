// 跨会话 LSP 观测客户端（方案 §5.5）：
//   GET /api/runtime/observe/v1/events?event_type=lsp.request.finished&limit=N
//
// 契约（后端 internal/runtimeobserve/model.go 与 runtimeapi/observe_handlers.go）：
//   * 成功响应是统一 envelope（{ok, data, warnings, redaction}），data 为 QueryResult；
//   * 失败走既有扁平/嵌套错误体，由 fetchRuntimeJson 抛 RuntimeApiError。
//     **观测面不可用的真实信号是 404，不是 403**：runtime-server 只在
//     `runtime.observe.enabled=true` 时才注册 /observe/v1/* 整组路由
//     （backend/internal/api/runtimeapi/handler.go 的 ensureObserveService 守卫），
//     配置关闭时路由根本不存在，mux 直接返回 404；403 是「路由在但没给管理令牌」。
//     两者都表示「观测面不可用」，必须与「真的拉取失败」区分开，否则面板会把
//     "runtime request failed with status 404" 当故障文案直接抛给用户。
//   * `event_type` 是**精确匹配**（无通配符），因此"全部 lsp.*"由本模块按类型并发拉取再合并。
//
// 口径约定：面板只展示事件事实与窗口计数，**不在此重算比率指标**——比率口径单源于
// TUI `/lsp baseline` 与 web 会话内面板（§3.3），避免出现第二套数字。

import { buildRuntimeUrlWithQuery, fetchRuntimeJson, RuntimeApiError } from "./shared";

export const LSP_OBSERVE_EVENT_TYPES = [
  "lsp.request.finished",
  "lsp.server.state",
  "lsp.diagnostics.updated",
] as const;

export type LspObserveEventType = (typeof LSP_OBSERVE_EVENT_TYPES)[number];

/** 观测面一条已投影、已脱敏的 LSP 事件（字段名与后端 JSON 一致）。 */
export type LspObserveEvent = {
  observation_seq: number;
  timestamp: string;
  type: string;
  source?: string;
  correlation?: Record<string, unknown>;
  payload?: Record<string, unknown>;
};

/** `/observe/v1/events` 的数据域（QueryResult）。 */
export type LspObserveQueryResult = {
  events: LspObserveEvent[];
  after_seq: number;
  latest_seq: number;
  oldest_available_seq: number;
  next_cursor: string | null;
  partial: boolean;
  count: number;
};

type LspObserveEnvelope = {
  ok?: boolean;
  data?: LspObserveQueryResult | null;
};

export type ListLspObserveEventsOptions = {
  eventType: LspObserveEventType | string;
  limit?: number;
  sessionId?: string;
  adminToken?: string;
  signal?: AbortSignal;
};

function buildObserveHeaders(adminToken?: string) {
  const token = adminToken?.trim();
  if (!token) {
    return {} as Record<string, string>;
  }
  return {
    Authorization: `Bearer ${token}`,
  } satisfies Record<string, string>;
}

/**
 * 观测面「不可用」的状态码集合。
 *
 * 404/405：路由未注册（observe.enabled=false，或方法不匹配）——后端在
 *          runtimeapi/handler.go 里用 ensureObserveService() 做注册守卫，
 *          关闭时整组 /observe/v1/* 路由不存在。
 * 403：路由在，但缺管理令牌，或后端显式判定 observation disabled。
 *
 * 语义上都是「这台 runtime 没有对外提供观测面」，而不是数据拉取失败。
 */
const OBSERVE_UNAVAILABLE_STATUSES = new Set([403, 404, 405]);

/**
 * 观测面不可用。面板据此渲染「观测面不可用」并给出可执行的排查提示，
 * 而不是回落到 RuntimeApiError 的原始英文状态码文案。
 */
export class LspObserveUnavailableError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "LspObserveUnavailableError";
    this.status = status;
  }
}

/** 把 RuntimeApiError 的「不可用」状态码翻译成 LspObserveUnavailableError，其余原样抛出。 */
function rethrowObserveError(err: unknown): never {
  if (err instanceof RuntimeApiError && OBSERVE_UNAVAILABLE_STATUSES.has(err.status)) {
    throw new LspObserveUnavailableError(err.status, err.message);
  }
  throw err;
}

/**
 * 探测观测面是否可用（GET /api/runtime/observe/v1/capabilities）。
 *
 * 存在意义：事件查询按 event_type 精确匹配、没有通配符，所以
 * `fetchLspObserveFeed` 一次要并发打 3 个请求。观测面关闭时这 3 个请求
 * 全部必然 404，纯属浪费往返并把 3 条错误日志刷给用户。先探一次
 * capabilities，关闭时立刻短路，事件查询一个都不发。
 */
export async function probeLspObserveCapabilities(
  options: { adminToken?: string; signal?: AbortSignal } = {},
): Promise<void> {
  try {
    await fetchRuntimeJson(buildRuntimeUrlWithQuery("/api/runtime/observe/v1/capabilities", {}), {
      headers: buildObserveHeaders(options.adminToken),
      signal: options.signal,
    });
  } catch (err) {
    rethrowObserveError(err);
  }
}

export async function listLspObserveEvents(
  options: ListLspObserveEventsOptions,
): Promise<LspObserveQueryResult> {
  const envelope = await fetchRuntimeJson<LspObserveEnvelope>(
    buildRuntimeUrlWithQuery("/api/runtime/observe/v1/events", {
      event_type: options.eventType,
      limit: options.limit,
      session_id: options.sessionId,
    }),
    {
      headers: buildObserveHeaders(options.adminToken),
      signal: options.signal,
    },
  ).catch(rethrowObserveError);
  // 契约：成功必有 data。缺失时如实报错（不把"未采集"渲染成空结果）。
  if (!envelope || envelope.ok === false || !envelope.data) {
    throw new Error("observe response missing data");
  }
  return envelope.data;
}

export type LspObserveFeed = {
  /** 合并去重后的事件（时间倒序）。 */
  events: LspObserveEvent[];
  /** 按类型保留各查询的原始结果（分页/游标由调用方决定是否使用）。 */
  byType: Partial<Record<LspObserveEventType, LspObserveQueryResult>>;
};

export type FetchLspObserveFeedOptions = {
  limit?: number;
  sessionId?: string;
  adminToken?: string;
  signal?: AbortSignal;
};

/**
 * 拉取全部 LSP 事件类型并合并。任一类型失败即整体失败（诚实降级：
 * 面板按"观测面不可用"展示原因，而不是部分数据冒充全量）。
 *
 * 先探一次 capabilities：观测面关闭时（observe.enabled=false → 整组路由
 * 未注册 → 404）直接短路，避免并发发出 3 个注定失败的请求。探测只在
 * 不可用时改变行为——可用时事件查询照旧并发发出，不增加串行等待。
 */
export async function fetchLspObserveFeed(
  options: FetchLspObserveFeedOptions = {},
): Promise<LspObserveFeed> {
  await probeLspObserveCapabilities({
    adminToken: options.adminToken,
    signal: options.signal,
  });

  const results = await Promise.all(
    LSP_OBSERVE_EVENT_TYPES.map(async (eventType) => {
      const result = await listLspObserveEvents({
        eventType,
        limit: options.limit,
        sessionId: options.sessionId,
        adminToken: options.adminToken,
        signal: options.signal,
      });
      return [eventType, result] as const;
    }),
  );

  const byType: LspObserveFeed["byType"] = {};
  const merged: LspObserveEvent[] = [];
  const seen = new Set<number>();
  for (const [eventType, result] of results) {
    byType[eventType] = result;
    for (const event of result.events) {
      if (seen.has(event.observation_seq)) {
        continue;
      }
      seen.add(event.observation_seq);
      merged.push(event);
    }
  }
  merged.sort((a, b) => b.observation_seq - a.observation_seq);
  return { events: merged, byType };
}

// ---------------------------------------------------------------------------
// 舰队级基线（比率区块）：GET /api/runtime/analytics/lsp/baseline
//
// 后端直接复用 internal/lsp/baseline.Analyze（与 TUI `/lsp baseline`、方案 §4.3
// 登记表同一实现，两侧由同一 fixture 数字互锁），所以这里是**同一口径的数字**，
// 不是前端重算。未采集项由后端输出 "n/a" + 原因，前端原样展示（不补 0）。
// ---------------------------------------------------------------------------

/** §4.3 基线登记表的一行（后端 baseline.Row）。 */
export type LspBaselineRow = {
  metric: string;
  value: string;
  window: string;
  samples: string;
  conclusion: string;
  date: string;
};

/**
 * 扫描过程事实（后端 baseline.ScanStats）。
 *
 * 仅当 `LspBaselineResponse.source === "chat_logs"` 时才有意义：数据面读分析库
 * 时**不扫日志**，这些字段恒为 0 —— 那是"这条路不扫日志"，不是"没扫到东西"。
 * 缺席（undefined）同样读作未采集，不要臆造成 0。
 */
export type LspBaselineScan = {
  files: number;
  lines: number;
  malformed: number;
  skipped_old?: number;
  skipped_files?: number;
  /**
   * 增量索引可观测性（后端 incremental.go）。三个字段都带 omitempty：
   * 老构建、或本轮完全没用索引时缺席，UI 据此渲染「未使用索引」而不是 0。
   *
   * 注意：数据面走分析库时后端根本不扫日志，也就谈不上增量索引 —— 缺席是
   * 预期的，不代表"索引没起作用"。UI 必须结合 `source` 判断，不许单看缺席。
   * - reused_files：账目与文件完全一致，一个字节都没读；
   * - delta_files：只 seek 读了增量尾部；
   * - indexed_files：这轮结束后写进账本的文件数。
   */
  reused_files?: number;
  delta_files?: number;
  indexed_files?: number;
};

/**
 * 基线缓存可观测性（后端 lspBaselineCacheInfo）。
 *
 * 必须透出到 UI：后端对全量扫描加了 60s TTL 缓存，而本仓库有明确的
 * "不伪造新鲜度"纪律——命中缓存的报表不能看起来像刚算出来的。
 *   - hit=false：本次请求自己跑了一次全量扫描，age_seconds 必为 0；
 *   - hit=true 且 age>0：数字来自 TTL 内的缓存条目，age 是其真实年龄；
 *   - hit=true 且 age=0：并发请求合并到了同一次在途扫描，数字确实是新的。
 * 字段为可选：旧后端不返回时按"未采集"处理，不臆造命中与否。
 */
export type LspBaselineCache = {
  hit?: boolean;
  age_seconds?: number;
  ttl_seconds?: number;
};

export type LspBaselineResponse = {
  schema_version: string;
  generated_at: string;
  /** "all" / "days=N" / "since=<RFC3339>"。 */
  window: string;
  /**
   * 事实源，由**后端**声明，UI 不许猜：
   * - "analytics_db"：读分析库（当前唯一的数据面路径）。scan.* 恒为 0。
   * - "chat_logs"：回扫会话日志。scan.* 与增量索引字段才有意义。
   *
   * 字段为可选：老后端不返回时按"未声明"处理，UI 退回中性措辞，不假装知道。
   */
  source?: "analytics_db" | "chat_logs";
  scan: LspBaselineScan;
  stats: Record<string, unknown>;
  rows: LspBaselineRow[];
  cache?: LspBaselineCache;
};

export type GetLspBaselineOptions = {
  /** 时间窗（天）；与 since 互斥，缺省 = 全窗口。 */
  days?: number;
  since?: string;
  adminToken?: string;
  signal?: AbortSignal;
};

export async function getLspBaseline(
  options: GetLspBaselineOptions = {},
): Promise<LspBaselineResponse> {
  return fetchRuntimeJson<LspBaselineResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/analytics/lsp/baseline", {
      days: options.days,
      since: options.since,
    }),
    {
      headers: buildObserveHeaders(options.adminToken),
      signal: options.signal,
    },
  );
}
