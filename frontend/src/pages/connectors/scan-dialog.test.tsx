// 扫码接入对话框单测：零填表（自动 ID + 只提交扫描基址）→ 创建成功后回调父页。
// 组件级渲染用 createRoot + act（与仓库既有组件测试同一范式，不引入新依赖）。

import { act } from "react";
import type { ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ConnectorSchema, ConnectorView } from "@/types/runtime";

vi.mock("@/api/runtime/connectors", () => ({
  createConnector: vi.fn(),
}));

import { createConnector } from "@/api/runtime/connectors";

import { ConnectorScanDialog } from "./scan-dialog";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const SCHEMA: ConnectorSchema = {
  version: "1",
  providers: [
    {
      provider: "wechat",
      label: "微信",
      modes: ["polling"],
      fields: [
        { key: "base_url", label: "iLink 基址", type: "url", placeholder: "https://…" },
        { key: "account_id", label: "账号 ID", type: "text" },
        {
          key: "mode",
          label: "接入模式",
          type: "enum",
          default: "polling",
          options: [{ value: "polling", label: "长轮询" }],
        },
      ],
      actions: [{ key: "start_login", label: "扫码登录", kind: "qr" }],
    },
    {
      provider: "telegram",
      label: "Telegram",
      fields: [{ key: "bot_token", label: "Bot Token", type: "secret" }],
    },
  ],
};

const CREATED: ConnectorView = {
  id: "wechat-abc",
  provider: "wechat",
  name: "微信",
  enabled: true,
  mode: "polling",
  settings: { base_url: "https://ilink.example" },
  secret_keys: [],
  policy: {},
};

function findButton(label: string) {
  return Array.from(document.querySelectorAll("button")).find((button) =>
    button.textContent?.includes(label),
  );
}

function findInput(ariaLabel: string) {
  return document.querySelector<HTMLInputElement>(`input[aria-label="${ariaLabel}"]`);
}

function setInputValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    "value",
  )?.set;
  act(() => {
    setter?.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

describe("ConnectorScanDialog", () => {
  const reactActEnvironment = globalThis as ReactActEnvironmentGlobal;
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    reactActEnvironment.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    vi.mocked(createConnector).mockReset();
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
  }

  function dialogProps(overrides: Partial<Parameters<typeof ConnectorScanDialog>[0]> = {}) {
    return {
      open: true,
      schema: SCHEMA,
      existingConnectors: [],
      onCreated: vi.fn(),
      onClose: vi.fn(),
      ...overrides,
    };
  }

  it("零填表：自动生成 ID、只提交扫描基址，成功后回调父页", async () => {
    vi.mocked(createConnector).mockResolvedValue(CREATED);
    const onCreated = vi.fn();
    await render(<ConnectorScanDialog {...dialogProps({ onCreated })} />);

    // 只渲染扫描基址 + 名称；账号 ID 等由扫码成功后回填，不出现在表单里。
    const base = findInput("iLink 基址");
    expect(base).not.toBeNull();
    expect(findInput("账号 ID")).toBeNull();

    setInputValue(base as HTMLInputElement, " https://ilink.example ");

    await act(async () => {
      findButton("开始扫码")?.click();
    });
    await act(async () => undefined);

    expect(createConnector).toHaveBeenCalledTimes(1);
    const [payload] = vi.mocked(createConnector).mock.calls[0];
    expect(payload.provider).toBe("wechat");
    expect(payload.id).toMatch(/^wechat-[a-z0-9]+$/);
    expect(payload.enabled).toBe(true);
    expect(payload.settings).toEqual({ base_url: "https://ilink.example" });
    expect(onCreated).toHaveBeenCalledWith(CREATED);
  });

  it("缺扫描基址：不创建并就地提示", async () => {
    const onCreated = vi.fn();
    await render(<ConnectorScanDialog {...dialogProps({ onCreated })} />);

    await act(async () => {
      findButton("开始扫码")?.click();
    });

    expect(createConnector).not.toHaveBeenCalled();
    expect(onCreated).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("请填写iLink 基址");
  });

  it("预填同通道已配置的基址，减少输入", async () => {
    await render(
      <ConnectorScanDialog
        {...dialogProps({
          existingConnectors: [
            { ...CREATED, settings: { base_url: "https://existing.example" } },
          ],
        })}
      />,
    );

    expect(findInput("iLink 基址")?.value).toBe("https://existing.example");
  });
});
