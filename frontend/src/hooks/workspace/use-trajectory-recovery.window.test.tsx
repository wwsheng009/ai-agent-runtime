// @vitest-environment jsdom

/**
 * 尾部优先回放的**窗口分页**用例（从 use-trajectory-recovery.test.tsx 拆出，
 * 满足 P0-2 单文件 ≤ 500 非空行）：首屏尾部窗口 → 「加载更早」按 before_seq
 * 前插 → 窗口未推进到末尾时按 after 补齐 → 回放日志裁剪后的降级。
 */
import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  fetchSessionRuntimeEvents,
  getSessionHistory,
} from "@/api/runtime/sessions";
import {
  createTrajectoryStore,
  type TrajectoryStore,
} from "@/hooks/workspace/use-trajectory-snapshot";

import { useTrajectoryRecovery } from "./use-trajectory-recovery";

vi.mock("@/api/runtime/sessions", () => ({
  fetchSessionRuntimeEvents: vi.fn(),
  getSessionHistory: vi.fn(),
}));

const mockFetch = vi.mocked(fetchSessionRuntimeEvents);
const mockHistory = vi.mocked(getSessionHistory);

function chatSseEvent(kind: string, seq: number, extra: Record<string, unknown> = {}) {
  return {
    type: `chat.sse.${kind}`,
    timestamp: "2026-08-16T00:00:00Z",
    payload: { ...extra, seq },
  };
}

type RecoveryApi = ReturnType<typeof useTrajectoryRecovery>;

/** 暴露 hook 返回值：断言窗口状态 + 主动触发「加载更早」。 */
function ApiHarness({
  store,
  sessionId,
  onApi,
}: {
  store: TrajectoryStore;
  sessionId: string | undefined;
  onApi: (api: RecoveryApi) => void;
}) {
  const api = useTrajectoryRecovery({ store, sessionId });
  useEffect(() => {
    onApi(api);
  });
  return null;
}

function requireApi(api: RecoveryApi | null): RecoveryApi {
  if (!api) {
    throw new Error("recovery api 尚未就绪");
  }
  return api;
}

/** 轨迹视图可见的文本（连续文本帧会合并成一项，故按拼接串判断先后）。 */
function visibleText(store: TrajectoryStore): string {
  return store
    .getSnapshot()
    .items.filter((item) => item.head.kind === "text")
    .map((item) => (item.head.kind === "text" ? item.head.content : ""))
    .join("");
}

