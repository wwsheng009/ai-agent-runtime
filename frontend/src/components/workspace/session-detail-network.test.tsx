// @vitest-environment jsdom

// 「网络详情」面级单测：只断言用户可见的归因口径——
//   * 有事件且闸门开启 → 连接正常（渲染链路无嫌疑）；
//   * 有事件但闸门关闭 → 到达未渲染（问题在前端，不在网络）；
//   * 通道建立但零字节 → 无事件（问题在 SSE live / 服务端 / 代理）；
//   * 两条通道（运行时流 / 直连回合）计数互不串台；
//   * 消息列缺失时 DOM 口径如实说明，不假装观测到了。
//   * 流量波动图：按秒成柱、窗口外的流量不画（宁可少画也不挪时间点）。
// 场景装配走 store 的真实写入 API（与传输层/接线层同一入口），不 mock 观测数据。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  beginLiveChannel,
  reportBlockedDelta,
  reportRenderGate,
  resetLiveDiagnostics,
} from "@/lib/live-diagnostics/store";

import { SessionDetailNetworkSection } from "./session-detail-network";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

let container: HTMLDivElement | null = null;
let root: Root | null = null;

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

async function mount(sessionId = "session-1") {
  await act(async () => {
    root?.render(<SessionDetailNetworkSection sessionId={sessionId} />);
  });
  await act(async () => {
    await flush();
  });
}

function nodeText(testId: string): string {
  return (
    document.body.querySelector(`[data-testid="${testId}"]`)?.textContent ?? ""
  );
}

/** 读取某个统计网格里指定标签的值（dt/dd 结构，按标签定位避免顺序耦合）。 */
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

