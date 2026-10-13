// 连接器页面交互单测：行内操作 busy 期间禁用该条编辑/测试按钮，
// 以及 schema / 列表加载失败的差异化反馈（schemaFailed vs loadFailed）。
// 另覆盖运行归属展示、抢占按钮与派生自 provider schema 的通道动作按钮。
// 组件级渲染用 createRoot + act + MemoryRouter（仓库既有范式，不引入新依赖）。

import { act } from "react";
import type { ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ConnectorSchema, ConnectorView } from "@/types/runtime";

vi.mock("@/api/runtime/connectors", () => ({
  createConnector: vi.fn(),
  deleteConnector: vi.fn(),
  getConnectorSchema: vi.fn(),
  listConnectors: vi.fn(),
  readConnectorFieldErrors: vi.fn(() => []),
  reloadConnector: vi.fn(),
  runConnectorAction: vi.fn(),
  takeoverConnector: vi.fn(),
  updateConnector: vi.fn(),
}));

import {
  getConnectorSchema,
  listConnectors,
  runConnectorAction,
  takeoverConnector,
  updateConnector,
} from "@/api/runtime/connectors";

import { ConnectorsPage } from "./connectors-page";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const SCHEMA: ConnectorSchema = {
  version: "1",
  providers: [
    {
      provider: "telegram",
      label: "Telegram",
      modes: ["polling"],
      fields: [{ key: "bot_token", label: "Bot Token", type: "secret", required: true }],
    },
  ],
};

