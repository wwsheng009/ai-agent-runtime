/**
 * P1-5 golden 向量测试：TS reducer 输出与 golden 摘要一致（对拍 TUI 语义）。
 */
import { describe, expect, it } from "vitest";

import {
  GOLDEN_VECTORS,
  KNOWN_MODEL_DIFFERENCES,
  type GoldenVector,
} from "./golden";
import { applyEvent, makeTrajectoryEvent } from "./trajectory-reducer";
import { createEmptyTrajectory } from "./types";

/** 渲染语义 head：text→content、reasoning→content、tool→name、structured→事件名。 */
function renderHead(item: { head: { kind: string; content?: string; name?: string } }) {
  const head = item.head;
  switch (head.kind) {
    case "text":
    case "reasoning":
      return head.content ?? "";
    case "tool":
      return head.name ?? "";
    case "structured":
      return head.kind;
    default:
      return null;
  }
}

function applyVector(vector: GoldenVector) {
  let snapshot = createEmptyTrajectory();
  for (const event of vector.events) {
    snapshot = applyEvent(
      snapshot,
      makeTrajectoryEvent(event.kind, event.seq, event.payload),
    ).snapshot;
  }
  return {
    itemKinds: snapshot.items.map((item) => item.head.kind),
    statuses: snapshot.items.map((item) => item.status),
    heads: snapshot.items.map((item) => renderHead(item as never)),
    lastEventSeq: snapshot.lastEventSeq,
  };
}

describe("P1-5 golden 向量（对拍 TUI encoder 语义）", () => {
  it("golden 向量数量与命名稳定（TUI 用例一一对应）", () => {
    expect(GOLDEN_VECTORS.map((v) => v.name)).toEqual([
      "basic-sequence",
      "out-of-order",
      "idempotent",
      "reasoning-independent",
    ]);
    for (const vector of GOLDEN_VECTORS) {
      expect(vector.tuiRef).toMatch(/^TestEncode/);
    }
  });

  for (const vector of GOLDEN_VECTORS) {
    it(`${vector.name}（TUI ${vector.tuiRef}）reducer 输出与 golden 一致`, () => {
      const actual = applyVector(vector);
      expect(actual.itemKinds).toEqual(vector.expected.itemKinds);
      expect(actual.statuses).toEqual(vector.expected.statuses);
      expect(actual.heads).toEqual(vector.expected.heads);
      expect(actual.lastEventSeq).toBe(vector.expected.lastEventSeq);
    });
  }

  it("已知模型差异已记录（评审文档不缺失）", () => {
    expect(KNOWN_MODEL_DIFFERENCES.length).toBeGreaterThanOrEqual(4);
    for (const diff of KNOWN_MODEL_DIFFERENCES) {
      expect(diff.length).toBeGreaterThan(10);
    }
  });
});
