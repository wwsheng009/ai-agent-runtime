// @vitest-environment jsdom

// 计划归档面的轮次差异与行级评论（报告 §4.4 前端部分）：展开/收起、着色行与计数、
// identical、失败重试，以及「点行号选区 → 提交（锚定该轮）→ 列表按重放投影展示 → 删除」。
//
// 从 artifact-panel-plans-surface.test.tsx 拆出（P0-2：单文件 ≤500 非空行）；harness 与
// 该文件同风格：mock 掉 runtime API（唯一网络边界），真实渲染面组件与 hook，断言只落在
// 用户可见文本上。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeAll, afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { codeHighlightingReady } from "@/components/ui/code-highlighting";
import type { RuntimeStoredPlan } from "@/types/runtime";

import { ArtifactPanelPlansSurface } from "./artifact-panel-plans-surface";

const {
  createRuntimePlanCommentMock,
  deleteRuntimePlanCommentMock,
  getRuntimePlanDiffMock,
  getRuntimePlanMock,
  listRuntimePlanCommentsMock,
  listRuntimePlansMock,
  reopenRuntimePlanMock,
} = vi.hoisted(() => ({
    createRuntimePlanCommentMock: vi.fn(),
    deleteRuntimePlanCommentMock: vi.fn(),
    getRuntimePlanDiffMock: vi.fn(),
    getRuntimePlanMock: vi.fn(),
    listRuntimePlanCommentsMock: vi.fn(),
    listRuntimePlansMock: vi.fn(),
    reopenRuntimePlanMock: vi.fn(),
  }));

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return {
    ...actual,
    createRuntimePlanComment: createRuntimePlanCommentMock,
    deleteRuntimePlanComment: deleteRuntimePlanCommentMock,
    getRuntimePlanDiff: getRuntimePlanDiffMock,
    getRuntimePlan: getRuntimePlanMock,
    listRuntimePlanComments: listRuntimePlanCommentsMock,
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

// 轮次差异（§4.4 前端部分）：展开/收起、着色行与计数、identical、失败重试。
describe("ArtifactPanelPlansSurface 轮次差异", () => {
  beforeAll(async () => {
    await codeHighlightingReady;
  });

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    createRuntimePlanCommentMock.mockReset();
    deleteRuntimePlanCommentMock.mockReset();
    getRuntimePlanDiffMock.mockReset();
    getRuntimePlanMock.mockReset();
    listRuntimePlanCommentsMock.mockReset();
    listRuntimePlanCommentsMock.mockResolvedValue({
      plan_id: "",
      revision: 0,
      latest_revision: 0,
      comments: [],
      count: 0,
    });
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

  function clickByTestIdWithShift(testId: string) {
    const element = container.querySelector(`[data-testid="${testId}"]`);
    expect(element).toBeInstanceOf(HTMLButtonElement);
    act(() => {
      element?.dispatchEvent(new MouseEvent("click", { bubbles: true, shiftKey: true }));
    });
  }

  function setTextareaValue(testId: string, value: string) {
    const element = container.querySelector(`[data-testid="${testId}"]`);
    expect(element).toBeInstanceOf(HTMLTextAreaElement);
    const setter = Object.getOwnPropertyDescriptor(
      HTMLTextAreaElement.prototype,
      "value",
    )?.set;
    act(() => {
      setter?.call(element, value);
      element?.dispatchEvent(new Event("input", { bubbles: true }));
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

  // 行级评论（§4.4 前端部分）：选择 → 提交（锚定该轮）→ 列表按重放状态展示 → 删除。
  it("行级评论：点行号选区提交，列表按重放投影展示并可删除", async () => {
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
    const created = {
      id: "c-1",
      revision: 2,
      start_line: 2,
      end_line: 3,
      excerpt: "1. 先灰度\n2. 再发布",
      body: "这两步要写清回滚",
      author: "user",
      created_at: "2026-09-25T10:00:00Z",
      status: "anchored",
      current_revision: 2,
      current_start_line: 2,
      current_end_line: 3,
    };
    listRuntimePlanCommentsMock
      .mockResolvedValueOnce({
        plan_id: "proj/plan",
        revision: 2,
        latest_revision: 2,
        comments: [],
        count: 0,
      })
      .mockResolvedValueOnce({
        plan_id: "proj/plan",
        revision: 2,
        latest_revision: 2,
        comments: [],
        count: 0,
      });
    createRuntimePlanCommentMock.mockResolvedValue({
      plan_id: "proj/plan",
      revision: 2,
      latest_revision: 2,
      comments: [created],
      count: 1,
    });
    deleteRuntimePlanCommentMock.mockResolvedValue(true);

    await openDetailWithRounds();
    clickByTestId("plan-round-diff-1-2");
    await settle();

    expect(container.textContent).toContain("暂无行级评论");
    expect(container.textContent).toContain("按 v2 重放锚点");

    // 点 L2，再 Shift+点 L3 扩成区间：行号取的是**新版正文**行号（含 hunk 头偏移）。
    clickByTestId("plan-diff-line-2");
    clickByTestIdWithShift("plan-diff-line-3");
    expect(container.textContent).toContain("已选 L2-3");

    setTextareaValue("plan-comment-draft", "这两步要写清回滚");
    clickButtonByText("留评论");
    await settle();

    expect(createRuntimePlanCommentMock).toHaveBeenCalledWith("proj/plan", {
      revision: 2,
      startLine: 2,
      endLine: 3,
      body: "这两步要写清回滚",
    });
    expect(container.textContent).toContain("这两步要写清回滚");
    expect(container.textContent).toContain("与当前正文一致");

    const deleteButton = container.querySelector('button[aria-label="删除评论"]');
    expect(deleteButton).toBeInstanceOf(HTMLButtonElement);
    act(() => {
      deleteButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await settle();

    expect(deleteRuntimePlanCommentMock).toHaveBeenCalledWith("proj/plan", "c-1");
    expect(container.textContent).not.toContain("这两步要写清回滚");
  });
});
