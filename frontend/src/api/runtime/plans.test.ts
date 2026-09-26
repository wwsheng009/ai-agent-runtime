// 归档计划 API 客户端：URL 分段编码与形状归一化（不触网，纯函数断言）。

import { describe, expect, it } from "vitest";

import {
  buildStoredPlanCommentsPath,
  buildStoredPlanDiffPath,
  buildStoredPlanDetailPath,
  buildStoredPlanReopenPath,
  isStoredPlanReopenConflict,
  normalizePlanComment,
  normalizePlanCommentList,
  normalizePlanDiffResult,
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

describe("行级评论端点", () => {
  it("路径复用详情端点的分段编码，id 允许含 '/'", () => {
    expect(buildStoredPlanCommentsPath("ai-agent-runtime/plan")).toBe(
      "/api/runtime/plans/ai-agent-runtime/plan/comments",
    );
    expect(buildStoredPlanCommentsPath("my project/plan #1.md")).toBe(
      "/api/runtime/plans/my%20project/plan%20%231.md/comments",
    );
  });

  it("normalizePlanComment：status 与 current_* 原样透传（前端不重算重放）", () => {
    const comment = normalizePlanComment({
      id: "c-1",
      revision: 2,
      start_line: 3,
      end_line: 4,
      excerpt: "1. ship it",
      body: "补上回滚风险",
      status: "moved",
      current_revision: 3,
      current_start_line: 5,
      current_end_line: 6,
    });

    expect(comment).toMatchObject({
      id: "c-1",
      revision: 2,
      status: "moved",
      current_revision: 3,
      current_start_line: 5,
      current_end_line: 6,
    });
    expect(comment?.author).toBeUndefined();

    // 没有 id 的行丢弃（形状不整，不猜语义）。
    expect(normalizePlanComment({ body: "无 id" })).toBeNull();
    expect(normalizePlanComment(null)).toBeNull();
  });

  it("normalizePlanCommentList：缺字段给稳定形状", () => {
    const list = normalizePlanCommentList({
      plan_id: "proj/plan",
      revision: 3,
      latest_revision: 3,
      comments: [{ id: "c-1", status: "orphaned" }, { body: "丢弃" }],
    });

    expect(list.count).toBe(1);
    expect(list.comments[0]).toMatchObject({ id: "c-1", status: "orphaned" });
    expect(normalizePlanCommentList(null)).toEqual({
      plan_id: "",
      revision: 0,
      latest_revision: 0,
      comments: [],
      count: 0,
    });
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

// 轮次差异 API 客户端：路径编码 + 结果归一化（不触网，纯函数断言）。
describe("getRuntimePlanDiff helpers", () => {
  it("buildStoredPlanDiffPath 在详情路径后追加 /diff 并逐段编码", () => {
    expect(buildStoredPlanDiffPath("proj/plan")).toBe("/api/runtime/plans/proj/plan/diff");
    expect(buildStoredPlanDiffPath("my project/plan #1.md")).toBe(
      "/api/runtime/plans/my%20project/plan%20%231.md/diff",
    );
  });

  it("normalizePlanDiffResult 归一形状并按布尔默认 false", () => {
    const result = normalizePlanDiffResult({
      plan_id: "proj/plan",
      from_version: 1,
      to_version: 3,
      added: 4,
      removed: 2,
      old_lines: 40,
      new_lines: 42,
      text: "--- v1\n+++ v3\n",
    });

    expect(result).toMatchObject({
      plan_id: "proj/plan",
      from_version: 1,
      to_version: 3,
      identical: false,
      added: 4,
      removed: 2,
      coarse: false,
      truncated: false,
    });
    expect(normalizePlanDiffResult({ from_version: 1 })).toBeNull();
  });
});
