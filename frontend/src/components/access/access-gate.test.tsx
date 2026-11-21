// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { getRuntimeAccessStatusMock, loginRuntimeAccessMock } = vi.hoisted(() => ({
  getRuntimeAccessStatusMock: vi.fn(),
  loginRuntimeAccessMock: vi.fn(),
}));

vi.mock("@/api/runtime/access", () => ({
  getRuntimeAccessStatus: getRuntimeAccessStatusMock,
  loginRuntimeAccess: loginRuntimeAccessMock,
}));

import { RuntimeApiError } from "@/api/runtime/shared";

import { AccessGate } from "./access-gate";

describe("AccessGate", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    getRuntimeAccessStatusMock.mockReset();
    loginRuntimeAccessMock.mockReset();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT;
  });

  async function render() {
    await act(async () => {
      root.render(
        <AccessGate>
          <div>workspace-ready</div>
        </AccessGate>,
      );
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it("本地访问直接渲染工作区", async () => {
    getRuntimeAccessStatusMock.mockResolvedValue({
      required: false,
      authenticated: true,
      local: true,
      configured: false,
    });
    await render();
    expect(container.textContent).toContain("workspace-ready");
  });

  it("非本地未登录时显示 Token 输入页，成功后进入工作区", async () => {
    getRuntimeAccessStatusMock.mockResolvedValue({
      required: true,
      authenticated: false,
      local: false,
      configured: true,
    });
    loginRuntimeAccessMock.mockResolvedValue({ authenticated: true });
    await render();

    expect(container.textContent).toContain("远程访问验证");
    expect(container.querySelector("#runtime-access-token")).not.toBeNull();
    expect(container.textContent).not.toContain("workspace-ready");

    const input = container.querySelector("#runtime-access-token") as HTMLInputElement;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
    await act(async () => {
      setter?.call(input, "secret");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      container.querySelector("form")?.dispatchEvent(
        new Event("submit", { bubbles: true, cancelable: true }),
      );
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(loginRuntimeAccessMock).toHaveBeenCalledWith("secret");
    expect(container.textContent).toContain("workspace-ready");
  });

  it("错误 Token 留在登录页并显示提示", async () => {
    getRuntimeAccessStatusMock.mockResolvedValue({
      required: true,
      authenticated: false,
      local: false,
      configured: true,
    });
    loginRuntimeAccessMock.mockRejectedValue(new RuntimeApiError(401, {
      code: "API_UNAUTHORIZED",
      error: "[API_UNAUTHORIZED] invalid access token",
    }));
    await render();

    const input = container.querySelector("#runtime-access-token") as HTMLInputElement;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
    await act(async () => {
      setter?.call(input, "wrong");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      container.querySelector("form")?.dispatchEvent(
        new Event("submit", { bubbles: true, cancelable: true }),
      );
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.textContent).toContain("Token 无效");
    expect(container.textContent).not.toContain("workspace-ready");
  });

  it("服务端未配置 token 时明确拒绝非本地访问", async () => {
    getRuntimeAccessStatusMock.mockResolvedValue({
      required: true,
      authenticated: false,
      local: false,
      configured: false,
    });
    await render();
    expect(container.textContent).toContain("服务端未配置访问 Token");
    expect(container.textContent).not.toContain("workspace-ready");
  });
});
