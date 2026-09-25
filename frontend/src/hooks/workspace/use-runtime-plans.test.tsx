// @vitest-environment jsdom

// 归档计划数据源单测：首屏列表 → 详情选择 → 失败态 → 手动刷新 → 运行时事件驱动刷新。
//
// 断言纪律：只断言「用户可见结论」——列表内容、选中项、错误文本、请求次数；
// URL 分段编码由 api/runtime/plans.test.ts 单独钉住，这里只验证调用的是原始 id。

import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { buildRuntimeEventReloadKey } from "@/hooks/workspace/use-runtime-checkpoints";
import type { RuntimeStoredPlan } from "@/types/runtime";

import {
  PLAN_REVIEW_REQUESTED_EVENT,
  shouldReloadRuntimePlans,
  useRuntimePlans,
} from "./use-runtime-plans";

const { getRuntimePlanMock, listRuntimePlansMock } = vi.hoisted(() => ({
  getRuntimePlanMock: vi.fn(),
  listRuntimePlansMock: vi.fn(),
}));

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return {
    ...actual,
    getRuntimePlan: getRuntimePlanMock,
    listRuntimePlans: listRuntimePlansMock,
  };
});

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

type UseRuntimePlansResult = ReturnType<typeof useRuntimePlans>;

let container: HTMLDivElement;
let root: Root | null = null;

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

async function settle() {
  for (let index = 0; index < 4; index += 1) {
    await act(async () => {
      await flush();
    });
  }
}

function plan(id: string, overrides: Partial<RuntimeStoredPlan> = {}): RuntimeStoredPlan {
  return {
    id,
    status: "pending",
    version: 1,
    rounds: [],
    content: "",
    content_available: false,
    ...overrides,
  };
}

function Harness({
  holderRef,
  lastRuntimeEventType,
  runtimeEventCount,
}: {
  holderRef: { current: UseRuntimePlansResult | null };
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
}) {
  const result = useRuntimePlans({ lastRuntimeEventType, runtimeEventCount });
  // 测试探针：在 effect 里写外部 ref（渲染期写 ref 会被 react-hooks/refs 拦下）。
  useEffect(() => {
    holderRef.current = result;
  });
  return null;
}

async function mount(
  holder: { current: UseRuntimePlansResult | null },
  props: { lastRuntimeEventType?: string; runtimeEventCount?: number } = {},
) {
  await act(async () => {
    root?.render(<Harness holderRef={holder} {...props} />);
  });
  await settle();
}

async function rerender(
  holder: { current: UseRuntimePlansResult | null },
  props: { lastRuntimeEventType?: string; runtimeEventCount?: number },
) {
  await act(async () => {
    root?.render(<Harness holderRef={holder} {...props} />);
  });
  await settle();
}

describe("shouldReloadRuntimePlans", () => {
  it("只在计划相关事件且事件键变化时重载", () => {
    const key = buildRuntimeEventReloadKey(PLAN_REVIEW_REQUESTED_EVENT, 1);

    expect(
      shouldReloadRuntimePlans({
        lastHandledEventKey: "",
        lastRuntimeEventKey: key,
        lastRuntimeEventType: PLAN_REVIEW_REQUESTED_EVENT,
      }),
    ).toBe(true);

    expect(
      shouldReloadRuntimePlans({
        lastHandledEventKey: key,
        lastRuntimeEventKey: key,
        lastRuntimeEventType: PLAN_REVIEW_REQUESTED_EVENT,
      }),
    ).toBe(false);

    expect(
      shouldReloadRuntimePlans({
        lastHandledEventKey: "",
        lastRuntimeEventKey: buildRuntimeEventReloadKey("tool.completed", 2),
        lastRuntimeEventType: "tool.completed",
      }),
    ).toBe(false);

    expect(
      shouldReloadRuntimePlans({
        lastRuntimeEventKey: "",
        lastRuntimeEventType: "",
      }),
    ).toBe(false);
  });
});

