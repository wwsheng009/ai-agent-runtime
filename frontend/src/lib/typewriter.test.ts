import { describe, expect, it } from "vitest";

import {
  advanceOnTargetChange,
  advanceReveal,
  BASE_CPS,
  commonPrefixLength,
  createTypewriterState,
  MAX_CPS,
  MAX_FRAME_MS,
  snapToCodePointBoundary,
  visibleTypewriterText,
  type TypewriterState,
} from "./typewriter";

const FULL = createTypewriterState("Alpha block.\n\nBeta tail is still growing");

/** 反复按帧推进直到追平；返回过程里出现过的所有可见文本。 */
function runToCompletion(
  start: TypewriterState,
  options: { elapsedMs?: number; maxFrames?: number } = {},
): { frames: string[]; state: TypewriterState } {
  const elapsedMs = options.elapsedMs ?? 16;
  const maxFrames = options.maxFrames ?? 2000;
  const frames: string[] = [];
  let state = start;
  for (let index = 0; index < maxFrames && state.animating; index += 1) {
    const next = advanceReveal(state, elapsedMs);
    if (next === state) {
      break;
    }
    state = next;
    frames.push(visibleTypewriterText(state, state.target, true));
  }
  return { frames, state };
}

describe("typewriter 状态机", () => {
  it("挂载即完整：历史回放 / 首次渲染不从头重打", () => {
    const state = createTypewriterState("已经完整的历史消息");
    expect(state.revealed).toBe("已经完整的历史消息".length);
    expect(state.animating).toBe(false);
    expect(visibleTypewriterText(state, state.target, true)).toBe(
      "已经完整的历史消息",
    );
  });

  it("纯追加进入逐字模式：揭示量从上一轮全长起步且只增不减", () => {
    const first = advanceOnTargetChange(FULL, `${FULL.target} Delta`, true);
    expect(first.revealed).toBe(FULL.target.length);
    expect(first.animating).toBe(true);

    const { frames, state } = runToCompletion(first);
    expect(state.animating).toBe(false);
    expect(state.revealed).toBe(state.target.length);
    // 中间帧全部是最终文本的前缀扩展（冻结块因此不会 remount）。
    for (let index = 1; index < frames.length; index += 1) {
      expect(frames[index].startsWith(frames[index - 1])).toBe(true);
      expect(frames[index].length).toBeGreaterThanOrEqual(frames[index - 1].length);
    }
  });

  it("打字期间再来大 chunk：水位不回退，且是最新目标的前缀", () => {
    const typing = advanceOnTargetChange(FULL, `${FULL.target} 增量一`, true);
    const mid = advanceReveal(typing, 16);
    expect(mid.revealed).toBeGreaterThanOrEqual(typing.revealed);

    const next = advanceOnTargetChange(mid, `${mid.target} 增量二`, true);
    // 水位保留（不回退）：旧实现的问题正是在这里把 shown 归零重打。
    expect(next.revealed).toBe(mid.revealed);
    expect(next.animating).toBe(true);

    const { state } = runToCompletion(next);
    expect(state.revealed).toBe(state.target.length);
  });

  it("改写（非纯追加）直接全量显示，绝不从头重打", () => {
    const typing = advanceOnTargetChange(FULL, `${FULL.target} 尾巴`, true);
    const rewritten = advanceOnTargetChange(typing, "完全不同的内容", true);
    expect(rewritten.revealed).toBe("完全不同的内容".length);
    expect(rewritten.animating).toBe(false);
  });

  it("active=false（历史 / 已结束 / 被打断）零成本直挂全文", () => {
    const inactive = advanceOnTargetChange(FULL, `${FULL.target} 新增`, false);
    expect(inactive.animating).toBe(false);
    expect(visibleTypewriterText(inactive, inactive.target, false)).toBe(
      inactive.target,
    );

    // 动画进行中被打断：立刻回到全文，不被 slice 滞后。
    const typing = advanceOnTargetChange(FULL, `${FULL.target} 新增`, true);
    expect(visibleTypewriterText(typing, typing.target, false)).toBe(
      typing.target,
    );
  });

  it("自适应追赶：积压越快速率越高，且不超上限", () => {
    const small = advanceOnTargetChange(FULL, `${FULL.target}abc`, true);
    const bigPending = `${FULL.target}${"x".repeat(2000)}`;
    const big = advanceOnTargetChange(FULL, bigPending, true);

    const smallStep = advanceReveal(small, 16).revealed - small.revealed;
    const bigStep = advanceReveal(big, 16).revealed - big.revealed;
    expect(bigStep).toBeGreaterThan(smallStep);

    // 单帧上限：MAX_CPS * MAX_FRAME_MS 的字符数（+1 让出取整误差）。
    const capped = advanceOnTargetChange(createTypewriterState(""), "y".repeat(200_000), true);
    const cappedStep = advanceReveal(capped, 1000).revealed - capped.revealed;
    expect(cappedStep).toBeLessThanOrEqual(
      Math.ceil((MAX_CPS * MAX_FRAME_MS) / 1000) + 1,
    );
    expect(cappedStep).toBeGreaterThan(Math.ceil((BASE_CPS * 16) / 1000));
  });

  it("揭示落点吸附码点边界：emoji 不会被劈成半个", () => {
    const text = `head ${"😀".repeat(8)} tail`;
    let state = advanceOnTargetChange(createTypewriterState(""), text, true);
    const { frames, state: done } = runToCompletion(state, { elapsedMs: 1 });
    state = done;
    expect(state.revealed).toBe(text.length);
    for (const frame of frames) {
      // 代理对必须成对出现：否则会出现落单的 high surrogate。
      expect(/[\uD800-\uDBFF](?![\uDC00-\uDFFF])/.test(frame)).toBe(false);
    }
  });

  it("追平后不再产生新状态（循环自然停止）", () => {
    const done: TypewriterState = {
      target: "abc",
      revealed: 3,
      animating: false,
    };
    expect(advanceReveal(done, 16)).toBe(done);
    const raced = advanceReveal({ target: "abc", revealed: 3, animating: true }, 16);
    expect(raced.animating).toBe(false);
    expect(raced.revealed).toBe(3);
  });

  it("公共前缀与边界吸附的边界情况", () => {
    expect(commonPrefixLength("abc", "abd")).toBe(2);
    expect(commonPrefixLength("abc", "abc")).toBe(3);
    expect(commonPrefixLength("", "abc")).toBe(0);
    expect(snapToCodePointBoundary("abc", 0, 3)).toBe(3);
    const pair = "a😀b";
    // 落点 2 正好是 low surrogate：退到代理对起点（1 > from=0）。
    expect(snapToCodePointBoundary(pair, 0, 2)).toBe(1);
    // from 已经等于代理对起点时不能退（否则没有进展），整对揭示。
    expect(snapToCodePointBoundary(pair, 1, 2)).toBe(3);
  });
});
