// @vitest-environment jsdom

// 计划归档面（报告 §4.5）：列表渲染 / 详情打开 / 空态 / 错误态 + 重试 / 事件驱动刷新。
//
// 与 artifact-panel*.test.tsx 同风格：mock 掉 runtime API（唯一网络边界），真实渲染面组件与 hook，
// 断言只落在用户可见文本上；`plan_review_requested` 的刷新口径由渲染期 props 变化模拟。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeAll, afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { codeHighlightingReady } from "@/components/ui/code-highlighting";
import { WORKSPACE_PANEL_SURFACES } from "@/components/workspace/panel-registry";
import { RuntimeApiError } from "@/lib/runtime-api";
import type { RuntimeStoredPlan } from "@/types/runtime";

import { ArtifactPanelPlansSurface } from "./artifact-panel-plans-surface";

const { getRuntimePlanDiffMock, getRuntimePlanMock, listRuntimePlansMock, reopenRuntimePlanMock } =
  vi.hoisted(() => ({
    getRuntimePlanDiffMock: vi.fn(),
    getRuntimePlanMock: vi.fn(),
    listRuntimePlansMock: vi.fn(),
    reopenRuntimePlanMock: vi.fn(),
  }));

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return {
    ...actual,
    getRuntimePlanDiff: getRuntimePlanDiffMock,
    getRuntimePlan: getRuntimePlanMock,
    listRuntimePlans: listRuntimePlansMock,
    reopenRuntimePlan: reopenRuntimePlanMock,
  };
});

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

let container: HTMLDivElement;
let root: Root | null = null;

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

async function settle() {
  for (let index = 0; index < 4; index += 1) {
    await act(async () => {
      await flush();
    });
  }
}

function plan(id: string, overrides: Partial<RuntimeStoredPlan> = {}): RuntimeStoredPlan {
  return {
    id,
    status: "pending",
    version: 1,
    rounds: [],
    content: "",
    content_available: false,
    ...overrides,
  };
}

async function renderSurface(
  props: { sessionId?: string; lastRuntimeEventType?: string; runtimeEventCount?: number } = {},
) {
  await act(async () => {
    root?.render(
      <ArtifactPanelPlansSurface
        lastRuntimeEventType={props.lastRuntimeEventType}
        runtimeEventCount={props.runtimeEventCount}
        sessionId={props.sessionId ?? ""}
      />,
    );
  });
  await settle();
}

function findButtonByText(text: string) {
  return Array.from(container.querySelectorAll("button")).find((button) =>
    button.textContent?.includes(text),
  );
}

