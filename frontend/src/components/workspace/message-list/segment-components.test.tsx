import { act } from "react";
import { createRoot } from "react-dom/client";
import { beforeAll, describe, expect, it } from "vitest";

import { StreamingMarkdown } from "./segment-components";

// 方案 §8.6 / 批次 D3 验收：移除打字机（`useTypewriter`）后
// ① 流式内容按到达节奏直接渲染，不再被 `content.slice(0, shown)` 滞后；
// ② 追加内容不会让已冻结的块 remount 重解析 —— 因此这里断言「同一块」的
//    DOM 节点身份跨追加保持不变（`generation` 一旦误增，渲染 key 变化会让
//    React 卸载重建该段落，节点身份随之改变）。
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

    // 打字机方案首帧 shown=0（只渲染空前缀），内容要到后续 tick 才逐段出现；
    // 直连后首帧即全量可见。
    expect(container.textContent).toContain("Alpha block.");
    expect(container.textContent).toContain("Beta tail is still growing");

    await act(async () => {
      root.unmount();
    });
  });

  it("keeps a frozen block mounted when a chunk lands in one jump", async () => {
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
    // 冲刷 deferred 渲染，避免断言落在未包裹的更新上。
    await act(async () => {});

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