describe("useRuntimePlans", () => {
  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    getRuntimePlanMock.mockReset();
    listRuntimePlansMock.mockReset();

    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("挂载即拉取列表，成功后进入已加载态", async () => {
    listRuntimePlansMock.mockResolvedValue({
      plans: [plan("proj/plan")],
      count: 1,
    });

    const holder: { current: UseRuntimePlansResult | null } = { current: null };
    await mount(holder);

    expect(listRuntimePlansMock).toHaveBeenCalledTimes(1);
    expect(holder.current?.plans.map((item) => item.id)).toEqual(["proj/plan"]);
    expect(holder.current?.plansLoading).toBe(false);
    expect(holder.current?.plansError).toBeNull();
    expect(holder.current?.loadedOnce).toBe(true);
  });

  it("select 拉取详情，select(null) 清空选择", async () => {
    listRuntimePlansMock.mockResolvedValue({
      plans: [plan("proj/plan")],
      count: 1,
    });
    getRuntimePlanMock.mockResolvedValue(
      plan("proj/plan", {
        status: "approved",
        version: 2,
        content: "# Plan",
        content_available: true,
      }),
    );

    const holder: { current: UseRuntimePlansResult | null } = { current: null };
    await mount(holder);

    act(() => holder.current?.select("proj/plan"));
    await settle();

    // 传原始 id；逐段编码由 API 层负责（见 api/runtime/plans.test.ts）。
    expect(getRuntimePlanMock).toHaveBeenCalledWith("proj/plan");
    expect(holder.current?.selectedPlanId).toBe("proj/plan");
    expect(holder.current?.selectedPlan?.content).toBe("# Plan");
    expect(holder.current?.detailLoading).toBe(false);
    expect(holder.current?.detailError).toBeNull();

    act(() => holder.current?.select(null));
    expect(holder.current?.selectedPlanId).toBeNull();
    expect(holder.current?.selectedPlan).toBeNull();
    expect(holder.current?.detailError).toBeNull();
  });

  it("详情 404 错误不吞掉列表，重试成功后恢复", async () => {
    listRuntimePlansMock.mockResolvedValue({
      plans: [plan("proj/ghost")],
      count: 1,
    });
    getRuntimePlanMock.mockRejectedValue(new Error("plan not found: proj/ghost"));

    const holder: { current: UseRuntimePlansResult | null } = { current: null };
    await mount(holder);

    act(() => holder.current?.select("proj/ghost"));
    await settle();

    expect(holder.current?.detailError).toBe("plan not found: proj/ghost");
    expect(holder.current?.selectedPlan).toBeNull();
    // 列表数据保留：详情失败只影响详情区。
    expect(holder.current?.plans.map((item) => item.id)).toEqual(["proj/ghost"]);

    getRuntimePlanMock.mockResolvedValue(plan("proj/ghost", { content: "# 恢复" }));
    act(() => holder.current?.select("proj/ghost"));
    await settle();

    expect(holder.current?.detailError).toBeNull();
    expect(holder.current?.selectedPlan?.content).toBe("# 恢复");
  });

  it("列表失败给出错误，refresh() 重拉并清除错误", async () => {
    listRuntimePlansMock.mockRejectedValueOnce(new Error("plan store unavailable"));

    const holder: { current: UseRuntimePlansResult | null } = { current: null };
    await mount(holder);

    expect(holder.current?.plansError).toBe("plan store unavailable");
    expect(holder.current?.loadedOnce).toBe(true);
    expect(holder.current?.plansLoading).toBe(false);

    listRuntimePlansMock.mockResolvedValue({
      plans: [plan("proj/plan")],
      count: 1,
    });
    await act(async () => {
      await holder.current?.refresh();
    });

    expect(holder.current?.plansError).toBeNull();
    expect(holder.current?.plans.map((item) => item.id)).toEqual(["proj/plan"]);
  });

  it("plan_review_requested 触发列表刷新，同一事件键不重复请求，其它事件不触发", async () => {
    listRuntimePlansMock.mockResolvedValue({ plans: [plan("proj/plan")], count: 1 });

    const holder: { current: UseRuntimePlansResult | null } = { current: null };
    await mount(holder, { lastRuntimeEventType: "tool.completed", runtimeEventCount: 2 });
    const callsAfterMount = listRuntimePlansMock.mock.calls.length;

    listRuntimePlansMock.mockResolvedValue({
      plans: [plan("proj/plan"), plan("proj/second", { status: "approved", version: 3 })],
      count: 2,
    });
    await rerender(holder, {
      lastRuntimeEventType: PLAN_REVIEW_REQUESTED_EVENT,
      runtimeEventCount: 3,
    });

    expect(listRuntimePlansMock.mock.calls.length).toBe(callsAfterMount + 1);
    expect(holder.current?.plans.map((item) => item.id)).toEqual([
      "proj/plan",
      "proj/second",
    ]);

    // 同一事件键重复投影（面板重渲染）不得再次拉取。
    await rerender(holder, {
      lastRuntimeEventType: PLAN_REVIEW_REQUESTED_EVENT,
      runtimeEventCount: 3,
    });
    expect(listRuntimePlansMock.mock.calls.length).toBe(callsAfterMount + 1);

    // 无关事件不触发重载。
    await rerender(holder, { lastRuntimeEventType: "tool.completed", runtimeEventCount: 4 });
    expect(listRuntimePlansMock.mock.calls.length).toBe(callsAfterMount + 1);
  });

  it("plan_review_requested 同时刷新已打开的详情", async () => {
    listRuntimePlansMock.mockResolvedValue({ plans: [plan("proj/plan")], count: 1 });
    getRuntimePlanMock.mockResolvedValue(plan("proj/plan", { content: "# 第一版" }));

    const holder: { current: UseRuntimePlansResult | null } = { current: null };
    await mount(holder, { lastRuntimeEventType: "tool.completed", runtimeEventCount: 1 });

    act(() => holder.current?.select("proj/plan"));
    await settle();
    expect(getRuntimePlanMock).toHaveBeenCalledTimes(1);

    getRuntimePlanMock.mockResolvedValue(plan("proj/plan", { content: "# 第二版" }));
    await rerender(holder, {
      lastRuntimeEventType: PLAN_REVIEW_REQUESTED_EVENT,
      runtimeEventCount: 2,
    });

    expect(getRuntimePlanMock).toHaveBeenCalledTimes(2);
    expect(holder.current?.selectedPlan?.content).toBe("# 第二版");
  });
});
