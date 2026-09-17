// @vitest-environment jsdom

// 「网络详情 → 会话订阅」区块单测（Batch 4 §4.6）。
//
// 只断言两件事：
//   1. 默认形态（注册表开关关闭 → 空条目）区块不渲染，旧面板不多出一块装饰；
//   2. 有条目时计数口径正确：live / poll / 总数 / 本会话强度 / 页面可见性。
// 注册表本身的行为由 lib/session-runtime/registry.test.ts 覆盖，这里只测接线。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { SessionRuntimeEntrySnapshot } from "@/lib/session-runtime/types";

const registryState = vi.hoisted(() => ({
  entries: [] as SessionRuntimeEntrySnapshot[],
}));

vi.mock("@/hooks/workspace/use-session-runtime-registry", () => ({
  useSessionRuntimeEntries: () => registryState.entries,
}));

import { resetLiveDiagnostics } from "@/lib/live-diagnostics/store";

import { SessionDetailNetworkSection } from "./session-detail-network";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

let container: HTMLDivElement | null = null;
let root: Root | null = null;

function entry(
  sessionId: string,
  mode: SessionRuntimeEntrySnapshot["mode"],
): SessionRuntimeEntrySnapshot {
  return {
    sessionId,
    mode,
    status: mode === "idle" ? "idle" : "online",
    lastSeq: 0,
    activeTurn: null,
    detached: false,
    pending: { approvals: 0, questions: 0, planPending: false },
    runningAgents: 0,
    lastEventAt: null,
    lastError: null,
  };
}

function statValue(gridTestId: string, label: string): string {
  const grid = document.body.querySelector(`[data-testid="${gridTestId}"]`);
  if (!grid) {
    return "";
  }
  for (const cell of Array.from(grid.querySelectorAll("div"))) {
    if (cell.querySelector("dt")?.textContent?.trim() === label) {
      return cell.querySelector("dd")?.textContent?.trim() ?? "";
    }
  }
  return "";
}

async function mount(sessionId = "session-1") {
  await act(async () => {
    root?.render(<SessionDetailNetworkSection sessionId={sessionId} />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

describe("SessionDetailNetworkSection · 会话订阅", () => {
  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    resetLiveDiagnostics();
    registryState.entries = [];
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container?.remove();
    container = null;
    resetLiveDiagnostics();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("注册表无条目（开关关闭）时不渲染订阅区块", async () => {
    await mount();

    expect(
      document.body.querySelector(
        '[data-testid="session-detail-network-subscriptions"]',
      ),
    ).toBeNull();
  });

  it("有条目时给出 live / poll / 总数与本会话强度", async () => {
    registryState.entries = [
      entry("session-1", "live"),
      entry("session-2", "poll"),
      entry("session-3", "poll"),
    ];

    await mount("session-1");

    const grid = "session-detail-network-subscriptions-stats";
    expect(statValue(grid, "实时")).toBe("1");
    expect(statValue(grid, "轮询")).toBe("2");
    expect(statValue(grid, "订阅数")).toBe("3");
    expect(statValue(grid, "本会话")).toBe("实时");
    expect(statValue(grid, "页面可见性")).toBe("前台");
  });

  it("本会话不在订阅集合里时显示「未订阅」", async () => {
    registryState.entries = [entry("session-2", "live")];

    await mount("session-9");

    expect(
      statValue("session-detail-network-subscriptions-stats", "本会话"),
    ).toBe("未订阅");
  });

  it("页面隐藏时可见性口径切换为「后台降采样」", async () => {
    registryState.entries = [entry("session-1", "live")];
    const descriptor = Object.getOwnPropertyDescriptor(document, "hidden");
    Object.defineProperty(document, "hidden", {
      configurable: true,
      get: () => true,
    });

    try {
      await mount("session-1");

      expect(
        statValue("session-detail-network-subscriptions-stats", "页面可见性"),
      ).toBe("后台降采样");
    } finally {
      if (descriptor) {
        Object.defineProperty(document, "hidden", descriptor);
      } else {
        delete (document as { hidden?: boolean }).hidden;
      }
    }
  });
});
