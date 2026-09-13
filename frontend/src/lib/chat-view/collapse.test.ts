import { describe, expect, it } from "vitest";

import type { MessageSegment } from "@/data/mock";

import { findFinalAnswerStart, summarizeCollapsedEvidence } from "./collapse";
import { buildNodes } from "./nodes";

function nodesOf(segments: MessageSegment[]) {
  return buildNodes("assistant-1", segments);
}

describe("findFinalAnswerStart", () => {
  it("工具调用后跟文本：边界落在文本段，工具/推理可折叠", () => {
    const nodes = nodesOf([
      { type: "reasoning", content: "plan" },
      { type: "tool", name: "read", status: "finished", resultSummary: "ok" },
      { type: "text", content: "Done." },
    ]);

    expect(findFinalAnswerStart(nodes)).toBe(2);
    expect(nodes.slice(0, 2).every((node) => node.evidence)).toBe(true);
  });

  it("无 final answer（末尾仍是工具/推理）时不折叠", () => {
    const nodes = nodesOf([
      { type: "reasoning", content: "thinking" },
      { type: "tool", name: "grep", status: "running" },
    ]);

    expect(findFinalAnswerStart(nodes)).toBe(-1);
  });

  it("纯工具 Turn：无最终回答", () => {
    const nodes = nodesOf([
      { type: "tool", name: "read", status: "finished", resultSummary: "ok" },
      { type: "tool", name: "edit", status: "finished", resultSummary: "ok" },
    ]);

    expect(findFinalAnswerStart(nodes)).toBe(-1);
  });

  it("空白文本不构成最终回答", () => {
    const nodes = nodesOf([
      { type: "tool", name: "read", status: "finished" },
      { type: "text", content: "   \n" },
    ]);

    expect(findFinalAnswerStart(nodes)).toBe(-1);
  });

  it("仅富内容收尾（无文本/图片）不构成最终回答", () => {
    const nodes = nodesOf([
      { type: "tool", name: "read", status: "finished" },
      { type: "code", language: "ts", code: "const a = 1;" },
    ]);

    expect(findFinalAnswerStart(nodes)).toBe(-1);
  });
});

describe("summarizeCollapsedEvidence", () => {
  it("汇总工具数与带回复条数并省略零值段", () => {
    const nodes = nodesOf([
      { type: "tool", name: "read", status: "finished", resultSummary: "ok" },
      { type: "tool", name: "edit", status: "error", errorMessage: "boom" },
      { type: "tool", name: "grep", status: "running" },
      { type: "reasoning", content: "x" },
    ]);

    const summary = summarizeCollapsedEvidence(nodes);

    expect(summary.tools).toBe(3);
    expect(summary.replies).toBe(2);
    expect(summary.parts).toEqual([
      { kind: "tools", count: 3 },
      { kind: "replies", count: 2 },
    ]);
    expect(summary.empty).toBe(false);
  });

  it("全零过程证据（仅推理）时 empty = true", () => {
    const summary = summarizeCollapsedEvidence(
      nodesOf([{ type: "reasoning", content: "thought" }]),
    );

    expect(summary.parts).toEqual([]);
    expect(summary.empty).toBe(true);
  });

  it("子代理计数来自调用方（轨迹侧统计）", () => {
    const summary = summarizeCollapsedEvidence(
      nodesOf([
        { type: "tool", name: "task", status: "finished", resultSummary: "ok" },
      ]),
      { subagents: 2 },
    );

    expect(summary.parts).toEqual([
      { kind: "tools", count: 1 },
      { kind: "replies", count: 1 },
      { kind: "subagents", count: 2 },
    ]);
  });
});
