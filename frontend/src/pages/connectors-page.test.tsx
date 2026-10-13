// 连接器页面交互单测：行内操作 busy 期间禁用该条编辑/测试按钮，
// 以及 schema / 列表加载失败的差异化反馈（schemaFailed vs loadFailed）。
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
  updateConnector: vi.fn(),
}));

import {
  getConnectorSchema,
  listConnectors,
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
});
