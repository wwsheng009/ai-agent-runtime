// @vitest-environment jsdom

// 「会话详情」面内「路由」区块的单测：只断言用户可见口径——
// 档位行来自后端投影、写入层切换与 target_path 回显、按字段错误回显、
// config 层二次确认（未确认不发请求）、不可写层置灰。
//
// 网络层整模块 mock（`@/api/runtime/session-routing`），不依赖真实后端。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RuntimeApiError } from "@/api/runtime/shared";
import type { SessionRoutingResponse } from "@/types/runtime";

import { SessionDetailRoutingSection } from "./session-detail-routing-section";

const { getSessionRoutingMock, updateSessionRoutingMock } = vi.hoisted(() => ({
  getSessionRoutingMock: vi.fn(),
  updateSessionRoutingMock: vi.fn(),
}));

vi.mock("@/api/runtime/session-routing", () => ({
  getSessionRouting: getSessionRoutingMock,
  updateSessionRouting: updateSessionRoutingMock,
}));

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function routingFixture(
  overrides: Partial<SessionRoutingResponse> = {},
): SessionRoutingResponse {
  return {
    sessionId: "session-1",
    scope: "main",
    targetLayer: "",
    targetPath: "",
    actorInvalidated: false,
    updated: false,
    routing: {
      schemaVersion: 1,
      enabled: true,
      level: "hard",
      provider: "opencode.ai",
      model: "deepseek-v4.1",
      reasoning: "high",
      source: "session",
      disabled: false,
      warnings: ["高价档位保护已开启"],
      revision: "2026-09-22T00:00:00Z by web",
      effectiveFrom: "next_turn",
    },
    subAgent: {
      schemaVersion: 1,
      enabled: false,
      level: "",
      provider: "",
      model: "",
      reasoning: "",
      source: "default",
      disabled: true,
      warnings: [],
      revision: "",
      effectiveFrom: "next_turn",
    },
    panel: {
      scope: "main",
      childSession: false,
      sessionOverride: true,
      workspaceOverride: false,
      configOverride: true,
      workspacePath: "E:/ws",
      workspacePrefsPath: "E:/ws/.aicli/chat-prefs.yaml",
      configPath: "C:/Users/vince/.aicli/config.yaml",
      configLayer: "user",
      writableLayers: ["session", "workspace", "config"],
      levels: [
        {
          level: "hard",
          enabled: true,
          provider: "opencode.ai",
          model: "deepseek-v4.1",
          reasoning: "high",
          source: "session",
          expensive: false,
        },
        {
          level: "easy",
          enabled: false,
          provider: "local",
          model: "small-model",
          reasoning: "low",
          source: "config",
          expensive: true,
        },
      ],
      subAgent: null,
    },
    warnings: [],
    ...overrides,
  };
}

