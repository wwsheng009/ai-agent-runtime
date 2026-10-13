// 扫码登录对话框状态机单测：start_login → 每 2s 轮询 login_status（scanned →
// confirmed）→ credential_ready 时 PATCH 回填 settings/secrets → 回调父页刷新；
// Escape 关闭并中止轮询，回填保存中拦截关闭。
// 用 mock fetch 驱动真实 API 客户端 + createRoot/act（不引入 @testing-library）。

import { act } from "react";
import type { ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ConnectorActionSpec, ConnectorView } from "@/types/runtime";

import { ConnectorActionDialog } from "./action-dialog";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const CONNECTOR: ConnectorView = {
  id: "wx-1",
  provider: "wechat",
  name: "微信机器人",
  enabled: true,
  mode: "polling",
  settings: {},
  policy: {},
};

const ACTION: ConnectorActionSpec = {
  key: "start_login",
  label: "扫码登录",
  kind: "qr",
};

const EXPIRES_AT = "2999-01-01T00:00:00Z";

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

function statusPayload(status: string, extra: Record<string, unknown> = {}) {
  return {
    login_id: "login-1",
    status,
    base_url: "https://ilink.example",
    qr_code: "qr-1",
    qr_code_content: "data:image/png;base64,QQ==",
    expires_at: EXPIRES_AT,
    ...extra,
  };
}

function findButton(label: string) {
  return Array.from(document.querySelectorAll("button")).find((button) =>
    button.textContent?.includes(label),
  );
}

