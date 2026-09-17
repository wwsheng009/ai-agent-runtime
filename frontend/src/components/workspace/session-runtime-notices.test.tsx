// Batch 3（多会话并发运行时 §4.7.3）：后台会话「完成 / 待交互」页内通知的呈现契约。
//
// 只验证呈现与回调：有通知才挂载；每条带 kind 标记；「前往会话」「忽略」分别把
// sessionId / 通知 id 交回接线层；标题解析不到时回落「会话 {{sessionId}}」而不是空白。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { type SessionRuntimeNotice } from "@/lib/session-runtime/notices";

import { SessionRuntimeNotices } from "./session-runtime-notices";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function notice(
  overrides: Partial<SessionRuntimeNotice> & Pick<SessionRuntimeNotice, "kind">,
): SessionRuntimeNotice {
  return {
    id: `session-alpha#${overrides.kind}`,
    sessionId: "session-alpha",
    createdAt: 1,
    ...overrides,
  };
}

describe("SessionRuntimeNotices", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function render(
    props: Partial<React.ComponentProps<typeof SessionRuntimeNotices>> & {
      notices: SessionRuntimeNotice[];
    },
  ) {
    const merged: React.ComponentProps<typeof SessionRuntimeNotices> = {
      onDismiss: vi.fn(),
      onOpenSession: vi.fn(),
      ...props,
    };
    act(() => {
      root?.render(<SessionRuntimeNotices {...merged} />);
    });
    return merged;
  }

  it("没有通知时不挂载（不占位、不引入空节点）", () => {
    render({ notices: [] });
    expect(
      container.querySelector('[data-testid="session-runtime-notices"]'),
    ).toBeNull();
  });

  it("按 kind 渲染每条通知，并带上会话标题", () => {
    render({
      notices: [
        notice({ kind: "turn_finished" }),
        notice({
          id: "session-beta#approval",
          sessionId: "session-beta",
          kind: "approval",
        }),
      ],
      resolveSessionTitle: (sessionId) =>
        sessionId === "session-alpha" ? "Alpha 会话" : undefined,
    });

    const cards = Array.from(
      container.querySelectorAll<HTMLElement>("[data-notice-kind]"),
    );
    expect(cards.map((card) => card.getAttribute("data-notice-kind"))).toEqual([
      "turn_finished",
      "approval",
    ]);
    expect(cards[0]?.textContent).toContain("Alpha 会话");
    // 标题解析不到 → 回落文案仍带会话 id，不伪造标题。
    expect(cards[1]?.textContent).toContain("session-beta");
  });

  it("「前往会话」回传 sessionId，「忽略」回传通知 id", () => {
    const onOpenSession = vi.fn();
    const onDismiss = vi.fn();
    render({
      notices: [notice({ kind: "question" })],
      onDismiss,
      onOpenSession,
    });

    const card = container.querySelector<HTMLElement>(
      '[data-notice-kind="question"]',
    );
    expect(card).not.toBeNull();

    // 卡片内固定两枚按钮：先「前往会话」，后「忽略」。
    const buttons = Array.from(
      card?.querySelectorAll<HTMLButtonElement>("button") ?? [],
    );
    const [openButton, dismissButton] = buttons;
    expect(buttons).toHaveLength(2);

    act(() => {
      openButton?.click();
    });
    expect(onOpenSession).toHaveBeenCalledWith("session-alpha");

    act(() => {
      dismissButton?.click();
    });
    expect(onDismiss).toHaveBeenCalledWith("session-alpha#question");
  });
});
