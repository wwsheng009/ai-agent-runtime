import { describe, expect, it } from "vitest";

import {
  compareSessionsByRecency,
  insertSessionInOrder,
  isSameSessionOrder,
  isSessionOrderMode,
  moveSessionInOrder,
  orderSessionsForMode,
  reconcileSessionOrder,
  sessionRecencyTime,
} from "./session-order";

type Candidate = { id: string; updatedAt?: string; createdAt?: string };

function session(id: string, updatedAt?: string): Candidate {
  return updatedAt === undefined ? { id } : { id, updatedAt };
}

describe("session-order", () => {
  describe("sessionRecencyTime", () => {
    it("优先取 updatedAt，缺失时回退 createdAt", () => {
      expect(
        sessionRecencyTime({ id: "s1", updatedAt: "2026-01-02T00:00:00Z" }),
      ).toBe(Date.parse("2026-01-02T00:00:00Z"));
      expect(
        sessionRecencyTime({ id: "s1", createdAt: "2026-01-01T00:00:00Z" }),
      ).toBe(Date.parse("2026-01-01T00:00:00Z"));
    });

    it("时间缺失或非法一律按 -Infinity，不做猜测", () => {
      expect(sessionRecencyTime({ id: "s1" })).toBe(Number.NEGATIVE_INFINITY);
      expect(sessionRecencyTime({ id: "s1", updatedAt: "not-a-date" })).toBe(
        Number.NEGATIVE_INFINITY,
      );
    });
  });

  describe("compareSessionsByRecency", () => {
    it("最近更新在前", () => {
      expect(
        compareSessionsByRecency(
          session("s1", "2026-01-03T00:00:00Z"),
          session("s2", "2026-01-01T00:00:00Z"),
        ),
      ).toBeLessThan(0);
    });

    it("同一时刻按会话 id 升序，保证稳定", () => {
      const stamp = "2026-01-01T00:00:00Z";
      expect(
        compareSessionsByRecency(session("s-a", stamp), session("s-b", stamp)),
      ).toBeLessThan(0);
      expect(
        compareSessionsByRecency(session("s-b", stamp), session("s-a", stamp)),
      ).toBeGreaterThan(0);
    });

    it("时间无法解析的会话排在最后", () => {
      const sorted = [session("s-broken"), session("s-ok", "2020-01-01T00:00:00Z")]
        .sort(compareSessionsByRecency)
        .map((item) => item.id);
      expect(sorted).toEqual(["s-ok", "s-broken"]);
    });
  });

  describe("reconcileSessionOrder", () => {
    it("账目缺省时原样返回当前顺序的副本", () => {
      const current = ["a", "b"];
      const result = reconcileSessionOrder(current, undefined);
      expect(result).toEqual(["a", "b"]);
      expect(result).not.toBe(current);
    });

    it("账目顺序优先，已消失的会话忽略", () => {
      expect(reconcileSessionOrder(["a", "b", "c"], ["c", "gone", "a"])).toEqual([
        "c",
        "a",
        "b",
      ]);
    });

    it("账目里的重复项只保留一次", () => {
      expect(reconcileSessionOrder(["a", "b"], ["b", "b", "a"])).toEqual([
        "b",
        "a",
      ]);
    });

    it("未入账的新会话按当前顺序追加到末尾", () => {
      expect(
        reconcileSessionOrder(["new-2", "old-1", "new-1"], ["old-1"]),
      ).toEqual(["old-1", "new-2", "new-1"]);
    });
  });

  describe("orderSessionsForMode", () => {
    const sessions = [
      session("s-old", "2026-01-01T00:00:00Z"),
      session("s-new", "2026-01-05T00:00:00Z"),
      session("s-mid", "2026-01-03T00:00:00Z"),
    ];

    it("最近更新模式恒按时间实时排序，且忽略账目", () => {
      const ordered = orderSessionsForMode(sessions, "updated", [
        "s-old",
        "s-mid",
        "s-new",
      ]);
      expect(ordered.map((item) => item.id)).toEqual(["s-new", "s-mid", "s-old"]);
    });

    it("手动模式按账目顺序", () => {
      const ordered = orderSessionsForMode(sessions, "manual", [
        "s-old",
        "s-new",
        "s-mid",
      ]);
      expect(ordered.map((item) => item.id)).toEqual(["s-old", "s-new", "s-mid"]);
    });

    it("手动模式无账目时按最近更新呈现（不写账目）", () => {
      const ordered = orderSessionsForMode(sessions, "manual", undefined);
      expect(ordered.map((item) => item.id)).toEqual(["s-new", "s-mid", "s-old"]);
    });

    it("手动模式下未入账的新会话按最近更新追加在末尾", () => {
      const ordered = orderSessionsForMode(
        [...sessions, session("s-fresh", "2026-02-01T00:00:00Z")],
        "manual",
        ["s-mid", "s-old"],
      );
      expect(ordered.map((item) => item.id)).toEqual([
        "s-mid",
        "s-old",
        "s-fresh",
        "s-new",
      ]);
    });

    it("两种模式切换不丢手动顺序（验收：同一账目往返）", () => {
      const account = ["s-old", "s-new", "s-mid"];
      const manual = orderSessionsForMode(sessions, "manual", account);
      const updated = orderSessionsForMode(sessions, "updated", account);
      const backToManual = orderSessionsForMode(
        [...sessions].sort(compareSessionsByRecency),
        "manual",
        account,
      );

      expect(updated.map((item) => item.id)).toEqual(["s-new", "s-mid", "s-old"]);
      expect(backToManual.map((item) => item.id)).toEqual(
        manual.map((item) => item.id),
      );
    });

    it("不改动入参数组", () => {
      const input = [...sessions];
      orderSessionsForMode(input, "updated", undefined);
      expect(input.map((item) => item.id)).toEqual(["s-old", "s-new", "s-mid"]);
    });
  });

  describe("moveSessionInOrder", () => {
    const order = ["a", "b", "c"];

    it("放到目标之前", () => {
      expect(moveSessionInOrder(order, "c", "a", "before")).toEqual([
        "c",
        "a",
        "b",
      ]);
    });

    it("放到目标之后", () => {
      expect(moveSessionInOrder(order, "a", "b", "after")).toEqual([
        "b",
        "a",
        "c",
      ]);
    });

    it("源与目标相同、或任一侧不在顺序里时返回原引用", () => {
      expect(moveSessionInOrder(order, "a", "a", "before")).toBe(order);
      expect(moveSessionInOrder(order, "missing", "a", "before")).toBe(order);
      expect(moveSessionInOrder(order, "a", "missing", "after")).toBe(order);
    });

    it("落点与现状等价时不产生新数组（调用方跳过写入）", () => {
      expect(moveSessionInOrder(order, "b", "c", "before")).toBe(order);
      expect(moveSessionInOrder(order, "b", "a", "after")).toBe(order);
    });
  });

  describe("insertSessionInOrder（跨组落点）", () => {
    const target = ["x", "y", "z"];

    it("会话不在目标组顺序里也能按落点插入", () => {
      expect(insertSessionInOrder(target, "new", "y", "before")).toEqual([
        "x",
        "new",
        "y",
        "z",
      ]);
      expect(insertSessionInOrder(target, "new", "z", "after")).toEqual([
        "x",
        "y",
        "z",
        "new",
      ]);
    });

    it("锚点不在顺序里、或与源相同（同组口径）时返回原引用", () => {
      expect(insertSessionInOrder(target, "new", "missing", "before")).toBe(
        target,
      );
      expect(insertSessionInOrder(target, "x", "x", "after")).toBe(target);
    });

    it("插入结果与现状等价时返回原引用（不写空账目）", () => {
      expect(insertSessionInOrder(target, "y", "x", "after")).toBe(target);
    });
  });

  describe("isSameSessionOrder / isSessionOrderMode", () => {
    it("逐位比较", () => {
      expect(isSameSessionOrder(["a", "b"], ["a", "b"])).toBe(true);
      expect(isSameSessionOrder(["a", "b"], ["b", "a"])).toBe(false);
      expect(isSameSessionOrder(["a"], ["a", "b"])).toBe(false);
    });

    it("只认两种模式，未知值不映射", () => {
      expect(isSessionOrderMode("updated")).toBe(true);
      expect(isSessionOrderMode("manual")).toBe(true);
      expect(isSessionOrderMode("recency")).toBe(false);
      expect(isSessionOrderMode(undefined)).toBe(false);
    });
  });
});
