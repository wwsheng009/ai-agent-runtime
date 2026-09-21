import { describe, expect, it } from "vitest";

import { dimensionOptions, formatFirstToken, normalizeDimensions } from "./format";

describe("formatFirstToken", () => {
  // 契约：0/缺省 = 未采集（非流式请求、历史记录或首个增量前失败），返回 null
  // 让调用方显示本地化「未采集」，不得把 0 ms 当成有效首字时间。
  it("returns null when the first token was never observed", () => {
    expect(formatFirstToken(undefined)).toBeNull();
    expect(formatFirstToken(null)).toBeNull();
    expect(formatFirstToken(0)).toBeNull();
    expect(formatFirstToken(-5)).toBeNull();
    expect(formatFirstToken(Number.NaN)).toBeNull();
  });

  it("formats observed first-token latencies with the shared duration scale", () => {
    expect(formatFirstToken(1)).toBe("1 ms");
    expect(formatFirstToken(420)).toBe("420 ms");
    expect(formatFirstToken(1230)).toBe("1.2 s");
  });
});

describe("normalizeDimensions", () => {
  it("tolerates an empty object payload", () => {
    const dimensions = normalizeDimensions({});
    expect(dimensions.schema_version).toBe("runtime.analytics.v1");
    expect(dimensions.generated_at).toBe("");
    expect(dimensions.providers).toEqual([]);
    expect(dimensions.models).toEqual([]);
    expect(dimensions.directories).toEqual([]);
    expect(dimensions.projects).toEqual([]);
    expect(dimensions.statuses).toEqual([]);
  });

  it("tolerates missing or null payloads", () => {
    for (const raw of [undefined, null]) {
      const dimensions = normalizeDimensions(raw);
      expect(dimensions.providers).toEqual([]);
      expect(dimensions.statuses).toEqual([]);
    }
  });

  it("keeps valid payloads and drops non-string entries", () => {
    const dimensions = normalizeDimensions({
      schema_version: "runtime.analytics.v1",
      generated_at: "2026-09-13T00:00:00Z",
      providers: ["anthropic", 42, null, "openai"],
      models: "claude-sonnet-4",
      directories: ["/repo"],
      projects: undefined,
      statuses: ["ok"],
    });
    expect(dimensions.providers).toEqual(["anthropic", "openai"]);
    expect(dimensions.models).toEqual([]);
    expect(dimensions.directories).toEqual(["/repo"]);
    expect(dimensions.projects).toEqual([]);
    expect(dimensions.statuses).toEqual(["ok"]);
  });
});

describe("dimensionOptions", () => {
  it("does not throw when values are missing", () => {
    expect(dimensionOptions(undefined, "", "All providers")).toEqual([
      { value: "", label: "All providers" },
    ]);
    expect(dimensionOptions(null, "", "All providers")).toEqual([
      { value: "", label: "All providers" },
    ]);
  });

  it("prepends the active filter when it is absent from the payload", () => {
    expect(dimensionOptions(["anthropic"], "openai", "All providers")).toEqual([
      { value: "", label: "All providers" },
      { value: "openai", label: "openai" },
      { value: "anthropic", label: "anthropic" },
    ]);
  });

  it("renders normalized dimensions from a stub backend response", () => {
    const dimensions = normalizeDimensions({});
    expect(dimensionOptions(dimensions.providers, "", "All providers")).toEqual([
      { value: "", label: "All providers" },
    ]);
  });
});
