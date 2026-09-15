// 冻结前缀（`SettledMarkdownBlocks`）的性能契约。
//
// 为什么需要这条护栏：流式期间打字机每 ~32ms 重渲染一次 `MessageMarkdown`，
// `blocks` 数组每次都随解析结果重建（新数组 + 新对象）。如果 memo 的比较器退化成
// 「按数组身份比较」，前缀里的每个块都会跟着每拍重新解析 markdown —— 而前缀内容
// 逐字未变，这正是「长回答越流越卡」的来源。
//
// 断言用的是「自定义 markdown 组件被渲染了几次」这个可观测事实，而不是内部实现：
// 跳过重渲染 → 段落组件不再渲染 → 计数不变。

import { act, useEffect } from "react";
import { createRoot } from "react-dom/client";
import { type Components } from "react-markdown";
import { afterEach, beforeAll, describe, expect, it } from "vitest";

import {
  SettledMarkdownBlocks,
  type SettledMarkdownBlock,
} from "./settled-blocks";

beforeAll(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
});

let roots: Array<ReturnType<typeof createRoot>> = [];

afterEach(() => {
  for (const root of roots) {
    act(() => root.unmount());
  }
  roots = [];
});

/**
 * 段落探针：记录「渲染次数」与「卸载次数」。
 * 计数放在 effect（含 cleanup）里累加 —— `react-hooks/globals` 禁止渲染期改写外部变量。
 */
function createParagraphProbe() {
  const counters = { renders: 0, unmounts: 0 };
  const components = {
    p: function ProbeParagraph() {
      useEffect(() => {
        counters.renders += 1;
        return () => {
          counters.unmounts += 1;
        };
      });
      return null;
    },
  } as unknown as Components;
  return { counters, components };
}

function renderBlocks(
  container: HTMLElement,
  blocks: SettledMarkdownBlock[],
  components: Components,
) {
  const root = createRoot(container);
  roots.push(root);
  return act(async () => {
    root.render(<SettledMarkdownBlocks blocks={blocks} components={components} />);
  });
}

describe("冻结前缀渲染", () => {
  it("key 未变时整棵前缀跳过重渲染（即使数组与对象都是新的）", async () => {
    const container = document.createElement("div");
    const { counters, components } = createParagraphProbe();

    await renderBlocks(container, [{ key: "k1", content: "第一段" }], components);
    expect(counters.renders).toBe(1);

    // 模拟下一拍：数组与元素对象都是新的，但 key（由块内容派生）相同。
    await act(async () => {
      roots[0].render(
        <SettledMarkdownBlocks
          blocks={[{ key: "k1", content: "第一段" }]}
          components={components}
        />,
      );
    });
    expect(counters.renders).toBe(1);
  });

  it("key 变化（块内容被改写 / 新块冻结）时必须重渲染", async () => {
    const container = document.createElement("div");
    const { counters, components } = createParagraphProbe();

    await renderBlocks(container, [{ key: "k1", content: "第一段" }], components);
    await act(async () => {
      roots[0].render(
        <SettledMarkdownBlocks
          blocks={[
            { key: "k1", content: "第一段" },
            { key: "k2", content: "第二段" },
          ]}
          components={components}
        />,
      );
    });
    expect(counters.renders).toBe(2);
  });

  it("块数量减少（尾块回退）时：被裁掉的块真正卸载，保留的块不重渲染", async () => {
    const container = document.createElement("div");
    const { counters, components } = createParagraphProbe();
    const first = { key: "k1", content: "第一段" };

    await renderBlocks(
      container,
      [first, { key: "k2", content: "第二段" }],
      components,
    );
    expect(counters.renders).toBe(2);
    expect(counters.unmounts).toBe(0);

    await act(async () => {
      roots[0].render(
        <SettledMarkdownBlocks blocks={[first]} components={components} />,
      );
    });
    // 比较器必须按「长度 + 逐项 key」判定不等，父组件才会重渲染并卸掉 k2；
    // 只比较较短长度的实现会漏掉这一拍，k2 会滞留在页面上。
    expect(counters.unmounts).toBe(1);
    // 保留的 k1 内容未变 → 子级 memo 跳过，不产生额外渲染。
    expect(counters.renders).toBe(2);
  });

  it("components 身份变化时必须重渲染（否则会沿用旧的渲染器）", async () => {
    const container = document.createElement("div");
    const { counters, components } = createParagraphProbe();
    const blocks = [{ key: "k1", content: "第一段" }];

    await renderBlocks(container, blocks, components);
    expect(counters.renders).toBe(1);

    const next = createParagraphProbe();
    await act(async () => {
      roots[0].render(
        <SettledMarkdownBlocks
          blocks={[{ key: "k1", content: "第一段" }]}
          components={next.components}
        />,
      );
    });
    expect(next.counters.renders).toBe(1);
  });
});
