// @vitest-environment jsdom

// 会话载荷 LRU 卸载的接线测试：切换时才扫描、最近窗口保留、保护集合实时生效。
import { act, type Dispatch, type SetStateAction } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { type Thread } from "@/data/mock";

import { useWorkspaceThreadPayloadRetention } from "./use-workspace-thread-payload-retention";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function makeThread(id: string): Thread {
  return {
    id,
    title: `title-${id}`,
    summary: "",
    updatedAt: "2026-01-01T00:00:00.000Z",
    status: "active",
    tags: [],
    prompts: [],
    messages: [
      {
        id: `${id}-m1`,
        role: "assistant",
        author: "runtime",
        label: "",
        segments: [{ type: "text", content: id }],
      },
    ],
    artifacts: [],
  };
}

type HarnessProps = {
  selectedThreadId: string | null;
  protectedKeys?: readonly string[];
  setThreads: Dispatch<SetStateAction<Thread[]>>;
  lruSize?: number;
};

function Harness(props: HarnessProps) {
  useWorkspaceThreadPayloadRetention(props);
  return null;
}

describe("useWorkspaceThreadPayloadRetention", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  function lastResult(
    setThreads: ReturnType<typeof vi.fn>,
  ): Thread[] {
    const result = setThreads.mock.results.at(-1)?.value as Thread[];
    expect(Array.isArray(result)).toBe(true);
    return result;
  }

  it("挂载时按选中会话收口：窗口外的 Idle 会话被卸载，选中会话保留", () => {
    const threads = ["a", "b", "c"].map(makeThread);
    const setThreads = vi.fn((action: SetStateAction<Thread[]>) =>
      typeof action === "function" ? action(threads) : action,
    );
    act(() => {
      root.render(
        <Harness
          selectedThreadId="a"
          setThreads={setThreads as unknown as Dispatch<SetStateAction<Thread[]>>}
          lruSize={0}
        />,
      );
    });

    const result = lastResult(setThreads);
    expect(result.find((thread) => thread.id === "a")?.messages).toHaveLength(1);
    expect(result.find((thread) => thread.id === "b")?.messages).toEqual([]);
    expect(result.find((thread) => thread.id === "c")?.messages).toEqual([]);
  });

  it("切换会话时保留最近访问窗口内的会话载荷（含上一个选中）", () => {
    const threads = ["a", "b", "c"].map(makeThread);
    const setThreads = vi.fn((action: SetStateAction<Thread[]>) =>
      typeof action === "function" ? action(threads) : action,
    ) as unknown as Dispatch<SetStateAction<Thread[]>> & ReturnType<typeof vi.fn>;

    act(() => {
      root.render(
        <Harness selectedThreadId="a" setThreads={setThreads} lruSize={1} />,
      );
    });
    act(() => {
      root.render(
        <Harness selectedThreadId="b" setThreads={setThreads} lruSize={1} />,
      );
    });

    const result = lastResult(setThreads);
    expect(result.find((thread) => thread.id === "a")?.messages).toHaveLength(1);
    expect(result.find((thread) => thread.id === "b")?.messages).toHaveLength(1);
    expect(result.find((thread) => thread.id === "c")?.messages).toEqual([]);
  });

  it("protectedKeys 经 ref 实时生效：受保护会话即使不在最近窗口也不卸载", () => {
    const threads = ["a", "b", "c"].map(makeThread);
    const setThreads = vi.fn((action: SetStateAction<Thread[]>) =>
      typeof action === "function" ? action(threads) : action,
    ) as unknown as Dispatch<SetStateAction<Thread[]>> & ReturnType<typeof vi.fn>;

    act(() => {
      root.render(
        <Harness
          selectedThreadId="a"
          protectedKeys={["c"]}
          setThreads={setThreads}
          lruSize={0}
        />,
      );
    });

    const result = lastResult(setThreads);
    expect(result.find((thread) => thread.id === "a")?.messages).toHaveLength(1);
    expect(result.find((thread) => thread.id === "b")?.messages).toEqual([]);
    expect(result.find((thread) => thread.id === "c")?.messages).toHaveLength(1);
  });
});