describe("ConnectorActionDialog", () => {
  const reactActEnvironment = globalThis as ReactActEnvironmentGlobal;
  const originalFetch = globalThis.fetch;
  let container: HTMLDivElement;
  let root: Root | null;
  let calls: FetchCall[];
  /** 依次消费的 login_status 响应；耗尽后回 wait。 */
  let statusResponses: Array<Record<string, unknown>>;
  let startResponse: Record<string, unknown>;
  /** 非 null 时 PATCH 先等待该 Promise（模拟保存中）。 */
  let patchGate: Promise<void> | null;

  beforeEach(() => {
    reactActEnvironment.IS_REACT_ACT_ENVIRONMENT = true;
    vi.useFakeTimers();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    calls = [];
    statusResponses = [];
    startResponse = statusPayload("wait");
    patchGate = null;

    globalThis.fetch = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input), "http://localhost");
        const method = (init?.method ?? "GET").toUpperCase();
        const body =
          typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
        calls.push({ path: url.pathname, method, body });

        if (url.pathname.endsWith("/actions/start_login")) {
          return jsonResponse(startResponse);
        }
        if (url.pathname.endsWith("/actions/login_status")) {
          const next = statusResponses.shift();
          return jsonResponse(next ?? statusPayload("wait"));
        }
        if (method === "PATCH") {
          if (patchGate) {
            await patchGate;
          }
          return jsonResponse({ ...CONNECTOR });
        }
        return jsonResponse({ error: `unexpected ${method} ${url.pathname}` }, 500);
      },
    ) as typeof fetch;
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container.remove();
    globalThis.fetch = originalFetch;
    vi.useRealTimers();
    reactActEnvironment.IS_REACT_ACT_ENVIRONMENT = false;
  });

  async function render(element: ReactElement) {
    await act(async () => {
      root?.render(element);
    });
    await act(async () => undefined);
  }

  async function advance(ms: number) {
    await act(async () => {
      vi.advanceTimersByTime(ms);
    });
    await act(async () => undefined);
  }

  function dispatchEscape() {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
  }

  function statusCalls() {
    return calls.filter((call) => call.path.endsWith("/actions/login_status"));
  }

  it("start_login → 轮询 scanned/confirmed → PATCH 回填并回调刷新", async () => {
    statusResponses.push(statusPayload("scanned"), statusPayload("confirmed", {
      credential_ready: true,
      account_id: "acc-1",
      user_id: "user-1",
      settings: {
        base_url: "https://ilink.example",
        account_id: "acc-1",
        owner_user_id: "user-1",
      },
      secrets: { bot_token: "tok-1" },
    }));
    const onClose = vi.fn();
    const onRefilled = vi.fn();

    await render(
      <ConnectorActionDialog
        open
        connector={CONNECTOR}
        action={ACTION}
        onRefilled={onRefilled}
        onClose={onClose}
      />,
    );

    // start_login 已发出；qr_code_content 是 data:image → <img> 展示。
    expect(calls[0]).toMatchObject({
      path: "/api/connectors/wx-1/actions/start_login",
      method: "POST",
    });
    expect(calls[0].body).toBeUndefined();
    expect(document.querySelector("img")?.getAttribute("src")).toBe(
      "data:image/png;base64,QQ==",
    );

    // 第一次轮询：scanned。
    await advance(2000);
    expect(statusCalls()).toHaveLength(1);
    expect(statusCalls()[0].body).toEqual({ login_id: "login-1" });
    expect(document.body.textContent).toContain("已扫码，请在手机上确认");

    // 第二次轮询：confirmed → 自动 PATCH 回填 settings/secrets。
    await advance(2000);
    const patch = calls.find((call) => call.method === "PATCH");
    expect(patch?.path).toBe("/api/connectors/wx-1");
    expect(patch?.body).toEqual({
      settings: {
        base_url: "https://ilink.example",
        account_id: "acc-1",
        owner_user_id: "user-1",
      },
      secrets: { bot_token: "tok-1" },
    });
    expect(onRefilled).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toContain("凭据已回填");

    // 终态（confirmed）后不再轮询。
    await advance(4000);
    expect(statusCalls()).toHaveLength(2);

    act(() => {
      dispatchEscape();
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("Escape 关闭后中止轮询（不再发 login_status）", async () => {
    statusResponses.push(statusPayload("scanned"));
    const onClose = vi.fn();

    await render(
      <ConnectorActionDialog
        open
        connector={CONNECTOR}
        action={ACTION}
        onClose={onClose}
      />,
    );
    await advance(2000);
    expect(statusCalls()).toHaveLength(1);

    act(() => {
      dispatchEscape();
    });
    expect(onClose).toHaveBeenCalledTimes(1);

    // 父页关闭弹窗（open=false）→ 挂起的轮询 effect 清理。
    await render(
      <ConnectorActionDialog
        open={false}
        connector={CONNECTOR}
        action={ACTION}
        onClose={onClose}
      />,
    );
    await advance(4000);
    expect(statusCalls()).toHaveLength(1);
  });

  it("回填保存中 Escape 拦截；保存完成后恢复关闭", async () => {
    statusResponses.push(
      statusPayload("confirmed", {
        credential_ready: true,
        settings: { base_url: "https://ilink.example" },
        secrets: { bot_token: "tok-1" },
      }),
    );
    let release: () => void = () => undefined;
    patchGate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const onClose = vi.fn();

    await render(
      <ConnectorActionDialog
        open
        connector={CONNECTOR}
        action={ACTION}
        onClose={onClose}
      />,
    );
    await advance(2000);

    expect(document.body.textContent).toContain("正在回填");
    act(() => {
      dispatchEscape();
    });
    expect(onClose).not.toHaveBeenCalled();

    await act(async () => {
      release();
    });
    await act(async () => undefined);
    expect(document.body.textContent).toContain("凭据已回填");

    act(() => {
      dispatchEscape();
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("qr_code_content 非图片时展示文本并提供「复制链接」", async () => {
    startResponse = statusPayload("wait", {
      qr_code_content: "https://ilink.example/login/abc123",
    });
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(window.navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });

    await render(
      <ConnectorActionDialog
        open
        connector={CONNECTOR}
        action={ACTION}
        onClose={vi.fn()}
      />,
    );

    expect(document.querySelector("img")).toBeNull();
    expect(document.body.textContent).toContain("https://ilink.example/login/abc123");

    await act(async () => {
      findButton("复制链接")?.click();
    });
    expect(writeText).toHaveBeenCalledWith("https://ilink.example/login/abc123");
    expect(document.body.textContent).toContain("已复制");
  });
});