function clickButtonByText(text: string) {
  const button = findButtonByText(text);
  expect(button).toBeInstanceOf(HTMLButtonElement);
  act(() => {
    button?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

describe("ArtifactPanelPlansSurface", () => {
  beforeAll(async () => {
    await codeHighlightingReady;
  });

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    getRuntimePlanDiffMock.mockReset();
    getRuntimePlanMock.mockReset();
    listRuntimePlansMock.mockReset();
    reopenRuntimePlanMock.mockReset();

    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("注册表登记了 plans 自包含面", () => {
    const spec = WORKSPACE_PANEL_SURFACES.find((entry) => entry.id === "plans");

    expect(spec).toBeDefined();
    expect(spec?.surface).toBeDefined();
    expect(spec?.requiresSession).toBe(false);
  });

  it("列表：状态徽标中文标签 + 版本 + 更新时间 + 计划路径 + 会话/项目", async () => {
    listRuntimePlansMock.mockResolvedValue({
      plans: [
        plan("proj/plan", {
          status: "pending",
          version: 2,
          plan_path: "docs/plan.md",
          session_id: "session-1",
          project_slug: "proj",
          updated_at: "2026-09-25T10:00:00Z",
        }),
        plan("proj/done", {
          status: "approved",
          version: 1,
          plan_path: "docs/done.md",
          project: "proj",
          updated_at: "2026-09-24T10:00:00Z",
        }),
        plan("proj/quit", {
          status: "not_implemented",
          version: 4,
          plan_path: "docs/quit.md",
        }),
      ],
      count: 3,
    });

    await renderSurface();

    expect(container.textContent).toContain("计划归档");
    expect(container.textContent).toContain("待评审");
    expect(container.textContent).toContain("已批准");
    expect(container.textContent).toContain("未实施");
    expect(container.textContent).toContain("v2");
    expect(container.textContent).toContain("docs/plan.md");
    expect(container.textContent).toContain("session-1");
    expect(container.textContent).toContain("proj");
    expect(container.textContent).toContain("更新于");
    // 未知状态不猜语义：原文透传而不是显示空白。
    expect(container.textContent).not.toContain("undefined");
  });

  it("详情：轮次（决策 + 备注摘要）与最新快照正文，可返回列表", async () => {
    listRuntimePlansMock.mockResolvedValue({
      plans: [plan("proj/plan", { plan_path: "docs/plan.md" })],
      count: 1,
    });
    getRuntimePlanMock.mockResolvedValue(
      plan("proj/plan", {
        status: "approved",
        version: 3,
        plan_path: "docs/plan.md",
        session_id: "session-1",
        project_slug: "proj",
        content: "# 计划\n\n- 步骤一\n- 步骤二",
        content_available: true,
        rounds: [
          {
            version: 1,
            decision: "request_changes",
            notes: "补充回滚方案",
            source: "user",
            created_at: "2026-09-25T09:00:00Z",
          },
          { version: 2, decision: "approve", notes: "可以实施" },
        ],
      }),
    );

    await renderSurface();
    clickButtonByText("docs/plan.md");
    await settle();

    expect(getRuntimePlanMock).toHaveBeenCalledWith("proj/plan");
    expect(container.textContent).toContain("评审轮次");
    expect(container.textContent).toContain("请求修改");
    expect(container.textContent).toContain("补充回滚方案");
    expect(container.textContent).toContain("批准");
    expect(container.textContent).toContain("最新快照");
    expect(container.textContent).toContain("步骤一");
    expect(container.querySelector("h1")?.textContent).toBe("计划");

    clickButtonByText("返回列表");

    expect(container.textContent).toContain("docs/plan.md");
    expect(container.textContent).not.toContain("评审轮次");
  });

  it("空态：无归档计划时给出可解释文案", async () => {
    listRuntimePlansMock.mockResolvedValue({ plans: [], count: 0 });

    await renderSurface();

    expect(container.textContent).toContain("还没有归档计划");
    expect(container.textContent).toContain("0 条归档计划");
  });

  it("错误态：展示错误并可重试恢复", async () => {
    listRuntimePlansMock.mockRejectedValueOnce(new Error("plan store unavailable"));

    await renderSurface();

    expect(container.textContent).toContain("plan store unavailable");
    expect(findButtonByText("重试")).toBeInstanceOf(HTMLButtonElement);

    listRuntimePlansMock.mockResolvedValue({
      plans: [plan("proj/plan", { plan_path: "docs/plan.md" })],
      count: 1,
    });
    clickButtonByText("重试");
    await settle();

    expect(container.textContent).not.toContain("plan store unavailable");
    expect(container.textContent).toContain("docs/plan.md");
  });

  it("plan_review_requested 到达时刷新列表", async () => {
    listRuntimePlansMock.mockResolvedValue({
      plans: [plan("proj/plan", { plan_path: "docs/plan.md" })],
      count: 1,
    });

    await renderSurface({ lastRuntimeEventType: "tool.completed", runtimeEventCount: 1 });
    expect(container.textContent).toContain("docs/plan.md");
    expect(listRuntimePlansMock).toHaveBeenCalledTimes(1);

    listRuntimePlansMock.mockResolvedValue({
      plans: [
        plan("proj/plan", { plan_path: "docs/plan.md" }),
        plan("proj/new", { plan_path: "docs/new.md", status: "pending", version: 1 }),
      ],
      count: 2,
    });

    await act(async () => {
      root?.render(
        <ArtifactPanelPlansSurface
          lastRuntimeEventType="plan_review_requested"
          runtimeEventCount={2}
          sessionId=""
        />,
      );
    });
    await settle();

    expect(listRuntimePlansMock).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain("docs/new.md");

    // 同一事件键再次投影（重渲染）不重复拉取。
    await act(async () => {
      root?.render(
        <ArtifactPanelPlansSurface
          lastRuntimeEventType="plan_review_requested"
          runtimeEventCount={2}
          sessionId=""
        />,
      );
    });
    await settle();

    expect(listRuntimePlansMock).toHaveBeenCalledTimes(2);
  });

  it("重新评审：无会话时按钮禁用，有会话时回灌并给出成功提示", async () => {
    listRuntimePlansMock.mockResolvedValue({
      plans: [plan("proj/plan", { plan_path: "docs/plan.md" })],
      count: 1,
    });
    getRuntimePlanMock.mockResolvedValue(
      plan("proj/plan", { plan_path: "docs/plan.md", content: "# 计划", content_available: true }),
    );
    reopenRuntimePlanMock.mockResolvedValue({
      plan_id: "proj/plan",
      version: 3,
      bytes: 12,
      created: true,
      unchanged: false,
      forced: false,
    });

    await renderSurface();
    clickButtonByText("docs/plan.md");
    await settle();

    const disabledButton = findButtonByText("重新评审");
    expect(disabledButton).toBeInstanceOf(HTMLButtonElement);
    expect((disabledButton as HTMLButtonElement).disabled).toBe(true);

    // 面板拿到会话上下文后（同一详情视图重渲染），同一个按钮才可点。
    await renderSurface({ sessionId: "session-1" });
    clickButtonByText("重新评审");
    await settle();

    expect(reopenRuntimePlanMock).toHaveBeenCalledWith("session-1", "proj/plan", {});
    expect(container.textContent).toContain("已从归档恢复 v3");
    expect(container.textContent).toContain("已进入 plan mode");
  });

  it("重新评审冲突：展示后端 hint，确认后带 force 重试", async () => {
    listRuntimePlansMock.mockResolvedValue({
      plans: [plan("proj/plan", { plan_path: "docs/plan.md" })],
      count: 1,
    });
    getRuntimePlanMock.mockResolvedValue(
      plan("proj/plan", { plan_path: "docs/plan.md", content: "# 计划", content_available: true }),
    );
    reopenRuntimePlanMock
      .mockRejectedValueOnce(
        new RuntimeApiError(409, {
          conflict: true,
          error: "planmode: plan file differs from the archived snapshot",
          hint: "确认覆盖后带 force=true 重试",
        } as never),
      )
      .mockResolvedValueOnce({
        plan_id: "proj/plan",
        version: 3,
        bytes: 12,
        created: false,
        unchanged: false,
        forced: true,
      });

    await renderSurface({ sessionId: "session-1" });
    clickButtonByText("docs/plan.md");
    await settle();
    clickButtonByText("重新评审");
    await settle();

    expect(container.textContent).toContain("工作区计划文件与归档快照不一致");
    expect(container.textContent).toContain("确认覆盖后带 force=true 重试");

    clickButtonByText("强制覆盖并重新评审");
    await settle();

    expect(reopenRuntimePlanMock).toHaveBeenLastCalledWith("session-1", "proj/plan", {
      force: true,
    });
    expect(container.textContent).toContain("已从归档恢复 v3");
    expect(container.textContent).not.toContain("确认覆盖后带 force=true 重试");
  });
});

// 轮次差异（§4.4 前端部分）：展开/收起、着色行与计数、identical、失败重试。
describe("ArtifactPanelPlansSurface 轮次差异", () => {
  beforeAll(async () => {
    await codeHighlightingReady;
  });

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    getRuntimePlanDiffMock.mockReset();
    getRuntimePlanMock.mockReset();
    listRuntimePlansMock.mockReset();
    reopenRuntimePlanMock.mockReset();

    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function clickByTestId(testId: string) {
    const element = container.querySelector(`[data-testid="${testId}"]`);
    expect(element).toBeInstanceOf(HTMLButtonElement);
    act(() => {
      element?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
  }

  async function openDetailWithRounds() {
    listRuntimePlansMock.mockResolvedValue({
      plans: [plan("proj/plan", { plan_path: "docs/plan.md" })],
      count: 1,
    });
    getRuntimePlanMock.mockResolvedValue(
      plan("proj/plan", {
        plan_path: "docs/plan.md",
        version: 2,
        content: "# 计划",
        content_available: true,
        rounds: [
          { version: 1, decision: "request_changes", notes: "补充回滚方案" },
          { version: 2, decision: "approve" },
        ],
      }),
    );

    await renderSurface({ sessionId: "session-1" });
    clickButtonByText("docs/plan.md");
    await settle();
  }

  it("展开某一轮的差异：按上一轮→该轮取数，渲染计数与着色行，再点收起", async () => {
    getRuntimePlanDiffMock.mockResolvedValue({
      plan_id: "proj/plan",
      from_version: 1,
      to_version: 2,
      identical: false,
      added: 2,
      removed: 1,
      old_lines: 2,
      new_lines: 3,
      coarse: false,
      truncated: false,
      text: "--- v1 request_changes (user)\n+++ v2 approve (user)\n@@ -1,2 +1,3 @@\n # 计划\n-1. 先发布\n+1. 先灰度\n+2. 再发布\n",
    });

    await openDetailWithRounds();
    clickByTestId("plan-round-diff-1-2");
    await settle();

    expect(getRuntimePlanDiffMock).toHaveBeenCalledWith("proj/plan", { from: 1, to: 2 });
    expect(container.querySelector('[data-testid="plan-diff-1-2"]')).not.toBeNull();
    expect(container.textContent).toContain("v1 → v2");
    expect(container.textContent).toContain("+2");
    expect(container.textContent).toContain("-1");
    expect(container.textContent).toContain("+2. 再发布");

    clickByTestId("plan-round-diff-1-2");
    expect(container.querySelector('[data-testid="plan-diff-1-2"]')).toBeNull();
  });

  it("identical：判等走 identical 字段而不是文本，提示无变更", async () => {
    getRuntimePlanDiffMock.mockResolvedValue({
      plan_id: "proj/plan",
      from_version: 1,
      to_version: 2,
      identical: true,
      added: 0,
      removed: 0,
      old_lines: 2,
      new_lines: 2,
      coarse: false,
      truncated: false,
      text: "--- v1 request_changes (user)\n+++ v2 approve (user)\n",
    });

    await openDetailWithRounds();
    clickByTestId("plan-round-diff-1-2");
    await settle();

    expect(container.textContent).toContain("无变更");
    expect(container.textContent).toContain("这一轮快照与上一轮完全一致");
  });

  it("失败：给出错误文案，重试按钮重新取数", async () => {
    getRuntimePlanDiffMock.mockRejectedValueOnce(new Error("boom"));
    getRuntimePlanDiffMock.mockResolvedValueOnce({
      plan_id: "proj/plan",
      from_version: 1,
      to_version: 2,
      identical: false,
      added: 1,
      removed: 0,
      old_lines: 1,
      new_lines: 2,
      coarse: false,
      truncated: false,
      text: "--- v1 a\n+++ v2 b\n@@ -1 +1,2 @@\n # 计划\n+2. 再发布\n",
    });

    await openDetailWithRounds();
    clickByTestId("plan-round-diff-1-2");
    await settle();

    expect(container.textContent).toContain("差异加载失败");

    clickButtonByText("重试");
    await settle();

    expect(getRuntimePlanDiffMock).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain("+2. 再发布");
  });
});
