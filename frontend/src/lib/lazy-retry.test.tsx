// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  ChunkLoadError,
  isChunkLoadError,
  isChunkLoadFailure,
  loadWithRetry,
} from "./lazy-retry";

describe("loadWithRetry（lazy chunk 有上限 + 递增退避重试）", () => {
  beforeEach(() => {
    // 失败路径会经 logger 出口写 console.error，测试中静音。
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("首次加载成功时不触发退避", async () => {
    const loader = vi.fn(async () => "ok");
    const delays: number[] = [];
    const sleep = vi.fn(async (delayMs: number) => {
      delays.push(delayMs);
    });

    await expect(loadWithRetry(loader, { sleep })).resolves.toBe("ok");

    expect(loader).toHaveBeenCalledTimes(1);
    expect(delays).toEqual([]);
  });

  it("失败后按退避重试一次即成功（假 loader 只调用两次）", async () => {
    let calls = 0;
    const loader = vi.fn(async () => {
      calls += 1;
      if (calls === 1) {
        throw new Error("network glitch");
      }
      return "ok";
    });
    const delays: number[] = [];
    const sleep = vi.fn(async (delayMs: number) => {
      delays.push(delayMs);
    });

    await expect(loadWithRetry(loader, { sleep })).resolves.toBe("ok");

    expect(loader).toHaveBeenCalledTimes(2);
    expect(delays).toEqual([400]);
  });

  it("达到次数上限后停止重试并抛出 ChunkLoadError（不退化为死循环）", async () => {
    const loader = vi.fn(async () => {
      throw new Error("404 chunk missing");
    });
    const delays: number[] = [];
    const sleep = vi.fn(async (delayMs: number) => {
      delays.push(delayMs);
    });

    let caught: unknown;
    try {
      await loadWithRetry(loader, { sleep });
    } catch (error) {
      caught = error;
    }

    expect(caught).toBeInstanceOf(ChunkLoadError);
    expect(isChunkLoadError(caught)).toBe(true);
    expect(isChunkLoadError(caught) ? caught.attempts : 0).toBe(3);
    // 默认上限 = 退避表长度 + 1：首次 + 2 次重试。
    expect(loader).toHaveBeenCalledTimes(3);
    expect(delays).toEqual([400, 1200]);
  });

  it("次数上限可配置，退避表用尽后沿用最后一个延迟且仍受上限约束", async () => {
    const loader = vi.fn(async () => {
      throw new Error("404 chunk missing");
    });
    const delays: number[] = [];
    const sleep = vi.fn(async (delayMs: number) => {
      delays.push(delayMs);
    });

    await expect(
      loadWithRetry(loader, { attempts: 4, delaysMs: [50, 100], sleep }),
    ).rejects.toBeInstanceOf(ChunkLoadError);

    expect(loader).toHaveBeenCalledTimes(4);
    expect(delays).toEqual([50, 100, 100]);
  });

  it("识别浏览器原生 dynamic import 失败文案为 chunk 加载失败", () => {
    expect(
      isChunkLoadFailure(
        new Error("Failed to fetch dynamically imported module: /assets/page.js"),
      ),
    ).toBe(true);
    expect(isChunkLoadFailure(new Error("unrelated render error"))).toBe(false);
  });
});
