import { describe, expect, it } from "vitest";

import { formatToolInputPreview } from "./input-preview";

// 展开面板的「输入」在回放（结构化 JSON）与实时（后端 arg_preview 键值文本）两条链路上
// 必须给出同一套展示：同一次调用不能一边是 JSON、一边是键值文本。
describe("formatToolInputPreview（输入面板的回放/实时统一）", () => {
  it("结构化 JSON 入参 → 键值文本（键按字典序）", () => {
    expect(formatToolInputPreview('{"path":"frontend/e2e","depth":2}')).toBe(
      "depth=2 path=frontend/e2e",
    );
    expect(formatToolInputPreview('{\n  "command": "go test ./...",\n  "timeout": 30\n}')).toBe(
      "command=go test ./... timeout=30",
    );
    expect(formatToolInputPreview('{"query":"useEffect","case_sensitive":true}')).toBe(
      "case_sensitive=true query=useEffect",
    );
  });

  it("ls 形态：回放入参 JSON 与实时预览文本折叠出同一条展示", () => {
    const replayed = formatToolInputPreview('{"depth":2,"path":"backend/internal/toolargs"}');
    const live = formatToolInputPreview("depth=2 path=backend/internal/toolargs");

    expect(replayed).toBe("depth=2 path=backend/internal/toolargs");
    expect(live).toBe(replayed);
  });

  it("标量数组按折叠行同一分隔符连接；字符串值压成单行", () => {
    expect(formatToolInputPreview('{"patterns":["Popover","DialogTrigger"],"path":"src"}')).toBe(
      "path=src patterns=Popover | DialogTrigger",
    );
    expect(formatToolInputPreview('{"command":"ls -la\\n  src"}')).toBe("command=ls -la src");
  });

  it("含嵌套结构 / 非对象 JSON / 解析失败 → 原样保留（不丢字段）", () => {
    const nested = '{"files":[{"file_path":"src/a.ts","limit":20}],"path":"src"}';
    expect(formatToolInputPreview(nested)).toBe(nested);
    expect(formatToolInputPreview('{"options":{"depth":2}}')).toBe('{"options":{"depth":2}}');
    expect(formatToolInputPreview("[1,2,3]")).toBe("[1,2,3]");
    expect(formatToolInputPreview('{"path":')).toBe('{"path":');
  });

  it("已是预览文本 / 空串 → trim 后原样返回", () => {
    expect(formatToolInputPreview("path=frontend/src depth=1")).toBe("path=frontend/src depth=1");
    expect(formatToolInputPreview("  *** Begin Patch  ")).toBe("*** Begin Patch");
    expect(formatToolInputPreview("   ")).toBe("");
    expect(formatToolInputPreview("")).toBe("");
  });

  it("归一后过长 → 退回原始 JSON（不二次截断）", () => {
    const long = `{"command":"${"x".repeat(500)}"}`;
    expect(formatToolInputPreview(long)).toBe(long);
  });
});