describe("SessionDetailRoutingSection", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    getSessionRoutingMock.mockReset();
    updateSessionRoutingMock.mockReset();
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function query<T extends Element>(testId: string): T | null {
    return document.body.querySelector<T>(`[data-testid="${testId}"]`);
  }

  function textOf(testId: string): string {
    return query(testId)?.textContent?.trim() ?? "";
  }

  function click(testId: string) {
    const node = query<HTMLElement>(testId);
    if (!node) {
      throw new Error(`missing node: ${testId}`);
    }
    act(() => {
      node.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
  }

  function setInput(testId: string, value: string) {
    const input = query<HTMLInputElement>(testId);
    if (!input) {
      throw new Error(`missing input: ${testId}`);
    }
    // 绕过 React 的 value tracker，模拟真实键入。
    const setter = Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )?.set;
    act(() => {
      setter?.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }

  async function flush() {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }

  async function renderSection(sessionId = "session-1") {
    await act(async () => {
      root?.render(<SessionDetailRoutingSection sessionId={sessionId} />);
    });
    await flush();
  }

  it("档位表格与摘要直接展示后端投影，且区块内嵌于侧栏（非全屏）", async () => {
    getSessionRoutingMock.mockResolvedValue(routingFixture());

    await renderSection();

    expect(getSessionRoutingMock).toHaveBeenCalledWith(
      "session-1",
      expect.objectContaining({ adminToken: "" }),
    );
    // 非全屏：没有对话框/遮罩，只有面内的一个 section。
    expect(query("session-detail-routing")?.tagName).toBe("SECTION");
    expect(document.body.querySelectorAll('[role="dialog"]')).toHaveLength(0);

    // 档位行：level × provider/model/effort/source + 启用/关闭态。
    expect(textOf("routing-level-state-hard")).toBe("启用");
    expect(textOf("routing-level-state-easy")).toBe("关闭");
    expect(
      query<HTMLInputElement>("routing-field-hard-provider")?.value,
    ).toBe("opencode.ai");
    expect(query<HTMLInputElement>("routing-field-hard-model")?.value).toBe(
      "deepseek-v4.1",
    );
    expect(
      query<HTMLInputElement>("routing-field-hard-reasoning_effort")?.value,
    ).toBe("high");
    expect(textOf("routing-level-source-hard")).toBe("会话");
    expect(textOf("routing-level-source-easy")).toBe("配置");
    expect(textOf("routing-level-expensive-easy")).toBe("高价档位");

    // 摘要与 warnings 用投影原文，不改写。
    expect(textOf("routing-summary-model")).toBe("deepseek-v4.1");
    expect(textOf("routing-summary-revision")).toBe("2026-09-22T00:00:00Z by web");
    expect(textOf("routing-warnings")).toContain("高价档位保护已开启");

    // 生效值来自其它层时给出继承提示（不改写「应该是什么」）。
    expect(textOf("routing-inherited-easy")).toContain("配置");
  });

  it("切换写入层回显 target_path，保存时带上该层与字段级补丁", async () => {
    getSessionRoutingMock.mockResolvedValue(routingFixture());
    updateSessionRoutingMock.mockResolvedValue(
      routingFixture({
        targetLayer: "workspace",
        targetPath: "E:/ws/.aicli/chat-prefs.yaml",
        updated: true,
      }),
    );

    await renderSection();

    // 默认 session 层没有文件路径，用文案说明。
    expect(textOf("routing-target-path")).toBe("会话记录（随会话持久化）");

    click("routing-layer-workspace");
    expect(textOf("routing-target-path")).toBe("E:/ws/.aicli/chat-prefs.yaml");

    setInput("routing-field-hard-model", "next-model");
    click("routing-save");
    await flush();

    expect(updateSessionRoutingMock).toHaveBeenCalledTimes(1);
    expect(updateSessionRoutingMock.mock.calls[0][0]).toBe("session-1");
    expect(updateSessionRoutingMock.mock.calls[0][1]).toEqual({
      target_layer: "workspace",
      main_agent: { profiles: { hard: { model: "next-model" } } },
      updated_by: "web",
    });
    expect(textOf("routing-notice")).toContain("工作区");
  });

  it("无改动时保存不发请求", async () => {
    getSessionRoutingMock.mockResolvedValue(routingFixture());

    await renderSection();
    click("routing-save");
    await flush();

    expect(updateSessionRoutingMock).not.toHaveBeenCalled();
    expect(textOf("routing-notice")).toBe("没有需要写入的改动。");
  });

  it("后端校验错误原样展示，并挂到命中的档位行上", async () => {
    getSessionRoutingMock.mockResolvedValue(routingFixture());
    updateSessionRoutingMock.mockRejectedValue(
      new RuntimeApiError(400, {
        error: "invalid routing override: main_agent.profiles.hard.model must not be empty",
        code: "invalid_request",
      }),
    );

    await renderSection();
    setInput("routing-field-hard-model", "  ");
    click("routing-save");
    await flush();

    const banner = textOf("routing-error");
    expect(banner).toContain("main_agent.profiles.hard.model must not be empty");
    // 命中行：字段标签 + 后端原文；不翻译、不改写。
    const rowError = textOf("routing-row-error-hard");
    expect(rowError).toContain("model:");
    expect(rowError).toContain("main_agent.profiles.hard.model must not be empty");
    // 未命中的档位行不显示错误。
    expect(query("routing-row-error-easy")).toBeNull();
  });

  it("config 层二次确认：确认前不发请求，确认后带 confirm=true", async () => {
    getSessionRoutingMock.mockResolvedValue(routingFixture());
    updateSessionRoutingMock.mockResolvedValue(
      routingFixture({
        targetLayer: "config",
        targetPath: "C:/Users/vince/.aicli/config.yaml",
        updated: true,
      }),
    );

    await renderSection();
    click("routing-layer-config");
    setInput("routing-field-hard-provider", "openai-compatible");
    click("routing-save");
    await flush();

    // 二次确认未点之前不发请求。
    expect(updateSessionRoutingMock).not.toHaveBeenCalled();
    expect(textOf("routing-config-confirm")).toContain(
      "C:/Users/vince/.aicli/config.yaml",
    );

    click("routing-config-confirm-ok");
    await flush();

    expect(updateSessionRoutingMock).toHaveBeenCalledTimes(1);
    expect(updateSessionRoutingMock.mock.calls[0][1]).toEqual({
      target_layer: "config",
      confirm: true,
      main_agent: { profiles: { hard: { provider: "openai-compatible" } } },
      updated_by: "web",
    });
  });

  it("config 层二次确认期间草稿改回原值：确认不再发请求（空补丁不落盘）", async () => {
    getSessionRoutingMock.mockResolvedValue(routingFixture());

    await renderSection();
    click("routing-layer-config");
    setInput("routing-field-hard-provider", "openai-compatible");
    click("routing-save");
    await flush();

    expect(textOf("routing-config-confirm")).toContain(
      "C:/Users/vince/.aicli/config.yaml",
    );

    // 确认框仍开着时把草稿改回投影原值 → 补丁变空。
    setInput("routing-field-hard-provider", "opencode.ai");
    click("routing-config-confirm-ok");
    await flush();

    // 空补丁 + confirm 会被后端按「清除该层覆盖」处理（config 层会清掉全局
    // routing 节）：必须拦在前端，不发这次请求。
    expect(updateSessionRoutingMock).not.toHaveBeenCalled();
    expect(textOf("routing-notice")).toBe("没有需要写入的改动。");
    expect(query("routing-config-confirm")).toBeNull();
  });

  it("当前层不可写（子会话只读）时档位输入框与开关一并置灰", async () => {
    getSessionRoutingMock.mockResolvedValue(
      routingFixture({
        panel: {
          ...routingFixture().panel,
          childSession: true,
          writableLayers: [],
        },
      }),
    );

    await renderSection();

    expect(query<HTMLInputElement>("routing-field-hard-model")?.disabled).toBe(
      true,
    );
    expect(
      query<HTMLInputElement>("routing-field-easy-provider")?.disabled,
    ).toBe(true);
    expect(query<HTMLInputElement>("routing-enabled-toggle")?.disabled).toBe(
      true,
    );
  });

  it("保存中档位输入框置灰，避免在途写入期间继续改草稿", async () => {
    getSessionRoutingMock.mockResolvedValue(routingFixture());
    // 永不落地的写入：saving 保持为 true。
    updateSessionRoutingMock.mockReturnValueOnce(new Promise(() => {}));

    await renderSection();
    setInput("routing-field-hard-model", "next-model");
    click("routing-save");
    await flush();

    expect(updateSessionRoutingMock).toHaveBeenCalledTimes(1);
    expect(query<HTMLInputElement>("routing-field-hard-model")?.disabled).toBe(
      true,
    );
  });

  it("重置发送 clear=true 清除该层覆盖", async () => {
    getSessionRoutingMock.mockResolvedValue(routingFixture());
    updateSessionRoutingMock.mockResolvedValue(
      routingFixture({ targetLayer: "session", updated: true }),
    );

    await renderSection();
    click("routing-reset");
    await flush();

    expect(updateSessionRoutingMock.mock.calls[0][1]).toEqual({
      target_layer: "session",
      clear: true,
      updated_by: "web",
    });
    expect(textOf("routing-notice")).toBe("已清除「会话」层的路由覆盖。");
  });

  it("后端未开放写入的层置灰并标注，子会话给出说明", async () => {
    getSessionRoutingMock.mockResolvedValue(
      routingFixture({
        panel: {
          ...routingFixture().panel,
          childSession: true,
          writableLayers: ["session"],
        },
      }),
    );

    await renderSection();

    expect(query<HTMLButtonElement>("routing-layer-session")?.disabled).toBe(false);
    const workspaceLayer = query<HTMLButtonElement>("routing-layer-workspace");
    const configLayer = query<HTMLButtonElement>("routing-layer-config");
    expect(workspaceLayer?.disabled).toBe(true);
    expect(configLayer?.disabled).toBe(true);
    expect(workspaceLayer?.textContent).toContain("不可写");
    expect(workspaceLayer?.getAttribute("title")).toContain("已置灰");
    expect(query<HTMLButtonElement>("routing-save")?.disabled).toBe(false);
    expect(textOf("routing-child-session-hint")).toContain("子会话不写路由覆盖");
  });

  it("无会话 id 时不渲染也不发请求", async () => {
    await renderSection("");

    expect(getSessionRoutingMock).not.toHaveBeenCalled();
    expect(query("session-detail-routing")).toBeNull();
  });

  it("读取失败时给出后端原因并可刷新", async () => {
    getSessionRoutingMock
      .mockRejectedValueOnce(new Error("routing endpoint unavailable"))
      .mockResolvedValueOnce(routingFixture());

    await renderSection();

    expect(textOf("session-detail-routing")).toContain("路由读取失败");
    expect(textOf("session-detail-routing")).toContain(
      "routing endpoint unavailable",
    );

    click("routing-retry");
    await flush();

    expect(getSessionRoutingMock).toHaveBeenCalledTimes(2);
    expect(textOf("routing-summary-model")).toBe("deepseek-v4.1");
  });
});
