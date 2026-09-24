// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  UsageLedgerView,
  UsagePolicyDetails,
  UsageStatsView,
} from "@/types/runtime";

const { getUsageLedgerMock, getUsagePolicyMock, getUsageStatsMock } = vi.hoisted(() => ({
  getUsageLedgerMock: vi.fn(),
  getUsagePolicyMock: vi.fn(),
  getUsageStatsMock: vi.fn(),
}));

vi.mock("@/api/runtime", () => ({
  getUsageLedger: getUsageLedgerMock,
  getUsagePolicy: getUsagePolicyMock,
  getUsageStats: getUsageStatsMock,
}));

import { UsageQuotaPanel } from "./quota";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function globalStats(): UsageStatsView {
  return {
    tracking_enabled: true,
    policy: {
      tracking_enabled: true,
      ledger_enabled: true,
      quota_enabled: true,
      default_max_requests: 100,
      default_max_tokens: 50_000,
      tenant_quota_count: 0,
      project_quota_count: 0,
      user_quota_count: 1,
    },
    scope: null,
    quota: null,
    usage: {
      scope_count: 1,
      user_count: 1,
      request_count: 9,
      execute_count: 6,
      agent_chat_count: 3,
      success_count: 8,
      failure_count: 1,
      prompt_tokens: 7_000,
      completion_tokens: 3_000,
      total_tokens: 10_000,
      last_skill: "",
      last_entrypoint: "",
      last_request_at: "",
    },
    scopes: [{ tenant_id: "", project_id: "", user_id: "alice", scope_key: "alice" }],
  };
}

function scopedStats(): UsageStatsView {
  return {
    ...globalStats(),
    scope: { tenant_id: "", project_id: "", user_id: "alice", scope_key: "alice" },
    quota: {
      scope_key: "alice",
      enabled: true,
      max_requests: 100,
      max_tokens: 50_000,
      remaining_requests: 91,
      remaining_tokens: 41_500,
      resolved_from: "user",
    },
    usage: {
      ...globalStats().usage,
      scope_count: 0,
      user_count: 0,
      last_skill: "ledger-skill",
      last_entrypoint: "execute",
      last_request_at: "2026-09-13T10:00:00Z",
    },
    scopes: [],
  };
}

function policyView(): UsagePolicyDetails {
  return {
    tracking_enabled: true,
    ledger_enabled: true,
    quota_enabled: true,
    default_max_requests: 100,
    default_max_tokens: 50_000,
    tenants: [],
    projects: [],
    users: [{ level: "user", key: "alice", max_requests: 100, max_tokens: 50_000 }],
  };
}

function ledgerView(): UsageLedgerView {
  return {
    records: [
      {
        id: "rec-1",
        request_id: "req-1",
        model_id: "00000000-0000-0000-0000-000000000000",
        provider_id: "00000000-0000-0000-0000-000000000000",
        input_tokens: 900,
        output_tokens: 300,
        total_tokens: 1_200,
        message_count: 3,
        max_tokens: 50_000,
        success: true,
        status_code: 200,
        metadata: { entrypoint: "execute", skill: "ledger-skill", scope_key: "alice" },
        created_at: "2026-09-13T10:00:00Z",
      },
      {
        id: "rec-2",
        request_id: "req-2",
        model_id: "00000000-0000-0000-0000-000000000000",
        provider_id: "00000000-0000-0000-0000-000000000000",
        input_tokens: 100,
        output_tokens: 0,
        total_tokens: 100,
        message_count: 1,
        max_tokens: 0,
        success: false,
        status_code: 500,
        metadata: { entrypoint: "agent_chat" },
        created_at: "2026-09-13T09:00:00Z",
      },
    ],
    count: 2,
    limit: 50,
    profileGroups: [
      {
        profile: "reviewer",
        requests: 1,
        failures: 0,
        input_tokens: 900,
        output_tokens: 300,
        total_tokens: 1_200,
      },
      {
        profile: "",
        requests: 1,
        failures: 1,
        input_tokens: 100,
        output_tokens: 0,
        total_tokens: 100,
      },
    ],
    groupedTotal: 2,
  };
}

