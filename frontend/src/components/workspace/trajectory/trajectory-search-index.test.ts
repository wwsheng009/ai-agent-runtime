import { describe, expect, it } from "vitest";

import type { TrajectoryItem } from "@/lib/trajectory/types";

import {
  buildTrajectorySearchIndex,
  searchTrajectoryIndex,
  tokenizeTrajectoryText,
  trajectorySearchSignature,
} from "./trajectory-search-index";

function item(
  id: string,
  updatedAt: number,
  text: string,
  kind: TrajectoryItem["kind"] = "assistant",
): TrajectoryItem {
  return {
    id,
    seq: 1,
    kind,
    causeId: "",
    status: "completed",
    createdAt: updatedAt,
    updatedAt,
    head: { kind: "text", content: text },
  };
}

const TOOL_ITEM: TrajectoryItem = {
  id: "tool:call-1",
  seq: 2,
  kind: "tool",
  causeId: "",
  status: "completed",
  createdAt: 3,
  updatedAt: 2,
  head: {
    kind: "tool",
    name: "web_search",
    phase: "finished",
    argsSummary: "query=Paris capital",
    resultSummary: "Paris is the capital of France.",
  },
};

describe("tokenizeTrajectoryText", () => {
  it("小写化并按标点/空白切分", () => {
    expect(tokenizeTrajectoryText("Hello, World! foo_bar")).toEqual([
      "hello",
      "world",
      "foo_bar",
    ]);
  });

  it("保留中文与数字", () => {
    expect(tokenizeTrajectoryText("查找 巴黎 2026 事件")).toEqual([
      "查找",
      "巴黎",
      "2026",
      "事件",
    ]);
  });

  it("空文本返回空数组", () => {
    expect(tokenizeTrajectoryText("   ")).toEqual([]);
  });
});

describe("trajectorySearchSignature", () => {
  it("相同集合签名相同", () => {
    const a = [item("a", 1, "x"), item("b", 2, "y")];
    const b = [item("a", 1, "x"), item("b", 2, "y")];
    expect(trajectorySearchSignature(a)).toBe(trajectorySearchSignature(b));
  });

  it("updatedAt 变化（内容更新）签名变化", () => {
    expect(trajectorySearchSignature([item("a", 1, "x")])).not.toBe(
      trajectorySearchSignature([item("a", 2, "x")]),
    );
  });

  it("顺序变化签名变化", () => {
    expect(
      trajectorySearchSignature([item("a", 1, "x"), item("b", 1, "y")]),
    ).not.toBe(
      trajectorySearchSignature([item("b", 1, "y"), item("a", 1, "x")]),
    );
  });
});

describe("buildTrajectorySearchIndex / searchTrajectoryIndex", () => {
  it("索引工具 item 的 name/args/result 文本", () => {
    const index = buildTrajectorySearchIndex([TOOL_ITEM]);
    expect(index.terms.get("web_search")).toEqual(new Set(["tool:call-1"]));
    expect(index.terms.get("paris")).toEqual(new Set(["tool:call-1"]));
    expect(index.terms.get("france")).toEqual(new Set(["tool:call-1"]));
  });

  it("单词命中", () => {
    const index = buildTrajectorySearchIndex([
      item("a", 1, "Paris is nice"),
      item("b", 1, "Berlin is cold"),
    ]);
    expect(searchTrajectoryIndex(index, "paris")).toEqual(new Set(["a"]));
    expect(searchTrajectoryIndex(index, "is")).toEqual(new Set(["a", "b"]));
  });

  it("多词 AND 语义：全部词命中才返回", () => {
    const index = buildTrajectorySearchIndex([
      item("a", 1, "Paris capital France"),
      item("b", 1, "Paris weather"),
      item("c", 1, "capital city"),
    ]);
    expect(searchTrajectoryIndex(index, "paris capital")).toEqual(
      new Set(["a"]),
    );
    expect(searchTrajectoryIndex(index, "paris capital france")).toEqual(
      new Set(["a"]),
    );
  });

  it("无命中返回空集合", () => {
    const index = buildTrajectorySearchIndex([item("a", 1, "hello")]);
    expect(searchTrajectoryIndex(index, "missing")).toEqual(new Set());
  });

  it("空查询返回 null（调用方走全量路径）", () => {
    const index = buildTrajectorySearchIndex([item("a", 1, "hello")]);
    expect(searchTrajectoryIndex(index, "   ")).toBeNull();
  });

  it("空索引安全", () => {
    const index = buildTrajectorySearchIndex([]);
    expect(searchTrajectoryIndex(index, "anything")).toEqual(new Set());
  });
});
