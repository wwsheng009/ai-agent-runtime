// 跨会话 LSP 观测客户端（方案 §5.5）：
//   GET /api/runtime/observe/v1/events?event_type=lsp.request.finished&limit=N
//
// 契约（后端 internal/runtimeobserve/model.go 与 runtimeapi/observe_handlers.go）：
//   * 成功响应是统一 envelope（{ok, data, warnings, redaction}），data 为 QueryResult；
//   * 失败走既有扁平/嵌套错误体，由 fetchRuntimeJson 抛 RuntimeApiError（403=未授权/未启用）；
//   * `event_type` 是**精确匹配**（无通配符），因此"全部 lsp.*"由本模块按类型并发拉取再合并。
//
// 口径约定：面板只展示事件事实与窗口计数，**不在此重算比率指标**——比率口径单源于
// TUI `/lsp baseline` 与 web 会话内面板（§3.3），避免出现第二套数字。

import { buildRuntimeUrlWithQuery, fetchRuntimeJson } from "./shared";

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
  );
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
 */
export async function fetchLspObserveFeed(
  options: FetchLspObserveFeedOptions = {},
): Promise<LspObserveFeed> {
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

/** 扫描过程事实（后端 baseline.ScanStats）。 */
export type LspBaselineScan = {
  files: number;
  lines: number;
  malformed: number;
  skipped_old?: number;
  skipped_files?: number;
};

export type LspBaselineResponse = {
  schema_version: string;
  generated_at: string;
  /** "all" / "days=N" / "since=<RFC3339>"。 */
  window: string;
  scan: LspBaselineScan;
  stats: Record<string, unknown>;
  rows: LspBaselineRow[];
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
