import { describe, expect, it } from "vitest";

import { classifyToolCategory } from "./tool-category";

describe("classifyToolCategory", () => {
  it("classifies namespaced MCP tools", () => {
    expect(classifyToolCategory("mcp__github__search_issues")).toBe("mcp");
  });

  it("classifies shell aliases without changing the original tool name", () => {
    expect(classifyToolCategory("shell")).toBe("shell");
    expect(classifyToolCategory("BASH")).toBe("shell");
    expect(classifyToolCategory("execute_shell_command")).toBe("shell");
    expect(classifyToolCategory("aicli_exec")).toBe("shell");
  });

  it("classifies other tool names as built-in tools", () => {
    expect(classifyToolCategory("view")).toBe("builtin");
    expect(classifyToolCategory("  apply_patch  ")).toBe("builtin");
  });
});
