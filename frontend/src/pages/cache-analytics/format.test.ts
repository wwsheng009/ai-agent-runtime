import { describe, expect, it } from "vitest";

import { formatCacheLatency } from "./format";

describe("formatCacheLatency", () => {
  // 契约：0/缺省 = 未采集（非流式请求、历史记录或首个增量前失败），必须回退到
  // 调用方传入的本地化文案，不得显示 0 ms 冒充首字时间/总耗时。
  it("falls back to the localized placeholder when the observation is missing", () => {
    expect(formatCacheLatency(undefined, "未采集")).toBe("未采集");
    expect(formatCacheLatency(0, "未采集")).toBe("未采集");
    expect(formatCacheLatency(-1, "未采集")).toBe("未采集");
    expect(formatCacheLatency(Number.NaN, "未采集")).toBe("未采集");
    expect(formatCacheLatency(undefined)).toBe("—");
  });

  it("formats observed latencies with the shared duration scale", () => {
    expect(formatCacheLatency(420)).toBe("420 ms");
    expect(formatCacheLatency(1230)).toBe("1.2 s");
    expect(formatCacheLatency(90_000)).toBe("1.5 min");
  });
});
