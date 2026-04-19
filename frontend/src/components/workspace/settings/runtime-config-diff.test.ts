import { describe, expect, it } from "vitest";

import { buildConfigLineDiff } from "./runtime-config-diff";

describe("buildConfigLineDiff", () => {
  it("marks removed and added lines", () => {
    const diff = buildConfigLineDiff(
      "server:\n  host: 127.0.0.1\nproviders:\n  default_provider: old\n",
      "server:\n  host: 127.0.0.1\nproviders:\n  default_provider: new\n",
    );

    expect(diff.some((line) => line.type === "remove" && line.value.includes("old"))).toBe(
      true,
    );
    expect(diff.some((line) => line.type === "add" && line.value.includes("new"))).toBe(
      true,
    );
  });
});
