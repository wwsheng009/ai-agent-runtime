import { act } from "react";
import { createRoot } from "react-dom/client";
import { beforeAll, describe, expect, it } from "vitest";

import { StreamingMarkdown } from "./segment-components";

// 方案 §8.6 / §8.6.1 验收：流式渲染 + 打字机（`useTypewriter`）并存后的两条契约。
//
// 批次 D3 曾因旧打字机的滞后帧把冻结块打回重解析而删掉它；重新引入的打字机
// 改为「单调前缀揭示」（见 `@/lib/typewriter`），因此这里继续守住 D3 的不变量：
// ① 已经完整的内容（首帧 / 历史回放）直挂，不从头重打、不被滞后；
// ② 流式追赶期间的任意中间帧都是上一帧的前缀扩展，追加内容不会让已冻结的块
//    remount 重解析 —— 断言「同一块」的 DOM 节点身份跨追加保持不变（`generation`
//    一旦误增，渲染 key 变化会让 React 卸载重建该段落，节点身份随之改变）。
describe("StreamingMarkdown", () => {
  beforeAll(() => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  });

  it("renders streamed content immediately without typewriter lag", async () => {
    const container = document.createElement("div");
    const root = createRoot(container);

    await act(async () => {
      root.render(
        <StreamingMarkdown
          content={"Alpha block.\n\nBeta tail is still growing"}
          streaming
        />,
      );
    });

    // 打字机「挂载即完整」（`createTypewriterState` 直接全文揭示，只有观察到目标文本
    // 继续增长才进入逐字模式）：首帧就是全量可见，控件挂载 / 历史回放都不被滞后。
    expect(container.textContent).toContain("Alpha block.");
    expect(container.textContent).toContain("Beta tail is still growing");

    await act(async () => {
      root.unmount();
    });
  });

  it("keeps a frozen block mounted while the typewriter catches up", async () => {
    const container = document.createElement("div");
    const root = createRoot(container);
    const firstChunk = "Alpha block.\n\nBeta block.\n\nGamma tail";
    // 整块一次性追加：等价于「打字机来不及逐字追赶」的滞后帧之后到达的大 chunk。
    const secondChunk = `${firstChunk}\n\nDelta block.\n\nEpsilon tail`;

    const frozenParagraph = () =>
      Array.from(container.querySelectorAll("p")).find((node) =>
        node.textContent?.includes("Alpha block."),
      );

    await act(async () => {
      root.render(<StreamingMarkdown content={firstChunk} streaming />);
    });
    const before = frozenParagraph();
    expect(before).toBeTruthy();

    await act(async () => {
      root.render(<StreamingMarkdown content={secondChunk} streaming />);
    });

    // 打字机在 rAF 里追赶：逐帧推进（每帧一个 act 作用域，rAF 触发的揭示才能
    // 落到 DOM 上），有界轮询到新块出现后立即停止。
    const revealedLengths: number[] = [];
    for (let frame = 0; frame < 60; frame += 1) {
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 16));
      });
      revealedLengths.push(container.textContent?.length ?? 0);
      if (container.textContent?.includes("Delta block.")) {
        break;
      }
    }
    // 单调不减：任意中间帧都是上一帧的前缀扩展（冻结块因而不会 remount）。
    for (let index = 1; index < revealedLengths.length; index += 1) {
      expect(revealedLengths[index]).toBeGreaterThanOrEqual(
        revealedLengths[index - 1],
      );
    }

    const alphaParagraphs = Array.from(container.querySelectorAll("p")).filter(
      (node) => node.textContent?.includes("Alpha block."),
    );
    // 节点身份不变 = 冻结块没有被 remount 重解析。
    expect(alphaParagraphs).toEqual([before]);
    expect(container.textContent).toContain("Beta block.");
    expect(container.textContent).toContain("Delta block.");

    await act(async () => {
      root.unmount();
    });
  });
});
