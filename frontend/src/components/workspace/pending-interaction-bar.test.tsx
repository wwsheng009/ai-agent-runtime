// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { PendingInteraction } from "@/lib/pending-interaction";

const approvalExplanation = vi.hoisted(() => ({ explain: vi.fn() }));

vi.mock("@/hooks/workspace/use-approval-explanation", () => ({
  useApprovalExplanation: () => approvalExplanation.explain,
}));

vi.mock("react-i18next", () => ({
  // 插值原样拼接，便于断言「将记住的模式」确实渲染到了 DOM。
  useTranslation: () => ({
    t: (
      key: string,
      options?: { pattern?: string; model?: string; message?: string },
    ) =>
      options?.pattern
        ? `${key}:${options.pattern}`
        : options?.model
          ? `${key}:${options.model}`
          : options?.message
            ? `${key}:${options.message}`
            : key,
  }),
}));

import { PendingInteractionBar } from "./pending-interaction-bar";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function approvalInteraction(
  overrides: Partial<PendingInteraction> = {},
): PendingInteraction {
  return {
    kind: "approval",
    id: "req-1",
    status: "pending",
    sessionId: "session-1",
    toolName: "shell_exec",
    reason: "run tests",
    riskLevel: "medium",
    ...overrides,
  } as PendingInteraction;
}

function questionInteraction(): PendingInteraction {
  return {
    kind: "question",
    id: "q-1",
    status: "pending",
    sessionId: "session-1",
    prompt: "which branch?",
    required: true,
    suggestions: ["main", "dev"],
  };
}

function planReviewInteraction(): PendingInteraction {
  return {
    kind: "plan_review",
    id: "plan-review:session-1",
    status: "pending",
    sessionId: "session-1",
    planPath: "docs/plan/example.md",
  };
}

