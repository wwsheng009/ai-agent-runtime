import { describe, expect, it } from "vitest";

import type { TrajectoryItem } from "@/lib/trajectory/types";

import {
  filterTrajectoryItems,
  trajectoryItemKindLabel,
  trajectoryItemMatches,
  trajectoryItemSummary,
  trajectoryItemText,
} from "./trajectory-view-shared";

function makeItem(overrides: Partial<TrajectoryItem> & { id: string }): TrajectoryItem {
  return {
    seq: 1,
    kind: "assistant",
    causeId: "",
    status: "completed",
    head: { kind: "text", content: "" },
    createdAt: 1,
    updatedAt: 1,
    ...overrides,
  };
}

describe("trajectory-view-shared", () => {
  describe("trajectoryItemKindLabel", () => {
    it("映射全部 kind", () => {
      expect(trajectoryItemKindLabel("assistant")).toBe("message");
      expect(trajectoryItemKindLabel("reasoning")).toBe("reasoning");
      expect(trajectoryItemKindLabel("tool")).toBe("tool");
      expect(trajectoryItemKindLabel("subagent")).toBe("subagent");
      expect(trajectoryItemKindLabel("result")).toBe("result");
    });
  });

  describe("trajectoryItemText / Summary", () => {
    it("text item 提取内容", () => {
      const item = makeItem({
        id: "t1",
        head: { kind: "text", content: "hello\nworld" },
      });
      expect(trajectoryItemText(item)).toBe("hello\nworld");
      expect(trajectoryItemSummary(item)).toBe("hello world");
    });

    it("tool item 拼接 name/args/result", () => {
      const item = makeItem({
        id: "tool1",
        kind: "tool",
        head: {
          kind: "tool",
          name: "bash",
          phase: "finished",
          argsSummary: "ls -la",
          resultSummary: "src",
        },
      });
      expect(trajectoryItemText(item)).toBe("bash\nls -la\nsrc");
      expect(trajectoryItemSummary(item)).toBe("bash ls -la src");
    });

    it("structured item 序列化 payload", () => {
      const item = makeItem({
        id: "p1",
        kind: "planning",
        head: { kind: "structured", payload: { plan: ["read", "write"] } },
      });
      expect(trajectoryItemText(item)).toBe('{"plan":["read","write"]}');
    });

    it("超长摘要裁剪到 maxLength", () => {
      const item = makeItem({
        id: "long",
        head: { kind: "text", content: "x".repeat(500) },
      });
      const summary = trajectoryItemSummary(item, 64);
      expect(summary).toHaveLength(65); // 64 + ellipsis
      expect(summary.endsWith("…")).toBe(true);
    });
  });

  describe("trajectoryItemMatches", () => {
    it("空查询恒匹配", () => {
      const item = makeItem({ id: "a", head: { kind: "text", content: "abc" } });
      expect(trajectoryItemMatches(item, "")).toBe(true);
      expect(trajectoryItemMatches(item, "   ")).toBe(true);
    });

    it("命中内容（大小写不敏感）", () => {
      const item = makeItem({ id: "a", head: { kind: "text", content: "Hello World" } });
      expect(trajectoryItemMatches(item, "hello")).toBe(true);
      expect(trajectoryItemMatches(item, "WORLD")).toBe(true);
      expect(trajectoryItemMatches(item, "missing")).toBe(false);
    });

    it("命中 tool name 与 structured payload", () => {
      const tool = makeItem({
        id: "tool",
        kind: "tool",
        head: { kind: "tool", name: "read_file", phase: "finished" },
      });
      expect(trajectoryItemMatches(tool, "read_file")).toBe(true);

      const planning = makeItem({
        id: "plan",
        kind: "planning",
        head: { kind: "structured", payload: { plan: "implement parser" } },
      });
      expect(trajectoryItemMatches(planning, "parser")).toBe(true);
    });
  });

  describe("trajectoryItemPassesFilter / filterTrajectoryItems", () => {
    const text = makeItem({ id: "text", head: { kind: "text", content: "answer" } });
    const reasoning = makeItem({
      id: "reasoning",
      kind: "reasoning",
      head: { kind: "reasoning", content: "think" },
    });
    const tool = makeItem({
      id: "tool",
      kind: "tool",
      head: { kind: "tool", name: "bash", phase: "finished" },
    });
    const planning = makeItem({
      id: "planning",
      kind: "planning",
      head: { kind: "structured", payload: { plan: "x" } },
    });
    const subagent = makeItem({
      id: "sub",
      kind: "subagent",
      head: { kind: "structured", payload: { agent: "worker" } },
    });
    const all = [text, reasoning, tool, planning, subagent];

    it("all 不过滤", () => {
      expect(filterTrajectoryItems(all, "all", "")).toHaveLength(5);
    });

    it("tools 只含工具", () => {
      expect(filterTrajectoryItems(all, "tools", "").map((i) => i.id)).toEqual(["tool"]);
    });

    it("messages 含 text/reasoning/assistant", () => {
      expect(filterTrajectoryItems(all, "messages", "").map((i) => i.id)).toEqual([
        "text",
        "reasoning",
      ]);
    });

    it("structured 含 G7 事件与 result/system", () => {
      expect(filterTrajectoryItems(all, "structured", "").map((i) => i.id)).toEqual([
        "planning",
        "sub",
      ]);
    });

    it("筛选 + 搜索叠加生效", () => {
      expect(filterTrajectoryItems(all, "all", "answer").map((i) => i.id)).toEqual(["text"]);
      expect(filterTrajectoryItems(all, "tools", "bash").map((i) => i.id)).toEqual(["tool"]);
      expect(filterTrajectoryItems(all, "tools", "answer")).toEqual([]);
    });
  });
});
