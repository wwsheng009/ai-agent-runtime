// 「网络详情」纯派生单测：诊断结论优先级、时间/体积格式化、帧间隔抖动。

import { describe, expect, it } from "vitest";

import {
  formatLiveAge,
  formatLiveBytes,
  formatLiveClock,
  formatLiveFrameLabel,
  resolveFrameGapStats,
  resolveLiveNetworkVerdict,
} from "./derive";
import { type LiveChannelDiagnostics, type LiveFrameSample } from "./types";

const NOW = Date.parse("2026-09-16T12:00:00.000Z");

function channel(
  overrides: Partial<LiveChannelDiagnostics> = {},
): LiveChannelDiagnostics {
  return {
    active: true,
    opens: 1,
    closes: 0,
    events: 10,
    keepalives: 2,
    errors: 0,
    bytes: 4096,
    stalls: 0,
    lastStallMs: null,
    lastOpenAt: NOW - 60_000,
    lastCloseAt: null,
    lastFrameAt: NOW - 500,
    lastEventAt: NOW - 500,
    lastKeepaliveAt: NOW - 15_000,
    lastErrorAt: null,
    lastError: null,
    lastEventName: "delta",
    cursor: 120,
    frames: [],
    ...overrides,
  };
}

function verdict(
  runtime: Partial<LiveChannelDiagnostics>,
  gate: { blockedDeltas?: number; lastBlockedAt?: number | null } = {},
) {
  return resolveLiveNetworkVerdict({
    runtime: channel(runtime),
    blockedDeltas: gate.blockedDeltas ?? 0,
    lastBlockedAt: gate.lastBlockedAt ?? null,
    now: NOW,
  }).code;
}

describe("resolveLiveNetworkVerdict", () => {
  it("最近有帧且无闸门拦截 → healthy", () => {
    expect(verdict({})).toBe("healthy");
  });

  it("闸门最近拦下过增量 → render-blocked（事件在到达、前端没渲染）", () => {
    expect(verdict({}, { blockedDeltas: 37, lastBlockedAt: NOW - 5_000 })).toBe(
      "render-blocked",
    );
  });

  it("连接层报错优先于闸门结论（先修连接，再谈渲染）", () => {
    expect(
      verdict(
        { errors: 2, lastErrorAt: NOW - 1_000, lastError: "upstream reset" },
        { blockedDeltas: 3, lastBlockedAt: NOW - 1_000 },
      ),
    ).toBe("channel-error");
  });

  it("错误早已过期时不再作为当前结论", () => {
    expect(verdict({ errors: 1, lastErrorAt: NOW - 120_000 })).toBe("healthy");
  });

  it("连接建立过但一帧未收 → no-events（SSE 没有事件）", () => {
    expect(verdict({ opens: 2, lastFrameAt: null, lastEventAt: null })).toBe(
      "no-events",
    );
  });

  it("超过断流阈值无任何字节 → no-events（含只剩 keepalive 的场景）", () => {
    expect(
      verdict({
        lastFrameAt: NOW - 25_000,
        lastEventAt: NOW - 90_000,
        keepalives: 6,
      }),
    ).toBe("no-events");
  });

  it("从未建连 → idle（不臆造故障）", () => {
    expect(
      verdict({ opens: 0, active: false, lastFrameAt: null, lastEventAt: null }),
    ).toBe("idle");
  });

  it("久无帧但未超阈值 → idle（会话空闲）", () => {
    expect(verdict({ lastFrameAt: NOW - 12_000 })).toBe("idle");
  });

  it("过期的闸门拦截不再左右结论", () => {
    expect(
      verdict({}, { blockedDeltas: 5, lastBlockedAt: NOW - 120_000 }),
    ).toBe("healthy");
  });
});

describe("格式化", () => {
  it("formatLiveAge 覆盖亚秒 / 秒 / 分级三档，缺值是 null", () => {
    expect(formatLiveAge(NOW, NOW - 400)).toBe("0.4s");
    expect(formatLiveAge(NOW, NOW - 12_400)).toBe("12s");
    expect(formatLiveAge(NOW, NOW - 185_000)).toBe("3m05s");
    expect(formatLiveAge(NOW, null)).toBeNull();
    // 时钟漂移（到达时刻晚于 now）不产生负数。
    expect(formatLiveAge(NOW, NOW + 3_000)).toBe("0.0s");
  });

  it("formatLiveBytes 覆盖 B / KB / MB", () => {
    expect(formatLiveBytes(0)).toBe("0 B");
    expect(formatLiveBytes(842)).toBe("842 B");
    expect(formatLiveBytes(1024 * 128.4)).toBe("128 KB");
    expect(formatLiveBytes(1024 * 1024 * 2.31)).toBe("2.31 MB");
  });

  it("formatLiveClock 输出本地 HH:MM:SS.mmm", () => {
    const at = new Date(2026, 8, 16, 9, 5, 7, 42).getTime();
    expect(formatLiveClock(at)).toBe("09:05:07.042");
  });

  it("resolveFrameGapStats 给出中位数与最大值（新旧顺序无关）", () => {
    const frames: LiveFrameSample[] = [
      { at: 1_500, bytes: 0, kind: "event", name: "delta", seq: 3 },
      { at: 1_000, bytes: 0, kind: "event", name: "delta", seq: 2 },
      { at: 0, bytes: 0, kind: "event", name: "delta", seq: 1 },
    ];
    expect(resolveFrameGapStats(frames)).toEqual({ p50: 750, max: 1_000 });
    expect(resolveFrameGapStats([])).toEqual({ p50: null, max: null });
    expect(resolveFrameGapStats([frames[0]])).toEqual({ p50: null, max: null });
  });

  it("formatLiveFrameLabel 带 seq 时附上游标", () => {
    expect(
      formatLiveFrameLabel({ at: 0, bytes: 0, kind: "event", name: "delta", seq: 7 }),
    ).toBe("delta #7");
    expect(
      formatLiveFrameLabel({ at: 0, bytes: 0, kind: "keepalive", name: ": keepalive", seq: null }),
    ).toBe(": keepalive");
  });
});
