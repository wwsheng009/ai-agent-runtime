// live 通道（`@/lib/live-stream-text`）的渲染契约。
//
// 优化目标：流式增量不再写页面级 thread state（每次写都会让整棵工作区树重渲染），
// 而是写模块级 store，只通知「正在增长的那一行」。这里的断言就是这条优化的护栏：
// ① 增量到达时**父组件不重渲染**，只有携带 live 键的那一行更新；
// ② 载体（`StreamingMarkdown` / `MessageReasoningRow`）最终显示 live 文本；
// ③ 未携带 live 键的行不订阅（历史消息 / 已定稿行不受干扰）；
// ④ 整体快照路径（`setLiveStreamText`）能覆盖 live 记录 —— 对应 `onResult` 的
//    output/reasoning 快照：只更新 canonical 时必须把 live 拉回来，否则气泡会
//    一直显示旧文本直到定稿。

import { act, useEffect } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";

import { MessageReasoningRow } from "@/components/workspace/message-reasoning-row";
import {
  appendLiveStreamReasoning,
  appendLiveStreamText,
  resetLiveStreamTextStore,
  setLiveStreamReasoning,
  setLiveStreamText,
} from "@/lib/live-stream-text";

import { StreamingMarkdown } from "./segment-components";

beforeAll(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
});

afterEach(() => {
  resetLiveStreamTextStore();
});

/** 让打字机的 rAF 循环跑一会儿（真实定时器，最多等 `budgetMs`）。 */
async function settle(budgetMs = 400) {
  const deadline = Date.now() + budgetMs;
  while (Date.now() < deadline) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 32));
    });
  }
}

async function waitForText(container: HTMLElement, needle: string, budgetMs = 800) {
  const deadline = Date.now() + budgetMs;
  while (!container.textContent?.includes(needle) && Date.now() < deadline) {
    await settle(32);
  }
  return container.textContent?.includes(needle) === true;
}

