// @vitest-environment jsdom

import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useTypewriter } from "./use-typewriter";

function Harness({
  content,
  active,
}: {
  content: string;
  active: boolean;
}) {
  const text = useTypewriter(content, active);
  return <div data-testid="text">{text}</div>;
}

const renderHarness = () => {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  return { host, root };
};

describe("useTypewriter", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    document.body.innerHTML = "";
  });

  const readText = (): string => {
    // 从测试节点读取最后一次渲染结果
    const node = document.querySelector('[data-testid="text"]');
    return node?.textContent ?? "";
  };

  it("returns the full content immediately when not active", () => {
    const { host, root } = renderHarness();
    act(() => {
      root.render(
        <Harness content="hello world" active={false} />,
      );
    });
    expect(readText()).toBe("hello world");
    act(() => {
      root.unmount();
      host.remove();
    });
  });

  it("types progressively while active and catches up with the full content", () => {
    const { host, root } = renderHarness();
    act(() => {
      root.render(<Harness content="" active={true} />);
    });
    expect(readText()).toBe("");

    // 内容到达：应渐进显示，而不是一次性全部渲染
    act(() => {
      root.render(
        <Harness
          content={"A".repeat(600)}
          active={true}
        />,
      );
    });
    // 首个 tick 前仍是空（打字机尚未开始暴露字符）
    expect(readText()).toBe("");

    act(() => {
      vi.advanceTimersByTime(30);
    });
    const partial = readText();
    expect(partial.length).toBeGreaterThan(0);
    expect(partial.length).toBeLessThan(600);

    // 足够长时间后全部追上
    act(() => {
      vi.advanceTimersByTime(30_000);
    });
    expect(readText()).toBe("A".repeat(600));

    act(() => {
      root.unmount();
      host.remove();
    });
  });

  it("flushes the remaining content immediately when active turns off", () => {
    const { host, root } = renderHarness();
    act(() => {
      root.render(<Harness content="" active={true} />);
    });
    act(() => {
      root.render(
        <Harness
          content={"B".repeat(900)}
          active={true}
        />,
      );
    });
    act(() => {
      vi.advanceTimersByTime(30);
    });
    expect(readText().length).toBeGreaterThan(0);
    expect(readText().length).toBeLessThan(900);

    // 结束流式：立即补全
    act(() => {
      root.render(
        <Harness
          content={"B".repeat(900)}
          active={false}
        />,
      );
    });
    expect(readText()).toBe("B".repeat(900));

    act(() => {
      root.unmount();
      host.remove();
    });
  });

  it("never splits surrogate pairs (emoji) at the exposure boundary", () => {
    const { host, root } = renderHarness();
    const emojiText = "🏓".repeat(50); // 每个 emoji 占 2 个 code unit
    act(() => {
      root.render(<Harness content="" active={true} />);
    });
    act(() => {
      root.render(<Harness content={emojiText} active={true} />);
    });
    // 推进一段时间（覆盖多次 tick 的落点）
    act(() => {
      vi.advanceTimersByTime(TimeToShow(emojiText, 1));
    });
    // 暴露的前缀长度必须始终是偶数（不会切开代理对）
    expect(readText().length % 2).toBe(0);
    expect(readText().length).toBeGreaterThan(0);

    // 继续推进直到全部暴露，最终必须是完整内容
    act(() => {
      vi.advanceTimersByTime(30_000);
    });
    expect(readText()).toBe(emojiText);

    act(() => {
      root.unmount();
      host.remove();
    });
  });

  it("restarts typing from the beginning when reactivated", () => {
    const { host, root } = renderHarness();
    // 第一轮：打出部分内容后结束
    act(() => {
      root.render(<Harness content={"A".repeat(300)} active={true} />);
    });
    act(() => {
      vi.advanceTimersByTime(120);
    });
    expect(readText().length).toBeGreaterThan(0);
    act(() => {
      root.render(<Harness content={"A".repeat(300)} active={false} />);
    });
    expect(readText()).toBe("A".repeat(300));

    // 第二轮（新一轮消息）：active 重新为 true，进度必须归零从头打字，
    // 而不是继承上一轮的 shown（否则首帧即全量，打字机失效）。
    act(() => {
      root.render(
        <Harness
          content={"C".repeat(400)}
          active={true}
        />,
      );
    });
    expect(readText()).toBe("");

    act(() => {
      vi.advanceTimersByTime(30);
    });
    expect(readText().length).toBeGreaterThan(0);
    expect(readText().length).toBeLessThan(400);

    // 格式校验：第二轮的文本必须是 C 开头
    expect(readText()[0]).toBe("C");

    act(() => {
      root.unmount();
      host.remove();
    });
  });
});

// 估算从 0 显示全部 text 所需毫秒数（按基础速度 + 加速阈值）
function TimeToShow(text: string, minChars: number): number {
  // 每 tick 至少 1 字符，因此 safe 上界：text 长度 * 30ms
  return text.length * 30 + minChars;
}