function setNativeValue(
  element: HTMLInputElement | HTMLTextAreaElement,
  value: string,
) {
  const prototype =
    element instanceof HTMLTextAreaElement
      ? HTMLTextAreaElement.prototype
      : HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(prototype, "value")?.set;
  setter?.call(element, value);
  element.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("PendingInteractionBar", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    approvalExplanation.explain.mockReset();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
  });

  afterEach(() => {
    if (root) {
      act(() => {
        root?.unmount();
      });
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderBar(props: {
    interaction: PendingInteraction | null;
    onResolveApproval?: (
      requestId: string,
      allow: boolean,
      options?: { rememberScope?: string; feedback?: string },
    ) => void;
    onAnswerQuestion?: (questionId: string, answer: string) => void;
    onPlanDecision?: (decision: string) => void;
    onPlanNotesChange?: (value: string) => void;
    planNotesDraft?: string;
    planActionPending?: boolean;
  }) {
    root = createRoot(container);
    act(() => {
      root?.render(
        <PendingInteractionBar
          interaction={props.interaction}
          onAnswerQuestion={props.onAnswerQuestion ?? (() => {})}
          onResolveApproval={props.onResolveApproval ?? (() => {})}
          onPlanDecision={props.onPlanDecision as never}
          onPlanNotesChange={props.onPlanNotesChange}
          planActionPending={props.planActionPending}
          planNotesDraft={props.planNotesDraft}
        />,
      );
    });
    return container;
  }

  /** 卡片内的全部按钮（提问 / 计划评审用；审批断言请用 buttons()）。 */
  function allButtons() {
    // 显式收窄为 HTMLButtonElement[]：querySelectorAll 的 Element 类型没有
    // click()/disabled，测试里要按真实按钮用（tsc -b 门禁要求）。
    return Array.from(container.querySelectorAll<HTMLButtonElement>("button"));
  }

  function buttons() {
    // 只取批准/拒绝：解释按钮是只读动作，不参与决定断言。
    return Array.from(
      container.querySelectorAll<HTMLButtonElement>(
        "button[data-approval-approve], button[data-approval-deny]",
      ),
    );
  }

  it("renders nothing without an interaction", () => {
    renderBar({ interaction: null });
    expect(container.querySelector('[data-testid="pending-interaction"]')).toBeNull();
  });

  it("resolves approvals through the shared slot", () => {
    const onResolveApproval = vi.fn();
    renderBar({
      interaction: approvalInteraction(),
      onResolveApproval,
    });

    const bar = container.querySelector('[data-testid="pending-interaction"]');
    expect(bar?.getAttribute("data-kind")).toBe("approval");
    expect(container.textContent).toContain("shell_exec");

    act(() => {
      buttons()[0].click();
    });
    expect(onResolveApproval).toHaveBeenCalledWith("req-1", true);

    act(() => {
      buttons()[1].click();
    });
    expect(onResolveApproval).toHaveBeenCalledWith("req-1", false);
  });

  it("disables approval actions while the decision is in flight", () => {
    renderBar({
      interaction: approvalInteraction({ status: "resolving" }),
    });
    expect(buttons()).toHaveLength(2);
    expect(buttons().every((button) => button.disabled)).toBe(true);
  });

  it("§4.8：可记忆的审批可勾选「记住」并附说明，不可记忆时不渲染入口", () => {
    const onResolveApproval = vi.fn();
    renderBar({
      interaction: approvalInteraction({
        rememberPattern: "path:docs/a.md",
      } as Partial<PendingInteraction>),
      onResolveApproval,
    });

    // 后端下发 rememberPattern 才出现「记住」；提示文案回显将记住的模式。
    const remember = container.querySelector(
      'input[type="checkbox"]',
    ) as HTMLInputElement;
    expect(remember).not.toBeNull();
    expect(container.textContent).toContain("path:docs/a.md");

    act(() => {
      remember.click();
    });

    const feedback = Array.from(
      container.querySelectorAll("input"),
    ).find((input) => input.type !== "checkbox") as HTMLInputElement;
    act(() => {
      setNativeValue(feedback, "只改这一个文件");
    });
    act(() => {
      buttons()[0].click();
    });
    expect(onResolveApproval).toHaveBeenCalledWith("req-1", true, {
      rememberScope: "session",
      feedback: "只改这一个文件",
    });
  });

  it("§4.8：后端未给 rememberPattern（危险工具等）时不渲染「记住」", () => {
    renderBar({ interaction: approvalInteraction() });
    expect(container.querySelector('input[type="checkbox"]')).toBeNull();
    // 说明输入仍然可用，但空说明不产生第三个参数（请求体保持旧形状）。
    const onResolveApproval = vi.fn();
    renderBar({ interaction: approvalInteraction(), onResolveApproval });
    act(() => {
      buttons()[0].click();
    });
    expect(onResolveApproval).toHaveBeenCalledWith("req-1", true);
  });

  it("§4.13：可按需生成解释，只读展示来源，不影响批准/拒绝提交", async () => {
    approvalExplanation.explain.mockResolvedValue({
      explanation: "会执行 rm -rf build/，删除构建产物目录",
      source: "model",
      model: "gpt-5.1",
    });
    const onResolveApproval = vi.fn();
    renderBar({
      interaction: approvalInteraction(),
      onResolveApproval,
    });

    const explain = container.querySelector<HTMLButtonElement>(
      "[data-approval-explain]",
    );
    expect(explain).not.toBeNull();
    await act(async () => {
      explain?.click();
    });

    // 只读：按条目会话与请求号取解释，不产生任何决定。
    expect(approvalExplanation.explain).toHaveBeenCalledWith(
      "session-1",
      "req-1",
    );
    expect(onResolveApproval).not.toHaveBeenCalled();
    const block = container.querySelector("[data-approval-explanation]");
    expect(block?.textContent).toContain("删除构建产物目录");
    expect(block?.textContent).toContain("gpt-5.1");
  });

  it("§4.13：解释失败就地提示，按钮可再试", async () => {
    approvalExplanation.explain.mockRejectedValue(new Error("model offline"));
    renderBar({ interaction: approvalInteraction() });

    const explain = container.querySelector<HTMLButtonElement>(
      "[data-approval-explain]",
    );
    await act(async () => {
      explain?.click();
    });

    const error = container.querySelector("[data-approval-explain-error]");
    expect(error?.textContent).toContain("model offline");
    expect(container.querySelector("[data-approval-explanation]")).toBeNull();
  });

  it("answers questions via suggestion or typed draft", () => {
    const onAnswerQuestion = vi.fn();
    renderBar({
      interaction: questionInteraction(),
      onAnswerQuestion,
    });

    // 建议按钮（main / dev）在前，提交按钮在后。
    const suggestionButton = allButtons()[0];
    expect(suggestionButton.textContent).toContain("main");
    act(() => {
      suggestionButton.click();
    });
    expect(onAnswerQuestion).toHaveBeenCalledWith("q-1", "main");

    const input = container.querySelector("input");
    expect(input).not.toBeNull();
    act(() => {
      setNativeValue(input as HTMLInputElement, "release/next");
    });
    act(() => {
      container
        .querySelector("form")
        ?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(onAnswerQuestion).toHaveBeenCalledWith("q-1", "release/next");
  });

  it("routes plan review decisions and notes through the same slot", () => {
    const onPlanDecision = vi.fn();
    const onPlanNotesChange = vi.fn();
    renderBar({
      interaction: planReviewInteraction(),
      onPlanDecision,
      onPlanNotesChange,
      planNotesDraft: "initial",
    });

    const textarea = container.querySelector("textarea");
    expect(textarea?.value).toBe("initial");
    act(() => {
      setNativeValue(textarea as HTMLTextAreaElement, "please split");
    });
    expect(onPlanNotesChange).toHaveBeenCalledWith("please split");

    act(() => {
      allButtons()[0].click();
    });
    act(() => {
      allButtons()[1].click();
    });
    act(() => {
      allButtons()[2].click();
    });
    expect(onPlanDecision.mock.calls.map((call) => call[0])).toEqual([
      "approve",
      "request_changes",
      "quit",
    ]);
  });
});
