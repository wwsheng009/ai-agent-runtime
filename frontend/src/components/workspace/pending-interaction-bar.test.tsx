// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { PendingInteraction } from "@/lib/pending-interaction";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
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
    onResolveApproval?: (requestId: string, allow: boolean) => void;
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

  function buttons() {
    return Array.from(container.querySelectorAll("button"));
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

  it("answers questions via suggestion or typed draft", () => {
    const onAnswerQuestion = vi.fn();
    renderBar({
      interaction: questionInteraction(),
      onAnswerQuestion,
    });

    // 建议按钮（main / dev）在前，提交按钮在后。
    const suggestionButton = buttons()[0];
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
      buttons()[0].click();
    });
    act(() => {
      buttons()[1].click();
    });
    act(() => {
      buttons()[2].click();
    });
    expect(onPlanDecision.mock.calls.map((call) => call[0])).toEqual([
      "approve",
      "request_changes",
      "quit",
    ]);
  });
});
