// 侧栏「机器人」面板单测（docs/bots/09 §5.3 N4）：
// 列表渲染（连接器 + 绑定摘要 + 归属）/ 绑定动作 / 解绑动作 / 未选会话禁用。
// mock fetch 驱动真实 API 客户端 + createRoot/act（仓库既有范式）。

import { act } from "react";
import type { ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ConnectorView } from "@/types/runtime";

import { WorkspaceBotPanel } from "./bot-panel";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const CONNECTORS: ConnectorView[] = [
  {
    id: "wx-1",
    provider: "wechat",
    name: "微信机器人",
    enabled: true,
    mode: "polling",
    settings: {},
    secret_keys: ["bot_token"],
    policy: {},
    active_owner: "runtime-server",
  },
  {
    id: "wx-2",
    provider: "wechat",
    name: "备用微信",
    enabled: true,
    mode: "polling",
    settings: {},
    secret_keys: ["bot_token"],
    policy: {},
  },
];

interface FetchCall {
  path: string;
  method: string;
  body: unknown;
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function findButton(label: string) {
  return Array.from(document.querySelectorAll("button")).find((button) =>
    button.textContent?.includes(label),
  );
}

describe("WorkspaceBotPanel", () => {
  const reactActEnvironment = globalThis as ReactActEnvironmentGlobal;
  const originalFetch = globalThis.fetch;
  let container: HTMLDivElement;
  let root: Root | null;
  let calls: FetchCall[];
  let bindingsPayload: unknown;

  beforeEach(() => {
    reactActEnvironment.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    calls = [];
    bindingsPayload = { items: [] };
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost");
      calls.push({
        path: url.pathname + url.search,
        method: (init?.method ?? "GET").toUpperCase(),
        body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
      });
      if (url.pathname === "/api/connectors") {
        return jsonResponse({ items: CONNECTORS });
      }
      if (url.pathname.endsWith("/bindings")) {
        return jsonResponse(bindingsPayload);
      }
      return jsonResponse(BINDING_UPDATED);
    }) as typeof fetch;
  });

  const BINDING_UPDATED = {
    connector_id: "wx-1",
    chat: "chat-1",
    session_id: "sess-current",
    updated_at: "2026-10-14T02:00:00Z",
    connector: { id: "wx-1", name: "微信机器人", provider: "wechat", enabled: true },
  };

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container.remove();
    globalThis.fetch = originalFetch;
    reactActEnvironment.IS_REACT_ACT_ENVIRONMENT = false;
  });

  async function render(element: ReactElement) {
    await act(async () => {
      root?.render(element);
    });
    await act(async () => undefined);
  }

  it("渲染连接器列表、归属与绑定摘要（含 aicli 侧既有绑定，D1 共享可见）", async () => {
    bindingsPayload = {
      items: [
        {
          connector_id: "wx-1",
          chat: "o9cq-owner@im.wechat",
          session_id: "sess-other",
          node_id: "node-26752-20261015T145521Z",
          updated_at: "2026-10-14T01:00:00Z",
          connector: { id: "wx-1", name: "微信机器人", provider: "wechat", enabled: true },
        },
      ],
    };
    await render(<WorkspaceBotPanel selectedSessionId="sess-current" />);

    expect(document.body.textContent).toContain("微信机器人");
    expect(document.body.textContent).toContain("备用微信");
    expect(document.body.textContent).toContain("运行于 runtime-server");
    expect(document.body.textContent).toContain("未运行");
    // 绑定行 + chat
    expect(document.body.textContent).toContain("o9cq-owner@im.wechat");
    expect(document.body.textContent).toContain("切换到当前会话");
    // 所在会话与节点（docs/bots/09 §5.3）
    expect(document.body.textContent).toContain("会话 sess-other");
    expect(document.body.textContent).toContain("节点 node-26752-20261015T145521Z");
  });

  it("切换到当前会话：PUT 带 expected 乐观锁（按 updated_at）", async () => {
    bindingsPayload = {
      items: [
        {
          connector_id: "wx-1",
          chat: "chat-1",
          session_id: "sess-old",
          updated_at: "2026-10-14T01:00:00Z",
          connector: { id: "wx-1", name: "微信机器人", provider: "wechat", enabled: true },
        },
      ],
    };
    await render(<WorkspaceBotPanel selectedSessionId="sess-current" />);

    await act(async () => {
      findButton("切换到当前会话")?.click();
    });
    await act(async () => undefined);

    const put = calls.find((call) => call.method === "PUT");
    expect(put?.path).toBe("/api/connectors/wx-1/bindings/chat-1");
    expect(put?.body).toEqual({
      session_id: "sess-current",
      expected_updated_at: "2026-10-14T01:00:00Z",
    });
  });

  it("解绑：DELETE 到对应路径", async () => {
    bindingsPayload = {
      items: [
        {
          connector_id: "wx-1",
          chat: "chat-1",
          session_id: "sess-1",
          updated_at: "2026-10-14T01:00:00Z",
          connector: { id: "wx-1", name: "微信机器人", provider: "wechat", enabled: true },
        },
      ],
    };
    await render(<WorkspaceBotPanel selectedSessionId="sess-current" />);

    await act(async () => {
      const unbind = Array.from(document.querySelectorAll("button")).find((button) =>
        button.getAttribute("aria-label")?.includes("解绑"),
      );
      unbind?.click();
    });
    await act(async () => undefined);

    const del = calls.find((call) => call.method === "DELETE");
    expect(del?.path).toBe("/api/connectors/wx-1/bindings/chat-1");
  });

  it("绑定新会话：展开输入框、Enter/点击提交", async () => {
    await render(<WorkspaceBotPanel selectedSessionId="sess-current" />);

    await act(async () => {
      findButton("绑定新会话")?.click();
    });
    const input = document.querySelector<HTMLInputElement>(
      'input[placeholder="IM 会话 id（chat）"]',
    );
    expect(input).not.toBeNull();

    const setter = Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )?.set;
    await act(async () => {
      setter?.call(input as HTMLInputElement, "new-chat-9");
      (input as HTMLInputElement).dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      findButton("绑定")?.click();
    });

    const put = calls.find((call) => call.method === "PUT");
    expect(put?.path).toBe("/api/connectors/wx-1/bindings/new-chat-9");
    expect(put?.body).toEqual({ session_id: "sess-current" });
  });
});
