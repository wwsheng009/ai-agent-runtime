// 归档计划 API 客户端：URL 分段编码与形状归一化（不触网，纯函数断言）。

import { describe, expect, it } from "vitest";

import {
  buildStoredPlanDetailPath,
  buildStoredPlanReopenPath,
  isStoredPlanReopenConflict,
  normalizeStoredPlan,
  normalizeStoredPlanList,
  normalizePlanReopenResult,
  readStoredPlanReopenHint,
} from "./plans";
import { RuntimeApiError } from "./shared";

describe("buildStoredPlanDetailPath", () => {
  it("保留 '/' 作为段分隔，逐段 encodeURIComponent", () => {
    expect(buildStoredPlanDetailPath("ai-agent-runtime/plan")).toBe(
      "/api/runtime/plans/ai-agent-runtime/plan",
    );
  });

  it("段内需转义的字符逐段编码（空格 / # / 中文）", () => {
    expect(buildStoredPlanDetailPath("my project/plan #1.md")).toBe(
      "/api/runtime/plans/my%20project/plan%20%231.md",
    );
    expect(buildStoredPlanDetailPath("项目/计划.md")).toBe(
      "/api/runtime/plans/%E9%A1%B9%E7%9B%AE/%E8%AE%A1%E5%88%92.md",
    );
  });

  it("丢弃空段，不生成 '//' 路由", () => {
    expect(buildStoredPlanDetailPath("/proj//plan/")).toBe(
      "/api/runtime/plans/proj/plan",
    );
  });
});

describe("normalizeStoredPlanList / normalizeStoredPlan", () => {
  it("缺失字段归一为稳定形状（rounds=[] / content=false）", () => {
    const plan = normalizeStoredPlan({
      id: "proj/plan",
      status: "pending",
      version: 2,
    });

    expect(plan?.rounds).toEqual([]);
    expect(plan?.content).toBe("");
    expect(plan?.content_available).toBe(false);
    expect(plan?.content_truncated).toBe(false);
  });

  it("详情正文与轮次原样透传", () => {
    const plan = normalizeStoredPlan({
      id: "proj/plan",
      status: "approved",
      version: 3,
      content: "# Plan",
      content_available: true,
      content_truncated: true,
      rounds: [
        {
          version: 1,
          decision: "request_changes",
          notes: "补充回滚方案",
          source: "user",
          created_at: "2026-09-25T10:00:00Z",
        },
      ],
    });

    expect(plan?.content).toBe("# Plan");
    expect(plan?.content_truncated).toBe(true);
    expect(plan?.rounds).toEqual([
      {
        version: 1,
        decision: "request_changes",
        notes: "补充回滚方案",
        source: "user",
        snapshot: undefined,
        created_at: "2026-09-25T10:00:00Z",
      },
    ]);
  });

  it("缺 id 的记录被丢弃；count 缺失时回退到实际条数", () => {
    expect(normalizeStoredPlan({ status: "pending" })).toBeNull();

    const list = normalizeStoredPlanList({
      plans: [{ id: "proj/plan", status: "pending", version: 1 }, { status: "pending" }],
    });

    expect(list.plans.map((plan) => plan.id)).toEqual(["proj/plan"]);
    expect(list.count).toBe(1);
  });
});

// 归档回灌（reopen）：URL/请求体形状、结果归一化、409 冲突识别（纯函数，不触网）。
describe("reopenRuntimePlan helpers", () => {
  it("buildStoredPlanReopenPath 走会话内 POST 端点并编码会话 id", () => {
    expect(buildStoredPlanReopenPath("session-1")).toBe(
      "/api/runtime/sessions/session-1/plan/reopen",
    );
    expect(buildStoredPlanReopenPath(" session/2 ")).toBe(
      "/api/runtime/sessions/session%2F2/plan/reopen",
    );
  });

  it("normalizePlanReopenResult 归一形状并保留 created/unchanged/forced", () => {
    const result = normalizePlanReopenResult({
      plan_id: "proj/plan",
      version: 4,
      bytes: 128,
      display_path: "docs/plan.md",
      created: true,
      forced: false,
    });

    expect(result).toMatchObject({
      plan_id: "proj/plan",
      version: 4,
      bytes: 128,
      display_path: "docs/plan.md",
      created: true,
      unchanged: false,
      forced: false,
    });
    expect(normalizePlanReopenResult({ version: 1 })).toBeNull();
  });

  it("409 + conflict=true 才算冲突，hint 优先于 error", () => {
    const conflict = new RuntimeApiError(409, {
      conflict: true,
      error: "planmode: plan file differs from the archived snapshot",
      hint: "确认覆盖后带 force=true 重试",
    } as never);

    expect(isStoredPlanReopenConflict(conflict)).toBe(true);
    expect(readStoredPlanReopenHint(conflict)).toBe("确认覆盖后带 force=true 重试");

    const notFound = new RuntimeApiError(404, { error: "missing" } as never);
    expect(isStoredPlanReopenConflict(notFound)).toBe(false);
    expect(readStoredPlanReopenHint(notFound)).toBe("missing");
    expect(isStoredPlanReopenConflict(new Error("network"))).toBe(false);
  });
});
