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

import {
  isUsageQuotaAdminForbidden,
  isUsageQuotaLedgerDisabled,
  isUsageQuotaLedgerUnavailable,
  toUsageQuotaSectionError,
  useUsageQuota,
  type UsageQuotaLedgerFilters,
  type UsageQuotaScope,
  type UseUsageQuotaResult,
} from "./use-usage-quota";

/** 后端「已启用但初始化失败」的 503：message 带降级原因，且不含 not configured。 */
function ledgerBroken(): Error {
  return Object.assign(
    new Error(
      "usage ledger unavailable: initialize skills usage ledger store: " +
        "unsupported usage ledger driver: postgres",
    ),
    { status: 503 },
  );
}

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function statsView(scopeKey: string | null): UsageStatsView {
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
    scope: scopeKey
      ? { tenant_id: "", project_id: "", user_id: scopeKey, scope_key: scopeKey }
      : null,
    quota: scopeKey
      ? {
          scope_key: scopeKey,
          enabled: true,
          max_requests: 100,
          max_tokens: 50_000,
          remaining_requests: 93,
          remaining_tokens: 41_500,
          resolved_from: "user",
        }
      : null,
    usage: {
      scope_count: scopeKey ? 0 : 1,
      user_count: scopeKey ? 0 : 1,
      request_count: 7,
      execute_count: 5,
      agent_chat_count: 2,
      success_count: 6,
      failure_count: 1,
      prompt_tokens: 6_000,
      completion_tokens: 2_500,
      total_tokens: 8_500,
      last_skill: "",
      last_entrypoint: "",
      last_request_at: "",
    },
    scopes: scopeKey
      ? []
      : [{ tenant_id: "", project_id: "", user_id: "alice", scope_key: "alice" }],
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

function ledgerView(limit = 50): UsageLedgerView {
  return { records: [], count: 0, limit, profileGroups: null, groupedTotal: null };
}

function serviceUnavailable(): Error {
  return Object.assign(new Error("usage ledger not configured"), { status: 503 });
}

function forbidden(): Error {
  return Object.assign(new Error("forbidden"), { status: 403 });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function Harness({
  adminToken,
  ledgerFilters,
  onState,
  scope,
}: {
  adminToken?: string;
  ledgerFilters?: UsageQuotaLedgerFilters;
  onState: (state: UseUsageQuotaResult) => void;
  scope?: UsageQuotaScope;
}) {
  const state = useUsageQuota({ adminToken, ledgerFilters, scope });
  onState(state);
  return null;
}

describe("useUsageQuota", () => {
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
    getUsageStatsMock.mockResolvedValue(statsView(null));
    getUsagePolicyMock.mockResolvedValue(policyView());
    getUsageLedgerMock.mockResolvedValue(ledgerView());
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderHook(props: {
    adminToken?: string;
    ledgerFilters?: UsageQuotaLedgerFilters;
    scope?: UsageQuotaScope;
  }) {
    let latest: UseUsageQuotaResult | null = null;
    function render(next: typeof props) {
      root.render(
        <Harness
          {...next}
          onState={(state) => {
            latest = state;
          }}
        />,
      );
    }
    act(() => render(props));
    return {
      get current(): UseUsageQuotaResult {
        if (!latest) {
          throw new Error("hook not rendered");
        }
        return latest;
      },
      update(next: typeof props) {
        act(() => render(next));
      },
    };
  }

  async function flush() {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it("并行拉取 stats / policy / ledger，并透传 scope 与账本筛选", async () => {
    const hook = renderHook({
      adminToken: " admin-secret ",
      scope: { tenantId: "tenant-a", userId: "alice" },
      ledgerFilters: { entrypoint: "execute", success: false, limit: 20 },
    });

    await flush();

    expect(getUsageStatsMock).toHaveBeenCalledWith({
      tenantId: "tenant-a",
      projectId: "",
      userId: "alice",
      adminToken: "admin-secret",
    });
    expect(getUsagePolicyMock).toHaveBeenCalledWith({ adminToken: "admin-secret" });
    expect(getUsageLedgerMock).toHaveBeenCalledWith({
      tenantId: "tenant-a",
      projectId: "",
      userId: "alice",
      entrypoint: "execute",
      skill: "",
      success: false,
      since: "",
      limit: 20,
      groupBy: undefined,
      adminToken: "admin-secret",
    });
    expect(hook.current.loading).toBe(false);
    expect(hook.current.stats?.usage.request_count).toBe(7);
    expect(hook.current.policy?.users).toHaveLength(1);
    expect(hook.current.ledger?.limit).toBe(50);
    expect(hook.current.statsError).toBeNull();
    expect(hook.current.ledgerError).toBeNull();
  });

  it("账本分组参数透传：ledgerFilters.groupBy 进入请求，未传时为 undefined（不发 group_by）", async () => {
    const hook = renderHook({ adminToken: "admin-secret" });
    await flush();

    expect(getUsageLedgerMock).toHaveBeenLastCalledWith({
      tenantId: "",
      projectId: "",
      userId: "",
      entrypoint: "",
      skill: "",
      success: undefined,
      since: "",
      limit: 50,
      groupBy: undefined,
      adminToken: "admin-secret",
    });

    hook.update({ adminToken: "admin-secret", ledgerFilters: { groupBy: "profile" } });
    await flush();

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

  it("账本 503 只标记账本段，stats / policy 仍呈现真实数据", async () => {
    getUsageLedgerMock.mockRejectedValue(serviceUnavailable());

    const hook = renderHook({ adminToken: "admin-secret" });

    await flush();

    expect(hook.current.ledger).toBeNull();
    expect(hook.current.ledgerError).toMatchObject({ status: 503 });
    expect(hook.current.stats?.usage.request_count).toBe(7);
    expect(hook.current.policy?.users).toHaveLength(1);
    expect(hook.current.statsError).toBeNull();
    expect(hook.current.policyError).toBeNull();
  });

  it("账本 503 区分「未启用」与「已启用但初始化失败」（不把降级原因说成未配置）", async () => {
    getUsageLedgerMock.mockRejectedValue(ledgerBroken());

    const hook = renderHook({ adminToken: "admin-secret" });

    await flush();

    expect(isUsageQuotaLedgerUnavailable(hook.current.ledgerError)).toBe(true);
    expect(isUsageQuotaLedgerDisabled(hook.current.ledgerError)).toBe(false);
    expect(hook.current.ledgerError?.message).toContain("unsupported usage ledger driver: postgres");
    // 判定只看 503 + `not configured`：未启用为 true，非 503 一律 false
    expect(
      isUsageQuotaLedgerDisabled({ status: 503, message: "usage ledger not configured" }),
    ).toBe(true);
    expect(isUsageQuotaLedgerDisabled({ status: 403, message: "usage ledger not configured" })).toBe(
      false,
    );
    expect(isUsageQuotaLedgerDisabled(null)).toBe(false);
  });

  it("403 由调用方按 admin token 缺失分类（不改写为通用错误）", async () => {
    getUsageStatsMock.mockRejectedValue(forbidden());
    getUsagePolicyMock.mockRejectedValue(forbidden());
    getUsageLedgerMock.mockRejectedValue(forbidden());

    const hook = renderHook({ adminToken: "" });

    await flush();

    expect(isUsageQuotaAdminForbidden(hook.current.statsError)).toBe(true);
    expect(isUsageQuotaAdminForbidden(hook.current.ledgerError)).toBe(true);
    expect(hook.current.stats).toBeNull();
    expect(hook.current.loading).toBe(false);
  });

  it("切换作用域后丢弃在途旧响应（慢响应不覆盖新作用域数据）", async () => {
    const slow = deferred<UsageStatsView>();
    getUsageStatsMock.mockReturnValueOnce(slow.promise);

    const hook = renderHook({ scope: { userId: "alice" } });
    await flush();

    getUsageStatsMock.mockResolvedValue(statsView("bob"));
    hook.update({ scope: { userId: "bob" } });
    await flush();
    expect(hook.current.stats?.scope?.scope_key).toBe("bob");

    await act(async () => {
      slow.resolve(statsView("alice"));
      await Promise.resolve();
    });

    expect(hook.current.stats?.scope?.scope_key).toBe("bob");
  });

  it("已知作用域在切到具体作用域后保留（供选择器回选）", async () => {
    const hook = renderHook({});
    await flush();
    expect(hook.current.knownScopes.map((item) => item.scope_key)).toEqual(["alice"]);

    getUsageStatsMock.mockResolvedValue(statsView("alice"));
    hook.update({ scope: { userId: "alice" } });
    await flush();

    // 具体作用域响应不再带 scopes，但已记住的列表不能被清空。
    expect(hook.current.stats?.scopes).toEqual([]);
    expect(hook.current.knownScopes.map((item) => item.scope_key)).toEqual(["alice"]);
  });

  it("refresh() 重新拉取三段", async () => {
    const hook = renderHook({});
    await flush();
    expect(getUsageStatsMock).toHaveBeenCalledTimes(1);

    act(() => {
      hook.current.refresh();
    });
    await flush();

    expect(getUsageStatsMock).toHaveBeenCalledTimes(2);
    expect(getUsagePolicyMock).toHaveBeenCalledTimes(2);
    expect(getUsageLedgerMock).toHaveBeenCalledTimes(2);
  });

  it("toUsageQuotaSectionError 归一化 status 与消息（非 Error 也不丢信息）", () => {
    expect(toUsageQuotaSectionError(serviceUnavailable())).toEqual({
      message: "usage ledger not configured",
      status: 503,
    });
    expect(toUsageQuotaSectionError("boom")).toEqual({ message: "boom", status: null });
    expect(isUsageQuotaLedgerUnavailable({ message: "x", status: 503 })).toBe(true);
    expect(isUsageQuotaLedgerUnavailable({ message: "x", status: 500 })).toBe(false);
  });
});
