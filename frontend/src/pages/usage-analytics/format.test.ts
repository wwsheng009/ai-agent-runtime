import { describe, expect, it } from "vitest";

import { dimensionOptions, normalizeDimensions } from "./format";

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