describe("live 通道渲染", () => {
  it("正文增量只更新挂了 live 键的那一行，父组件不重渲染", async () => {
    const container = document.createElement("div");
    const root = createRoot(container);
    // 渲染次数只在 effect 里累加（react-hooks/globals 不允许在渲染期改写外部变量）。
    const parentRenders = { count: 0 };

    function Parent() {
      useEffect(() => {
        parentRenders.count += 1;
      });
      return <StreamingMarkdown content="seed" liveStreamId="m1" streaming />;
    }

    await act(async () => {
      root.render(<Parent />);
    });
    setLiveStreamText("m1", "seed");
    expect(parentRenders.count).toBe(1);
    expect(container.textContent).toContain("seed");

    // 模拟 SSE 增量：只写 live store，不碰任何 React state。
    await act(async () => {
      appendLiveStreamText("m1", " plus tail");
    });

    expect(await waitForText(container, "plus tail")).toBe(true);
    // 关键不变量：父组件（页面级树）没有被增量惊动。
    expect(parentRenders.count).toBe(1);

    await act(async () => {
      root.unmount();
    });
  });

  it("未携带 live 键的行不订阅：历史消息不会被同名 live 记录改写", async () => {
    const container = document.createElement("div");
    const root = createRoot(container);

    await act(async () => {
      root.render(<StreamingMarkdown content="frozen history" />);
    });
    await act(async () => {
      appendLiveStreamText("m1", " live noise");
    });
    await settle(64);

    expect(container.textContent).toContain("frozen history");
    expect(container.textContent).not.toContain("live noise");

    await act(async () => {
      root.unmount();
    });
  });

  it("增量到达本身不触发渲染：渲染只由打字机节拍驱动", async () => {
    const container = document.createElement("div");
    const root = createRoot(container);
    const parentRenders = { count: 0 };

    function Parent() {
      useEffect(() => {
        parentRenders.count += 1;
      });
      return <StreamingMarkdown content="seed" liveStreamId="m1" streaming />;
    }

    await act(async () => {
      root.render(<Parent />);
    });
    setLiveStreamText("m1", "seed");

    // 一串增量（等价 SSE 突发）：只写 ref，不产生任何 React 渲染。
    const before = parentRenders.count;
    await act(async () => {
      appendLiveStreamText("m1", " one");
      appendLiveStreamText("m1", " two");
      appendLiveStreamText("m1", " three");
    });
    expect(parentRenders.count).toBe(before);

    // 下一帧（打字机节拍）把最新目标顺带揭示出来。
    expect(await waitForText(container, "one two three")).toBe(true);
    expect(parentRenders.count).toBe(before);

    await act(async () => {
      root.unmount();
    });
  });

  it("整体快照覆盖 live 记录（onResult 只更新 canonical 时不再显示陈旧文本）", async () => {
    const container = document.createElement("div");
    const root = createRoot(container);

    await act(async () => {
      root.render(
        <StreamingMarkdown content="seed" liveStreamId="m1" streaming />,
      );
    });
    setLiveStreamText("m1", "seed");
    await act(async () => {
      appendLiveStreamText("m1", " stale");
    });
    expect(await waitForText(container, "stale")).toBe(true);

    // 快照路径：canonical 被整体替换为「live + 快照独有文本」（`onResult` 的
    // output 快照就是这样越过 delta 路径的），live 必须同步到同一份，否则气泡
    // 一直显示快照到达前的旧文本直到定稿（见 use-workspace-agent-chat-turn）。
    await act(async () => {
      setLiveStreamText("m1", "seed stale + snapshot tail");
    });
    expect(await waitForText(container, "snapshot tail")).toBe(true);

    await act(async () => {
      root.unmount();
    });
  });

  it("推理增量即时反映到推理行（该行不走打字机）", async () => {
    const container = document.createElement("div");
    const root = createRoot(container);
    const parentRenders = { count: 0 };

    function Parent() {
      useEffect(() => {
        parentRenders.count += 1;
      });
      return (
        <MessageReasoningRow
          flowKey="k"
          liveStreamId="m1"
          segment={{ type: "reasoning", content: "初始推理" }}
          streaming
        />
      );
    }

    await act(async () => {
      root.render(<Parent />);
    });
    expect(container.textContent).toContain("初始推理");

    // 结构快照把 live 对齐到 store 副本（对应 setStreamingMessage 的同步），
    // 之后的增量追加在这份副本之上。
    await act(async () => {
      setLiveStreamReasoning("m1", "初始推理");
    });
    await act(async () => {
      appendLiveStreamReasoning("m1", " + 新增推理");
    });

    expect(container.textContent).toContain("初始推理 + 新增推理");
    expect(parentRenders.count).toBe(1);

    await act(async () => {
      root.unmount();
    });
  });

  it("live 记录缺失时回落到 store 文本（历史回放 / reload / 已定稿）", async () => {
    const container = document.createElement("div");
    const root = createRoot(container);

    await act(async () => {
      root.render(
        <StreamingMarkdown content="store copy" liveStreamId="m1" streaming />,
      );
    });

    expect(container.textContent).toContain("store copy");

    await act(async () => {
      root.unmount();
    });
  });

  it("live 短于 store 副本时回落：reload / 重连后已显示的正文不被截断", async () => {
    const container = document.createElement("div");
    const root = createRoot(container);

    await act(async () => {
      root.render(
        <StreamingMarkdown
          content="回放出来的长正文"
          liveStreamId="m1"
          streaming
        />,
      );
    });

    // 重连后 live 从「本例首个增量」重新起算：它比 store 副本短，不能顶掉副本。
    await act(async () => {
      appendLiveStreamText("m1", "新增");
    });
    await settle(64);

    expect(container.textContent).toContain("回放出来的长正文");
    expect(container.textContent).not.toContain("新增");

    await act(async () => {
      root.unmount();
    });
  });
});
