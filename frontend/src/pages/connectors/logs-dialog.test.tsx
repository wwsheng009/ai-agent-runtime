// 连接器日志抽屉单测：entries 渲染（时间/级别色标/方向/摘要）、stats 头部、
// 空态/失败态/无 runtime 快照分支、Escape 关闭、手动刷新与 5s 自动刷新。
// 组件级渲染用 createRoot + act（仓库既有范式，不引入新依赖）。

import { act } from "react";
import type { ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ConnectorLogEntry, ConnectorStats, ConnectorView } from "@/types/runtime";

vi.mock("@/api/runtime/connectors", () => ({
  CONNECTOR_LOGS_DEFAULT_LIMIT: 50,
  readConnectorLogs: vi.fn(),
}));

import { readConnectorLogs } from "@/api/runtime/connectors";

import { ConnectorLogsDialog } from "./logs-dialog";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const STATS: ConnectorStats = {
  inbound: 2,
  outbound: 1,
  errors: 1,
  dedup_skipped: 0,
  queue_full: 0,
  last_error: "投递失败：timeout",
  last_error_at: "2026-10-13T10:00:02Z",
  last_activity_at: "2026-10-13T10:00:02Z",
};

const ENTRY: ConnectorLogEntry = {
  time: "2026-10-13T10:00:02Z",
  level: "error",
  direction: "in",
  connector_id: "tg-main",
  chat: "42",
  summary: "投递失败：timeout",
};

const CONNECTOR_BASE: ConnectorView = {
  id: "tg-main",
  provider: "telegram",
  name: "Telegram @my_bot",
  enabled: true,
  mode: "polling",
  settings: {},
  policy: {},
};

const CONNECTOR: ConnectorView = {
  ...CONNECTOR_BASE,
  runtime: { stats: STATS },
};

function findButton(label: string) {
  return Array.from(document.querySelectorAll("button")).find((button) =>
    button.textContent?.includes(label),
  );
}

describe("ConnectorLogsDialog", () => {
  const reactActEnvironment = globalThis as ReactActEnvironmentGlobal;
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    reactActEnvironment.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    vi.mocked(readConnectorLogs).mockReset();
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container.remove();
    reactActEnvironment.IS_REACT_ACT_ENVIRONMENT = false;
  });

  async function render(element: ReactElement) {
    await act(async () => {
      root?.render(element);
    });
    await act(async () => undefined);
  }

  it("打开时加载并渲染 entries 与 stats 头部", async () => {
    vi.mocked(readConnectorLogs).mockResolvedValue({
      entries: [ENTRY],
      stats: STATS,
    });

    await render(
      <ConnectorLogsDialog open connector={CONNECTOR} onClose={vi.fn()} />,
    );

    expect(vi.mocked(readConnectorLogs)).toHaveBeenCalledWith("tg-main", 50, {
      adminToken: "",
    });
    expect(document.body.textContent).toContain("2026-10-13T10:00:02Z");
    expect(document.body.textContent).toContain("错误");
    expect(document.body.textContent).toContain("入站");
    expect(document.body.textContent).toContain("投递失败：timeout");
    expect(document.body.textContent).toContain("入 2");
    expect(document.body.textContent).toContain("错 1");
    expect(document.body.textContent).toContain("最近活动");
    // 级别色标：error 走语义 danger token。
    const levelChip = Array.from(document.querySelectorAll("span")).find(
      (node) => node.textContent === "错误",
    );
    expect(levelChip?.className).toContain("text-analytics-danger");
  });

  it("空态：entries 为空显示空态文案", async () => {
    vi.mocked(readConnectorLogs).mockResolvedValue({ entries: [], stats: STATS });

    await render(
      <ConnectorLogsDialog open connector={CONNECTOR} onClose={vi.fn()} />,
    );

    expect(document.body.textContent).toContain("暂无运行日志");
  });

  it("失败态：显示 loadFailed 与错误信息（不误报空态）", async () => {
    vi.mocked(readConnectorLogs).mockRejectedValue(new Error("logs down"));

    await render(
      <ConnectorLogsDialog open connector={CONNECTOR} onClose={vi.fn()} />,
    );

    expect(document.body.textContent).toContain("加载日志失败");
    expect(document.body.textContent).toContain("logs down");
    expect(document.body.textContent).not.toContain("暂无运行日志");
  });

  it("无 runtime 快照：头部降级提示且不显示计数；entries 仍渲染", async () => {
    vi.mocked(readConnectorLogs).mockResolvedValue({
      entries: [ENTRY],
      stats: STATS,
    });

    await render(
      <ConnectorLogsDialog open connector={CONNECTOR_BASE} onClose={vi.fn()} />,
    );

    expect(document.body.textContent).toContain("暂无运行快照");
    expect(document.body.textContent).not.toContain("入 2");
    expect(document.body.textContent).toContain("投递失败：timeout");
  });

  it("Escape 关闭；手动刷新重新拉取", async () => {
    vi.mocked(readConnectorLogs).mockResolvedValue({ entries: [], stats: STATS });
    const onClose = vi.fn();

    await render(
      <ConnectorLogsDialog open connector={CONNECTOR} onClose={onClose} />,
    );
    expect(vi.mocked(readConnectorLogs)).toHaveBeenCalledTimes(1);

    await act(async () => {
      findButton("刷新")?.click();
    });
    expect(vi.mocked(readConnectorLogs)).toHaveBeenCalledTimes(2);

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("自动刷新：勾选后每 5s 重新拉取，关闭即停", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(readConnectorLogs).mockResolvedValue({
        entries: [],
        stats: STATS,
      });
      const onClose = vi.fn();

      await render(
        <ConnectorLogsDialog open connector={CONNECTOR} onClose={onClose} />,
      );
      expect(vi.mocked(readConnectorLogs)).toHaveBeenCalledTimes(1);

      const toggle = document.querySelector<HTMLInputElement>(
        'input[aria-label="自动刷新（5 秒）"]',
      );
      expect(toggle).not.toBeNull();
      await act(async () => {
        toggle?.click();
      });

      await act(async () => {
        vi.advanceTimersByTime(5000);
      });
      expect(vi.mocked(readConnectorLogs)).toHaveBeenCalledTimes(2);

      // open=false 后定时器清除：继续推进时间不再触发拉取。
      await act(async () => {
        root?.render(
          <ConnectorLogsDialog open={false} connector={CONNECTOR} onClose={onClose} />,
        );
      });
      await act(async () => {
        vi.advanceTimersByTime(10000);
      });
      expect(vi.mocked(readConnectorLogs)).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });
});
