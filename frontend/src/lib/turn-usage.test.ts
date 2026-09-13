import { describe, expect, it } from "vitest";

import { isCompleteTurnUsage, readTurnUsage } from "./turn-usage";

describe("readTurnUsage", () => {
  it("归一化 provider 原样键（prompt/completion/total）", () => {
    expect(
      readTurnUsage({
        usage: { prompt_tokens: 1200, completion_tokens: 300, total_tokens: 1500 },
      }),
    ).toEqual({ promptTokens: 1200, completionTokens: 300, totalTokens: 1500 });
  });

  it("兼容 runtime 前缀键，并在缺 total 时用输入+输出补齐", () => {
    expect(
      readTurnUsage({
        usage: { usage_prompt_tokens: 40, usage_completion_tokens: 2 },
      }),
    ).toEqual({ promptTokens: 40, completionTokens: 2, totalTokens: 42 });
  });

  it("缺输入或输出任一 → 不完整，返回 null（渲染层整行隐藏）", () => {
    expect(readTurnUsage({ usage: { prompt_tokens: 100 } })).toBeNull();
    expect(readTurnUsage({ usage: { completion_tokens: 100 } })).toBeNull();
    expect(readTurnUsage({ usage: { total_tokens: 100 } })).toBeNull();
  });

  it("非法值与全零用量返回 null", () => {
    expect(
      readTurnUsage({
        usage: { prompt_tokens: "n/a", completion_tokens: 10 },
      }),
    ).toBeNull();
    expect(
      readTurnUsage({
        usage: { prompt_tokens: -1, completion_tokens: 10 },
      }),
    ).toBeNull();
    expect(
      readTurnUsage({
        usage: { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 },
      }),
    ).toBeNull();
  });

  it("空输入返回 null", () => {
    expect(readTurnUsage(null)).toBeNull();
    expect(readTurnUsage(undefined)).toBeNull();
    expect(readTurnUsage({})).toBeNull();
    expect(readTurnUsage({ usage: null })).toBeNull();
  });
});

describe("isCompleteTurnUsage", () => {
  it("只接受完整且非全零的用量对象", () => {
    expect(
      isCompleteTurnUsage({
        promptTokens: 1,
        completionTokens: 0,
        totalTokens: 1,
      }),
    ).toBe(true);
    expect(
      isCompleteTurnUsage({
        promptTokens: 0,
        completionTokens: 0,
        totalTokens: 0,
      }),
    ).toBe(false);
    expect(isCompleteTurnUsage({ promptTokens: 10 })).toBe(false);
    expect(isCompleteTurnUsage("1200")).toBe(false);
    expect(isCompleteTurnUsage(null)).toBe(false);
  });
});
