import { describe, expect, it, vi } from "vitest";

import {
  appendLiveStreamReasoning,
  appendLiveStreamText,
  clearLiveStreamText,
  getLiveStreamEntry,
  resetLiveStreamTextStore,
  setLiveStreamReasoning,
  setLiveStreamText,
  subscribeLiveStreamText,
} from "./live-stream-text";

// live 通道是模块级外部 store：用例之间必须复位，否则串味。
function reset() {
  resetLiveStreamTextStore();
}

describe("live-stream-text", () => {
  it("按到达顺序累加增量，且不同消息互不干扰", () => {
    reset();
    appendLiveStreamText("m1", "Hello");
    appendLiveStreamText("m1", " world");
    appendLiveStreamReasoning("m1", "think");
    appendLiveStreamText("m2", "other");

    expect(getLiveStreamEntry("m1")).toEqual({
      reasoningText: "think",
      text: "Hello world",
    });
    expect(getLiveStreamEntry("m2")).toEqual({ reasoningText: "", text: "other" });
    expect(getLiveStreamEntry("missing")).toBeNull();
    reset();
  });

  it("保留原始空白（不 trim）：增量语义下换行/缩进属于正文", () => {
    reset();
    appendLiveStreamText("m1", "\n  ");
    appendLiveStreamText("m1", "code");

    expect(getLiveStreamEntry("m1")?.text).toBe("\n  code");
    reset();
  });

  it("忽略空消息 id 与空增量，不建条目", () => {
    reset();
    appendLiveStreamText("", "x");
    appendLiveStreamText("m1", "");

    expect(getLiveStreamEntry("m1")).toBeNull();
    expect(getLiveStreamEntry("")).toBeNull();
    reset();
  });

  it("set* 为幂等覆盖：同一文本不换引用（订阅方不会白重渲染）", () => {
    reset();
    setLiveStreamText("m1", "abc");
    const first = getLiveStreamEntry("m1");
    setLiveStreamText("m1", "abc");
    expect(getLiveStreamEntry("m1")).toBe(first);

    setLiveStreamText("m1", "abcd");
    expect(getLiveStreamEntry("m1")).not.toBe(first);
    expect(getLiveStreamEntry("m1")?.text).toBe("abcd");
    reset();
  });

  it("正文与推理同时清空时条目整体移除", () => {
    reset();
    setLiveStreamReasoning("m1", "r");
    expect(getLiveStreamEntry("m1")).toEqual({ reasoningText: "r", text: "" });
    setLiveStreamReasoning("m1", "");
    expect(getLiveStreamEntry("m1")).toBeNull();
    reset();
  });

  it("clear 丢弃条目；对不存在的 id 不通知订阅者", () => {
    reset();
    const listener = vi.fn();
    const unsubscribe = subscribeLiveStreamText(listener);

    appendLiveStreamText("m1", "a");
    expect(listener).toHaveBeenCalledTimes(1);
    clearLiveStreamText("m1");
    expect(listener).toHaveBeenCalledTimes(2);
    expect(getLiveStreamEntry("m1")).toBeNull();

    clearLiveStreamText("m1");
    expect(listener).toHaveBeenCalledTimes(2);

    unsubscribe();
    appendLiveStreamText("m1", "b");
    expect(listener).toHaveBeenCalledTimes(2);
    reset();
  });

  it("同文本写入不触发通知（避免无变化重渲染）", () => {
    reset();
    setLiveStreamText("m1", "same");
    const listener = vi.fn();
    const unsubscribe = subscribeLiveStreamText(listener);

    setLiveStreamText("m1", "same");
    expect(listener).not.toHaveBeenCalled();
    setLiveStreamText("m1", "same!");
    expect(listener).toHaveBeenCalledTimes(1);
    unsubscribe();
    reset();
  });
});
