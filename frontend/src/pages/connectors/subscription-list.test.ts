// S2b（docs/bots/13 §8）：订阅列表解析辅助——分隔符/去重/回显。

import { describe, expect, it } from "vitest";

import { formatSubscriptionList, parseSubscriptionList } from "./subscription-list";

describe("subscription-list helpers", () => {
  it("parseSubscriptionList：逗号/空格/中文逗号分隔、去空去重保序", () => {
    expect(parseSubscriptionList("assistant_message, turn.*  approval")).toEqual([
      "assistant_message",
      "turn.*",
      "approval",
    ]);
    expect(parseSubscriptionList("a，b， a ,, c")).toEqual(["a", "b", "c"]);
    expect(parseSubscriptionList("  ")).toEqual([]);
  });

  it("formatSubscriptionList：逗号+空格回显；undefined → 空串", () => {
    expect(formatSubscriptionList(["a", "b"])).toBe("a, b");
    expect(formatSubscriptionList(undefined)).toBe("");
  });
});