describe("useTrajectoryRecovery（窗口分页）", () => {
  let container: HTMLDivElement;
  let root: Root;
  let store: TrajectoryStore;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    store = createTrajectoryStore();
    mockFetch.mockReset();
    mockHistory.mockReset();
    // 默认：会话历史为空（窗口用例不依赖兜底投影）。
    mockHistory.mockResolvedValue({
      session_id: "session-1",
      count: 0,
      history: [],
    });
  });

  afterEach(() => {
    act(() => {
      root.unmount();
      store.dispose();
    });
    container.remove();
  });

  it("窗口确认有更早内容时向前翻页：before_seq 前插更早事件，取尽后关闭入口", async () => {
    // 首屏尾部窗口：只有最近一页（seq 3），服务端确认还有更早内容。
    mockFetch.mockResolvedValueOnce({
      events: [chatSseEvent("chunk", 3, { type: "text", content: "最近的消息" })],
      count: 1,
      latest_seq: 3,
      first_seq: 3,
      has_more: true,
    });

    let api: RecoveryApi | null = null;
    act(() => {
      root.render(
        <ApiHarness
          store={store}
          sessionId="session-1"
          onApi={(next) => {
            api = next;
          }}
        />,
      );
    });

    await vi.waitFor(() => {
      expect(api?.window.hasMore).toBe(true);
    });
    expect(requireApi(api).ready).toBe(true);
    expect(requireApi(api).window.firstSeq).toBe(3);
    // 首屏只有一次请求（尾部窗口），更早内容尚未拉取。
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(visibleText(store)).toContain("最近的消息");
    expect(visibleText(store)).not.toContain("更早的消息");

    // 向前一页：seq 1..2（与窗口首 seq=3 相接），服务端确认已到日志开头。
    mockFetch.mockResolvedValueOnce({
      events: [
        chatSseEvent("chunk", 1, { type: "text", content: "更早的消息" }),
        chatSseEvent("chunk", 2, { type: "text", content: "更早的回答" }),
      ],
      count: 2,
      latest_seq: 3,
      first_seq: 1,
      has_more: false,
    });

    await act(async () => {
      await requireApi(api).loadEarlier();
    });

    // 排他上界 = 已回放窗口的最早 seq（不会重复拉取窗口内事件）。
    expect(mockFetch).toHaveBeenLastCalledWith("session-1", {
      beforeSeq: 3,
      limit: 500,
    });
    // 前插后投影与「一次性回放全部已加载事件」一致：更早内容排在窗口内容之前。
    const text = visibleText(store);
    expect(text).toContain("更早的消息");
    expect(text).toContain("更早的回答");
    expect(text.indexOf("更早的消息")).toBeLessThan(text.indexOf("最近的消息"));
    // 已到日志开头 → 视图不再显示「加载更早」入口。
    expect(requireApi(api).window.hasMore).toBe(false);
    expect(requireApi(api).window.firstSeq).toBe(1);
    expect(requireApi(api).loadingEarlier).toBe(false);
  });

  it("窗口未推进到末尾时按 after 补齐（去重丢弃 / 截断 / 保留期的兜底）", async () => {
    // 首屏窗口只回放到 seq 11，而服务端确认日志已到 seq 13：中间的持久化事件没有
    // 随窗口返回（一页被截断 / 保留期丢弃 / 同一段增量已被实时路径去重应用），
    // 窗口自身渲染不全。
    mockFetch.mockResolvedValueOnce({
      events: [
        chatSseEvent("chunk", 10, { type: "text", content: "窗口第一段" }),
        chatSseEvent("chunk", 11, { type: "text", content: "窗口第二段" }),
      ],
      count: 2,
      latest_seq: 13,
      first_seq: 10,
      last_seq: 13,
      has_more: true,
    });
    // 补齐请求：从窗口落地后的游标（11）续拉，服务端返回缺口 12..13。
    mockFetch.mockResolvedValueOnce({
      events: [
        chatSseEvent("chunk", 12, { type: "text", content: "窗口第三段" }),
        chatSseEvent("chunk", 13, { type: "text", content: "窗口第四段" }),
      ],
      count: 2,
      latest_seq: 13,
    });

    let api: RecoveryApi | null = null;
    act(() => {
      root.render(
        <ApiHarness
          store={store}
          sessionId="session-1"
          onApi={(next) => {
            api = next;
          }}
        />,
      );
    });

    await vi.waitFor(() => {
      expect(store.getSnapshot().lastEventSeq).toBe(13);
    });
    // 补齐用 after 续拉（不重复拉窗口、不与「加载更早」的 before_seq 语义混淆）。
    expect(mockFetch).toHaveBeenLastCalledWith("session-1", {
      after: 11,
      limit: 500,
    });
    // 窗口与补齐页全部落地，没有滞留在乱序缓冲里的事件。
    expect(store.getSnapshot().pending).toEqual({});
    const text = visibleText(store);
    expect(text).toContain("窗口第一段");
    expect(text).toContain("窗口第四段");
    expect(requireApi(api).window.firstSeq).toBe(10);
    expect(requireApi(api).window.hasMore).toBe(true);
    // 两个请求：尾部窗口 + 一次补齐（补齐到窗口末尾即停，不空转）。
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("回放日志被裁剪后「加载更早」不再前移游标：直接收起入口，不发请求", async () => {
    mockFetch.mockResolvedValueOnce({
      events: [chatSseEvent("chunk", 10, { type: "text", content: "最近的消息" })],
      count: 1,
      latest_seq: 10,
      first_seq: 10,
      last_seq: 10,
      has_more: true,
    });

    let api: RecoveryApi | null = null;
    act(() => {
      root.render(
        <ApiHarness
          store={store}
          sessionId="session-1"
          onApi={(next) => {
            api = next;
          }}
        />,
      );
    });

    await vi.waitFor(() => {
      expect(api?.window.hasMore).toBe(true);
    });
    expect(mockFetch).toHaveBeenCalledTimes(1);

    // 大会话反复前插触顶（真实触发见 use-trajectory-snapshot.test.ts）：更早页
    // 前插后会整体重建投影，最早保留动作之前的行无法复现——必须停止提供入口，
    // 否则「加载更早」把已渲染的最早行吞掉且游标已前移、再也拉不回来。
    store.isReplayLogTruncated = () => true;

    await act(async () => {
      await requireApi(api).loadEarlier();
    });

    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(requireApi(api).window.hasMore).toBe(false);
    expect(requireApi(api).window.firstSeq).toBe(10);
    expect(visibleText(store)).toContain("最近的消息");
  });

  it("窗口已到日志开头时「加载更早」是空操作：不发请求、不改变窗口", async () => {
    mockFetch.mockResolvedValueOnce({
      events: [chatSseEvent("chunk", 1, { type: "text", content: "唯一一页" })],
      count: 1,
      latest_seq: 1,
      first_seq: 1,
      has_more: false,
    });

    let api: RecoveryApi | null = null;
    act(() => {
      root.render(
        <ApiHarness
          store={store}
          sessionId="session-1"
          onApi={(next) => {
            api = next;
          }}
        />,
      );
    });

    await vi.waitFor(() => {
      expect(api?.ready).toBe(true);
    });
    expect(requireApi(api).window.hasMore).toBe(false);

    await act(async () => {
      await requireApi(api).loadEarlier();
    });

    // 窗口已到开头：不发请求，窗口状态不变。
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(requireApi(api).window.firstSeq).toBe(1);
  });
});
