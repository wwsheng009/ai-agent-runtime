// @vitest-environment jsdom

import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { type ConnectionStatusLabels } from "@/lib/connection-status";

import { ConnectionStatusBadge } from "./connection-status-badge";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const labels: ConnectionStatusLabels = {
  connecting: "连接中…",
  idle: "空闲",
  offline: "连接中断",
  online: "在线",
  reconnecting: "重连中…",
};

describe("ConnectionStatusBadge", () => {
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

  function renderBadge(
    overrides: Partial<ComponentProps<typeof ConnectionStatusBadge>> = {},
  ) {
    const props: ComponentProps<typeof ConnectionStatusBadge> = {
      labels,
      status: "offline",
      ...overrides,
    };

    act(() => {
      root?.render(<ConnectionStatusBadge {...props} />);
    });
    return props;
  }

  it("announces the unified status with a stable data attribute", () => {
    renderBadge({ status: "reconnecting" });

    const badge = container.querySelector('[data-connection-status="reconnecting"]');
    expect(badge).toBeInstanceOf(HTMLElement);
    expect(badge?.getAttribute("role")).toBe("status");
    expect(container.textContent).toContain("重连中…");
  });

  it("offers manual retry for non-online statuses only", () => {
    const idle = renderBadge({ status: "online", onRetry: vi.fn() });
    expect(idle.onRetry).not.toHaveBeenCalled();
    expect(container.querySelector("button")).toBeNull();

    const onRetry = vi.fn();
    renderBadge({ status: "offline", onRetry, retryLabel: "重试" });

    const retryButton = container.querySelector("button");
    expect(retryButton?.textContent).toBe("重试");
    act(() => {
      retryButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("hides the retry entry when no handler is wired", () => {
    renderBadge({ status: "connecting" });

    expect(container.querySelector("button")).toBeNull();
    expect(container.textContent).toContain("连接中…");
  });

  it("keeps the same status vocabulary in the compact header variant", () => {
    // P1-8：日志页头与会话流共用同一呈现件——只有视觉变体不同，
    // 状态属性/角色/文案来源保持完全一致。
    renderBadge({ status: "online", variant: "header" });

    const badge = container.querySelector('[data-connection-status="online"]');
    expect(badge).toBeInstanceOf(HTMLElement);
    expect(badge?.getAttribute("role")).toBe("status");
    expect(badge?.className).toContain("uppercase");
    expect(badge?.className).toContain("rounded-control");
    expect(container.textContent).toContain("在线");
  });
});