function httpError(status: number, message: string): Error {
  return Object.assign(new Error(message), { status });
}

function setInputValue(input: HTMLInputElement, value: string) {
  // 必须走原型上的原生 setter：React 的值追踪器会吞掉直接赋值后的 input 事件。
  const nativeSetter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    "value",
  )?.set;
  nativeSetter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("UsageQuotaPanel", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    getUsageStatsMock.mockReset();
    getUsagePolicyMock.mockReset();
    getUsageLedgerMock.mockReset();
    getUsageStatsMock.mockResolvedValue(globalStats());
    getUsagePolicyMock.mockResolvedValue(policyView());
    getUsageLedgerMock.mockResolvedValue(ledgerView());
    // jsdom 未实现 scrollIntoView，Select 展开时会调用它。
    Element.prototype.scrollIntoView = vi.fn();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function render(adminToken = "admin-secret") {
    act(() => {
      root.render(<UsageQuotaPanel adminToken={adminToken} />);
    });
  }

  async function flush() {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it("渲染策略、用量与账本；全局视图不伪造配额余量", async () => {
    render();
    await flush();

    expect(container.textContent).toContain("用量与配额");
    expect(container.textContent).toContain("用量追踪");
    expect(container.textContent).toContain("请求 100 / Token 50,000");
    expect(container.textContent).toContain("记录作用域 1");
    expect(container.textContent).toContain("选择具体作用域后显示配额余量");
    expect(container.textContent).toContain("ledger-skill");
    expect(container.textContent).toContain("1,200");
    expect(container.textContent).toContain("成功");
    expect(getUsageStatsMock).toHaveBeenCalledWith({
      tenantId: "",
      projectId: "",
      userId: "",
      adminToken: "admin-secret",
    });
  });

  it("应用账本筛选后按新条件重取（入口 / 技能 / 条数）", async () => {
    render();
    await flush();

    const entrypointInput = container.querySelector<HTMLInputElement>(
      'input[placeholder="如 execute / agent_chat"]',
    );
    const skillInput = container.querySelector<HTMLInputElement>(
      'input[placeholder="按技能过滤"]',
    );
    expect(entrypointInput).not.toBeNull();
    expect(skillInput).not.toBeNull();

    act(() => {
      setInputValue(entrypointInput as HTMLInputElement, "execute");
      setInputValue(skillInput as HTMLInputElement, "ledger-skill");
    });

    const apply = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent === "应用筛选",
    );
    expect(apply?.disabled).toBe(false);
    act(() => apply?.click());
    await flush();

    expect(getUsageLedgerMock).toHaveBeenLastCalledWith({
      tenantId: "",
      projectId: "",
      userId: "",
      entrypoint: "execute",
      skill: "ledger-skill",
      success: undefined,
      since: "",
      limit: 50,
      groupBy: "profile",
      adminToken: "admin-secret",
    });
  });

  it("403 提示补 admin token，503 说明账本未配置（不假装正常）", async () => {
    getUsageStatsMock.mockRejectedValue(httpError(403, "forbidden"));
    getUsagePolicyMock.mockRejectedValue(httpError(403, "forbidden"));
    getUsageLedgerMock.mockRejectedValue(httpError(503, "usage ledger not configured"));

    render();
    await flush();

    expect(container.textContent).toContain("填入 Admin Token 后可读取用量与配额");
    expect(container.textContent).toContain("后端未配置 usage ledger（503），账本不可用");
    expect(container.textContent).toContain("账本数据不可用");
    // 403 由 admin token 提示承载，不再重复渲染三行红色横幅
    expect(container.querySelectorAll('[role="alert"]')).toHaveLength(1);
  });

  it("账本 503 已启用但初始化失败时，说明「不可用 + 原因」而非「未配置」", async () => {
    getUsageLedgerMock.mockRejectedValue(
      httpError(
        503,
        "[CONFIG_INVALID] usage ledger unavailable: initialize skills usage ledger store: " +
          "unsupported usage ledger driver: postgres",
      ),
    );

    render();
    await flush();

    expect(container.textContent).toContain("后端已启用 usage ledger 但初始化失败（503）");
    expect(container.textContent).toContain("unsupported usage ledger driver: postgres");
    // 关键：不能把「启用但初始化失败」说成「未配置」，否则会把人带向开关而不是 dsn / 驱动
    expect(container.textContent).not.toContain("后端未配置 usage ledger（503）");
    expect(container.textContent).toContain("账本数据不可用");
  });

  it("结构不满足时抛出并显示错误消息（不显示伪造空态）", async () => {
    getUsageStatsMock.mockRejectedValue(new Error("invalid usage stats payload"));

    render();
    await flush();

    expect(container.textContent).toContain("用量统计读取失败");
    expect(container.textContent).toContain("invalid usage stats payload");
    // 失败段不遮蔽其余段：账本与策略照常呈现真实数据
    expect(container.textContent).toContain("ledger-skill");
    expect(container.textContent).toContain("alice");
  });

  it("切换到具体作用域后展示剩余配额与生效来源", async () => {
    render();
    await flush();

    getUsageStatsMock.mockResolvedValue(scopedStats());
    const scopeSelect = container.querySelector<HTMLButtonElement>(
      'button[aria-label="作用域"]',
    );
    expect(scopeSelect).not.toBeNull();
    act(() => scopeSelect?.click());
    await flush();

    const option = Array.from(document.querySelectorAll<HTMLElement>('[role="option"]')).find(
      (item) => item.textContent === "alice",
    );
    expect(option).toBeTruthy();
    act(() => option?.click());
    await flush();

    expect(getUsageStatsMock).toHaveBeenLastCalledWith({
      tenantId: "",
      projectId: "",
      userId: "alice",
      adminToken: "admin-secret",
    });
    expect(container.textContent).toContain("91 / 100");
    expect(container.textContent).toContain("41,500 / 50,000");
    expect(container.textContent).toContain("用户");
    expect(container.textContent).toContain("ledger-skill");
    // 具体作用域响应不再带 scopes，选择器仍保留已记住的可选项，不会自我清空。
    expect(scopeSelect?.textContent).toContain("alice");

    // 仍可切回全局聚合：选择器里保留着全局项。
    act(() => scopeSelect?.click());
    await flush();
    const globalOption = Array.from(
      document.querySelectorAll<HTMLElement>('[role="option"]'),
    ).find((item) => item.textContent === "全局聚合");
    expect(globalOption).toBeTruthy();
    act(() => globalOption?.click());
    await flush();

    expect(getUsageStatsMock).toHaveBeenLastCalledWith({
      tenantId: "",
      projectId: "",
      userId: "",
      adminToken: "admin-secret",
    });
  });

  it("渲染按 profile 分组表：未归属组与聚合总数如实呈现", async () => {
    render();
    await flush();

    expect(container.textContent).toContain("按 Profile 分组");
    expect(container.textContent).toContain("参与聚合 2 条（截断前全量）");
    expect(container.textContent).toContain("reviewer");
    expect(container.textContent).toContain("未归属");
    // 面板固定请求 group_by=profile（分组是固有展示面，不是用户筛选）
    expect(getUsageLedgerMock).toHaveBeenLastCalledWith({
      tenantId: "",
      projectId: "",
      userId: "",
      entrypoint: "",
      skill: "",
      success: undefined,
      since: "",
      limit: 50,
      groupBy: "profile",
      adminToken: "admin-secret",
    });
  });

  it("后端未返回分组数据时如实提示（不伪造空分组）", async () => {
    getUsageLedgerMock.mockResolvedValue({
      ...ledgerView(),
      profileGroups: null,
      groupedTotal: null,
    });
    render();
    await flush();

    expect(container.textContent).toContain("后端未返回按 profile 的分组数据");
    expect(container.textContent).not.toContain("按 Profile 分组");
  });

  it("空分组数组是真实空态（区别于未返回分组）", async () => {
    getUsageLedgerMock.mockResolvedValue({
      ...ledgerView(),
      profileGroups: [],
      groupedTotal: 0,
    });
    render();
    await flush();

    expect(container.textContent).toContain("按 Profile 分组");
    expect(container.textContent).toContain("当前筛选下没有可聚合的分组");
    expect(container.textContent).not.toContain("后端未返回按 profile 的分组数据");
  });
});