const CONNECTOR: ConnectorView = {
  id: "tg-main",
  provider: "telegram",
  name: "Telegram @my_bot",
  enabled: true,
  mode: "polling",
  settings: {},
  secret_keys: ["bot_token"],
  policy: {},
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

function findButton(label: string) {
  return Array.from(document.querySelectorAll("button")).find((button) =>
    button.textContent?.includes(label),
  );
}

describe("ConnectorsPage", () => {
  const reactActEnvironment = globalThis as ReactActEnvironmentGlobal;
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    reactActEnvironment.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    vi.mocked(getConnectorSchema).mockReset();
    vi.mocked(listConnectors).mockReset();
    vi.mocked(runConnectorAction).mockReset();
    vi.mocked(takeoverConnector).mockReset();
    vi.mocked(updateConnector).mockReset();
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
      root?.render(<MemoryRouter>{element}</MemoryRouter>);
    });
    await act(async () => undefined);
  }

  it("行内操作期间禁用该条编辑/测试按钮，完成后恢复", async () => {
    vi.mocked(getConnectorSchema).mockResolvedValue(SCHEMA);
    vi.mocked(listConnectors).mockResolvedValue([CONNECTOR]);
    const pending = deferred<ConnectorView>();
    vi.mocked(updateConnector).mockReturnValueOnce(pending.promise);

    await render(<ConnectorsPage />);

    const toggle = findButton("停用");
    expect(toggle).not.toBeNull();
    act(() => {
      toggle?.click();
    });

    expect(findButton("停用")?.disabled).toBe(true);
    expect(findButton("编辑")?.disabled).toBe(true);
    expect(findButton("测试消息")?.disabled).toBe(true);
    expect(findButton("重新加载")?.disabled).toBe(true);
    expect(findButton("删除")?.disabled).toBe(true);

    await act(async () => {
      pending.resolve({ ...CONNECTOR, enabled: false });
    });

    expect(findButton("编辑")?.disabled).toBe(false);
    expect(findButton("测试消息")?.disabled).toBe(false);
  });

  it("schema 加载失败：显示 schemaFailed 文案且「编辑」不可点", async () => {
    vi.mocked(getConnectorSchema).mockRejectedValue(new Error("boom"));
    vi.mocked(listConnectors).mockResolvedValue([CONNECTOR]);

    await render(<ConnectorsPage />);

    expect(document.body.textContent).toContain("加载配置模板失败");
    expect(document.body.textContent).toContain("boom");
    expect(findButton("编辑")?.disabled).toBe(true);
    expect(findButton("新增连接器")?.disabled).toBe(true);
  });

  it("列表加载失败：显示 loadFailed 文案（不误用 schemaFailed）", async () => {
    vi.mocked(getConnectorSchema).mockResolvedValue(SCHEMA);
    vi.mocked(listConnectors).mockRejectedValue(new Error("list down"));

    await render(<ConnectorsPage />);

    expect(document.body.textContent).toContain("加载连接器失败");
    expect(document.body.textContent).toContain("list down");
    expect(document.body.textContent).not.toContain("加载配置模板失败");
  });

  it("运行归属展示：本宿主高亮、未运行时显示「未运行」且不出抢占按钮", async () => {
    vi.mocked(getConnectorSchema).mockResolvedValue(SCHEMA);
    vi.mocked(listConnectors).mockResolvedValue([
      { ...CONNECTOR, active_owner: "host-a", owned_by_current_host: true },
      { ...CONNECTOR, id: "spare", name: "备用", active_owner: "", owned_by_current_host: false },
    ]);

    await render(<ConnectorsPage />);

    expect(document.body.textContent).toContain("运行于 host-a");
    expect(document.body.textContent).toContain("未运行");
    expect(findButton("抢占")).toBeUndefined();
  });

  it("非本宿主持有且启用：二次确认后抢占成功、反馈并刷新", async () => {
    vi.mocked(getConnectorSchema).mockResolvedValue(SCHEMA);
    vi.mocked(listConnectors).mockResolvedValue([
      { ...CONNECTOR, active_owner: "host-b", owned_by_current_host: false },
    ]);
    vi.mocked(takeoverConnector).mockResolvedValue({
      ...CONNECTOR,
      active_owner: "host-a",
      owned_by_current_host: true,
    });
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);

    await render(<ConnectorsPage />);

    const takeover = findButton("抢占");
    expect(takeover).not.toBeNull();
    await act(async () => {
      takeover?.click();
    });

    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(vi.mocked(takeoverConnector)).toHaveBeenCalledWith("tg-main", {
      adminToken: "",
    });
    expect(document.body.textContent).toContain("已抢占");
    // 成功后重新拉取列表（首次渲染 + 抢占后刷新）。
    expect(vi.mocked(listConnectors)).toHaveBeenCalledTimes(2);
    confirmSpy.mockRestore();
  });

  it("扫描类动作打开扫码弹窗；button 类动作直接执行并展示返回摘要", async () => {
    const wechatSchema: ConnectorSchema = {
      version: "1",
      providers: [
        {
          provider: "wechat",
          label: "微信",
          fields: [],
          actions: [
            { key: "start_login", label: "扫码登录", kind: "qr" },
            { key: "login_status", label: "登录状态", kind: "button" },
          ],
        },
      ],
    };
    const wechatConnector: ConnectorView = {
      ...CONNECTOR,
      id: "wx-1",
      provider: "wechat",
      name: "微信机器人",
    };
    vi.mocked(getConnectorSchema).mockResolvedValue(wechatSchema);
    vi.mocked(listConnectors).mockResolvedValue([wechatConnector]);
    vi.mocked(runConnectorAction).mockResolvedValue({
      raw: {},
      login_id: "login-1",
      status: "wait",
      qr_code_content: "data:image/png;base64,QQ==",
      expires_at: "2999-01-01T00:00:00Z",
    });

    await render(<ConnectorsPage />);

    await act(async () => {
      findButton("扫码登录")?.click();
    });
    await act(async () => undefined);

    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog).not.toBeNull();
    expect(dialog?.getAttribute("aria-label")).toBe("扫码登录 · 微信机器人");
    expect(vi.mocked(runConnectorAction)).toHaveBeenCalledWith(
      "wx-1",
      "start_login",
      undefined,
      { adminToken: "" },
    );

    // 关闭弹窗后执行 button 类动作：结果展示为单行摘要。
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    });
    await act(async () => undefined);

    vi.mocked(runConnectorAction).mockResolvedValue({
      raw: {},
      login_id: "login-1",
      status: "confirmed",
    });
    await act(async () => {
      findButton("登录状态")?.click();
    });

    expect(vi.mocked(runConnectorAction)).toHaveBeenLastCalledWith(
      "wx-1",
      "login_status",
      undefined,
      { adminToken: "" },
    );
    expect(document.body.textContent).toContain("status=confirmed");
  });
});
