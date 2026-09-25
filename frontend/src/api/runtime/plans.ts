// 归档计划（planstore）阅读面：GET /api/runtime/plans 系列端点的客户端封装。
//
// 端点事实（backend/internal/api/runtimeapi/plans_handlers.go）：
//   * GET /api/runtime/plans            → { plans: [...], count: N }（按 updated_at 新→旧）
//   * GET /api/runtime/plans/{id:.*}    → 单条记录 + 最新快照正文（content/content_available/…）
//
// 关键约束：**plan id 允许含 "/"**（形如 `ai-agent-runtime/plan`，见 planstore.IDFor），
// 因此详情 URL 必须按 "/" 分段逐段 encodeURIComponent —— 整串 encode 会把分隔符编成
// %2F，后端 mux 的 `{id:.*}` 路由与 store.Get 都拿不到原始 id（404）。

import type {
  RuntimeStoredPlan,
  RuntimeStoredPlanListResponse,
} from "@/types/runtime";

import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

export const RUNTIME_PLANS_PATH = "/api/runtime/plans";

/**
 * 把 plan id 拼成详情端点路径：保留 "/" 作为段分隔，其余字符逐段编码。
 * 空段（前导 / 连续 / 尾部 /）被丢弃，避免生成 `//` 路由。
 */
export function buildStoredPlanDetailPath(id: string) {
  const segments = id
    .split("/")
    .map((segment) => segment.trim())
    .filter((segment) => segment.length > 0)
    .map((segment) => encodeURIComponent(segment));

  return `${RUNTIME_PLANS_PATH}/${segments.join("/")}`;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function readString(value: unknown) {
  return typeof value === "string" ? value : "";
}

function readOptionalString(value: unknown) {
  const text = readString(value).trim();
  return text === "" ? undefined : text;
}

function readNumber(value: unknown, fallback = 0) {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

/**
 * 只做形状归一化：缺字段给空值，不猜语义、不覆盖后端状态码。
 * 后端省略 rounds/快照字段时统一成空数组 / false，渲染层不必再判 undefined。
 */
export function normalizeStoredPlan(raw: unknown): RuntimeStoredPlan | null {
  const record = asRecord(raw);
  const id = readOptionalString(record?.id);
  if (!record || !id) {
    return null;
  }

  const rounds = Array.isArray(record.rounds)
    ? record.rounds
        .map((round) => {
          const entry = asRecord(round);
          if (!entry) {
            return null;
          }
          return {
            version: readNumber(entry.version),
            decision: readOptionalString(entry.decision),
            notes: readOptionalString(entry.notes),
            source: readOptionalString(entry.source),
            snapshot: readOptionalString(entry.snapshot),
            created_at: readOptionalString(entry.created_at),
          };
        })
        .filter((round): round is NonNullable<typeof round> => round !== null)
    : [];

  const content = readString(record.content);

  return {
    id,
    session_id: readOptionalString(record.session_id),
    project_slug: readOptionalString(record.project_slug),
    project_path: readOptionalString(record.project_path),
    project: readOptionalString(record.project),
    plan_path: readOptionalString(record.plan_path),
    title: readOptionalString(record.title),
    status: readOptionalString(record.status) ?? "",
    version: readNumber(record.version),
    rounds,
    created_at: readOptionalString(record.created_at),
    updated_at: readOptionalString(record.updated_at),
    content,
    content_available:
      record.content_available === true ||
      (record.content_available === undefined && content !== ""),
    content_truncated: record.content_truncated === true,
    content_error: readOptionalString(record.content_error),
  };
}

export function normalizeStoredPlanList(raw: unknown): RuntimeStoredPlanListResponse {
  const record = asRecord(raw);
  const plans = Array.isArray(record?.plans)
    ? record.plans
        .map((plan) => normalizeStoredPlan(plan))
        .filter((plan): plan is RuntimeStoredPlan => plan !== null)
    : [];

  return {
    plans,
    count: readNumber(record?.count, plans.length),
  };
}

export async function listRuntimePlans(): Promise<RuntimeStoredPlanListResponse> {
  const payload = await fetchRuntimeJson<unknown>(buildRuntimeUrl(RUNTIME_PLANS_PATH), {
    headers: {
      Accept: "application/json",
    },
  });

  return normalizeStoredPlanList(payload);
}

/** 读取单条归档计划 + 最新快照正文；未知 id 由后端 404（RuntimeApiError）。 */
export async function getRuntimePlan(id: string): Promise<RuntimeStoredPlan> {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(buildStoredPlanDetailPath(id)),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );

  const plan = normalizeStoredPlan(payload);
  if (!plan) {
    throw new Error("runtime plan response is missing an id");
  }

  return plan;
}
