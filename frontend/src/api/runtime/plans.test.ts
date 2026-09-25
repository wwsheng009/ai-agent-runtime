// 归档计划 API 客户端：URL 分段编码与形状归一化（不触网，纯函数断言）。

import { describe, expect, it } from "vitest";

import {
  buildStoredPlanDetailPath,
  normalizeStoredPlan,
  normalizeStoredPlanList,
} from "./plans";

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
