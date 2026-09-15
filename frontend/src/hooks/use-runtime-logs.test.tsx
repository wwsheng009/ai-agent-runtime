// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useRuntimeLogs } from "@/hooks/use-runtime-logs";

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return {
    ...actual,
    listRuntimeLogs: vi.fn(),
    streamRuntimeLogs: vi.fn(),
  };
});

import { listRuntimeLogs, streamRuntimeLogs } from "@/lib/runtime-api";
import type { RuntimeLogsResponse } from "@/types/runtime";

const mockList = vi.mocked(listRuntimeLogs);
const mockStream = vi.mocked(streamRuntimeLogs);

void streamRuntimeLogs;

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

/**
 * 探针组件：把 hook 的关键状态渲染成文本。
 * 载荷缺字段时若 state 被写成 undefined，这里就会在 entries.find /
 * entries.some 上抛 TypeError，整条 /logs 路由被打成错误面。
 */
function LogsProbe() {
  const { entries, error, loading, selectedEntry } = useRuntimeLogs({
    follow: true,
    level: "",
    onSelectedCursorChange: () => {},
    query: "",
    selectedCursor: null,
  });

  return (
    <p data-testid="probe">{`${String(loading)}|${String(entries.length)}|${
      selectedEntry?.cursor ?? "none"
    }|${error ?? ""}`}</p>
  );
}

function probeText(): string {
  return document.querySelector('[data-testid="probe"]')?.textContent ?? "";
}

describe("useRuntimeLogs（列表载荷防御）", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    vi.spyOn(console, "error").mockImplementation(() => {});
    // 流式连接不参与这些用例：保持挂起，避免重连噪声。
    mockStream.mockImplementation(() => new Promise<void>(() => {}));
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
    vi.restoreAllMocks();
    vi.clearAllMocks();
  });

  it("载荷缺 entries 时降级为空列表，不把路由打成错误面", async () => {
    mockList.mockResolvedValue({
      count: 0,
      next_cursor: 0,
    } as unknown as RuntimeLogsResponse);

    await act(async () => {
      root.render(<LogsProbe />);
    });

    expect(probeText()).toBe("false|0|none|");
  });

  it("正常载荷渲染条目并把选中游标回落到首条", async () => {
    mockList.mockResolvedValue({
      count: 1,
      entries: [
        {
          cursor: 7,
          raw_text: "runtime-server started",
          level: "info",
          message: "runtime-server started",
        },
      ],
      next_cursor: 8,
    });

    await act(async () => {
      root.render(<LogsProbe />);
    });

    expect(probeText()).toBe("false|1|7|");
  });

  it("请求失败时清空列表并在页面上暴露错误", async () => {
    mockList.mockRejectedValue(new Error("boom"));

    await act(async () => {
      root.render(<LogsProbe />);
    });

    expect(probeText()).toBe("false|0|none|boom");
  });
});
