import { describe, expect, it } from "vitest";

import {
  convertConfigValueKind,
  getConfigValueAtPath,
  removeConfigValueAtPath,
  setConfigValueAtPath,
} from "./runtime-config-editor-utils";

describe("runtime config editor utils", () => {
  it("updates nested object paths", () => {
    const root = {
      providers: {
        items: {
          codex_fox: {
            enabled: true,
          },
        },
      },
    };

    const next = setConfigValueAtPath(
      root,
      ["providers", "items", "codex_fox", "enabled"],
      false,
    ) as Record<string, unknown>;

    expect(getConfigValueAtPath(next, ["providers", "items", "codex_fox", "enabled"])).toBe(
      false,
    );
    expect(getConfigValueAtPath(root, ["providers", "items", "codex_fox", "enabled"])).toBe(
      true,
    );
  });

  it("removes array items without mutating the source", () => {
    const root = {
      provider_groups: [{ name: "primary" }, { name: "standby" }],
    };

    const next = removeConfigValueAtPath(root, ["provider_groups", 0]) as {
      provider_groups: Array<{ name: string }>;
    };

    expect(next.provider_groups).toHaveLength(1);
    expect(next.provider_groups[0].name).toBe("standby");
    expect(root.provider_groups).toHaveLength(2);
  });

  it("converts primitive kinds predictably", () => {
    expect(convertConfigValueKind("12", "number")).toBe(12);
    expect(convertConfigValueKind(0, "boolean")).toBe(false);
    expect(convertConfigValueKind(true, "string")).toBe("true");
  });
});