describe("SessionDetailNetworkSection", () => {
  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    resetLiveDiagnostics();
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

  it("有事件且闸门开启：判为连接正常，并给出字节/事件/保活计数", async () => {
    const sink = beginLiveChannel({ channel: "runtime", sessionId: "session-1" });
    sink.open();
    sink.bytes(1200);
    sink.event("assistant_delta", { seq: 7, type: "assistant_delta" });
    sink.keepalive();
    reportRenderGate("session-1", {
      liveTurnId: "turn-1",
      resumedTurnId: null,
      localResponding: true,
      rendering: true,
    });

    await mount();

    expect(nodeText("session-detail-network-verdict")).toBe("连接正常");
    expect(statValue("session-detail-network-runtime-stats", "字节")).toBe("1.2 KB");
    expect(statValue("session-detail-network-runtime-stats", "事件")).toBe("1");
    expect(statValue("session-detail-network-runtime-stats", "保活")).toBe("1");
    expect(statValue("session-detail-network-runtime-stats", "最近事件")).toBe("0.0s");
    expect(statValue("session-detail-network-gate", "在途回合")).toBe("turn-1");
    expect(statValue("session-detail-network-gate", "被拦增量")).toBe("0");
    expect(nodeText("session-detail-network-frames")).toContain("assistant_delta #7");
  });

  it("有事件但闸门关闭：判为到达未渲染，并累计被拦增量", async () => {
    const sink = beginLiveChannel({ channel: "runtime", sessionId: "session-1" });
    sink.open();
    sink.event("assistant_delta", { seq: 9, type: "assistant_delta" });
    reportRenderGate("session-1", {
      liveTurnId: null,
      resumedTurnId: null,
      localResponding: false,
      rendering: false,
    });
    reportBlockedDelta("session-1");
    reportBlockedDelta("session-1");

    await mount();

    expect(nodeText("session-detail-network-verdict")).toBe("到达未渲染");
    expect(statValue("session-detail-network-gate", "被拦增量")).toBe("2");
    expect(statValue("session-detail-network-gate", "渲染闸门")).toBe("关");
    expect(nodeText("session-detail-network")).toContain("问题在前端渲染侧");
  });

  it("通道建立但零字节：判为无事件，并如实给出空帧列表", async () => {
    const sink = beginLiveChannel({ channel: "runtime", sessionId: "session-1" });
    sink.open();

    await mount();

    expect(nodeText("session-detail-network-verdict")).toBe("无事件");
    expect(nodeText("session-detail-network-frames")).toContain("尚未收到任何帧");
    expect(statValue("session-detail-network-runtime-stats", "最近字节")).toBe("尚无");
  });

  it("两条通道计数互不串台", async () => {
    const chat = beginLiveChannel({ channel: "chat", sessionId: "session-1" });
    chat.open();
    chat.event("message", { seq: 2, type: "assistant_delta" });

    await mount();

    expect(nodeText("session-detail-network-chat")).toContain("直连回合");
    expect(statValue("session-detail-network-chat-stats", "事件")).toBe("1");
    expect(statValue("session-detail-network-runtime-stats", "事件")).toBe("0");
  });

  it("消息列缺失时如实说明 DOM 未挂载", async () => {
    await mount();

    expect(nodeText("session-detail-network-dom-status")).toBe("未找到消息列");
    expect(statValue("session-detail-network-dom", "变更")).toBe("0");
  });

  it("消息列存在时说明已挂载，并统计 DOM 变更", async () => {
    // DOM 变更计数随 store 快照下发（写入走 250ms 合并窗口，观测本身不驱动
    // 渲染），因此这一段用假时钟把「合并窗口 + 面板 1s 节拍」推进一格。
    vi.useFakeTimers();
    try {
      const log = document.createElement("div");
      log.setAttribute("role", "log");
      document.body.append(log);

      await mount();

      expect(nodeText("session-detail-network-dom-status")).toBe("已挂载消息列");

      await act(async () => {
        log.append(document.createElement("p"));
        await flush();
        vi.advanceTimersByTime(1_000);
        await flush();
      });

      expect(statValue("session-detail-network-dom", "变更")).toBe("1");
      log.remove();
    } finally {
      vi.useRealTimers();
    }
  });

  it("流量波动图：按秒成柱，峰值/合计按 60s 窗口口径给出", async () => {
    vi.useFakeTimers();
    try {
      const start = Date.parse("2026-09-16T12:00:00.000Z");
      vi.setSystemTime(new Date(start));
      const sink = beginLiveChannel({ channel: "runtime", sessionId: "session-1" });
      sink.open();
      sink.bytes(400);
      vi.setSystemTime(new Date(start + 1_000));
      sink.bytes(1_000);
      // 面板 tick 前进了 50s：前两桶仍在窗口内，但都成了「左半边」的柱子。
      vi.setSystemTime(new Date(start + 50_000));
      sink.bytes(200);

      await mount();

      const chart = document.body.querySelector(
        '[data-testid="session-detail-network-runtime-traffic"]',
      );
      expect(chart?.getAttribute("data-traffic-total")).toBe("1600");
      expect(chart?.getAttribute("data-traffic-max")).toBe("1000");
      const bars = Array.from(chart?.querySelectorAll("[data-bytes]") ?? []).map(
        (bar) => Number(bar.getAttribute("data-bytes")),
      );
      expect(bars).toHaveLength(60);
      expect(bars.filter((bytes) => bytes > 0)).toEqual([400, 1_000, 200]);
      expect(nodeText("session-detail-network-runtime-traffic")).toContain(
        "峰值 1000 B/s",
      );
      expect(nodeText("session-detail-network-runtime-traffic")).toContain(
        "合计 1.6 KB",
      );
      // 两条通道各自成图：直连回合没有流量就不画柱子。
      expect(nodeText("session-detail-network-chat-traffic")).toContain(
        "近 60s 没有流量",
      );
    } finally {
      vi.useRealTimers();
    }
  });

  it("流量波动图：窗口内零流量时只说明，不留一排空柱", async () => {
    const sink = beginLiveChannel({ channel: "runtime", sessionId: "session-1" });
    sink.open();
    sink.event("assistant_delta", { seq: 3, type: "assistant_delta" });

    await mount();

    expect(nodeText("session-detail-network-runtime-traffic")).toContain(
      "近 60s 没有流量",
    );
    expect(
      document.body.querySelector(
        '[data-testid="session-detail-network-runtime-traffic-bars"]',
      ),
    ).toBeNull();
  });
});
