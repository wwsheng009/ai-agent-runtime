// @vitest-environment jsdom

// composer 上下文控件的行为：环上的百分比来自会话用量明细，点击展开面板，
// 手动压缩把 compact 命令交给 API，并用响应里的 token 立刻覆盖环上的显示。
//
// 渲染方式沿用仓库约定（createRoot + act + DOM 查询，见 artifact-detail-dialog.test.tsx），
// 不引入 testing-library。用量 hook 被替换成可控状态，避免测试里发真实请求。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AnalyticsSessionUsageDetail, AnalyticsStepUsage } from "@/types/runtime";

const mocks = vi.hoisted(() => ({
  usage: {
    error: null as string | null,
    loading: false,
    refresh: vi.fn(),
    usage: null as unknown,
  },
}));

vi.mock("@/hooks/workspace/use-session-usage", () => ({
  useSessionUsage: () => mocks.usage,
}));

vi.mock("@/api/runtime", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime")>();
  return { ...actual, compactSessionContext: vi.fn() };
});

import { compactSessionContext } from "@/api/runtime";

import { ComposerContextUsageControl } from "./composer-context-usage-control";

const mockCompact = vi.mocked(compactSessionContext);

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function buildStep(overrides: Partial<AnalyticsStepUsage>): AnalyticsStepUsage {
  return { success: true, usage_available: true, ...overrides };
}

function useSteps(steps: AnalyticsStepUsage[]): void {
  mocks.usage.usage = {
    steps,
  } as unknown as AnalyticsSessionUsageDetail;
}

const compactStatus = {
  maxContextTokens: 100_000,
  mode: "local",
  model: "",
  phase: "pre_turn",
  provider: "",
  reason: "",
  tokenBefore: 80_000,
  triggerTokenLimit: 60_000,
} as const;

describe("ComposerContextUsageControl", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;

    mockCompact.mockReset();
    mocks.usage.error = null;
    mocks.usage.loading = false;
    mocks.usage.refresh.mockClear();
    mocks.usage.usage = null;
  });

  afterEach(() => {
    if (root) {
      act(() => {
        root?.unmount();
      });
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderControl(props: { isResponding?: boolean } = {}) {
    root = createRoot(container);
    act(() => {
      root?.render(
        <ComposerContextUsageControl
          isResponding={props.isResponding}
          sessionId="session-1"
        />,
      );
    });
  }

  function query(testId: string): Element | null {
    return document.body.querySelector(`[data-testid="${testId}"]`);
  }

  function click(element: Element | null) {
    act(() => {
      element?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
  }

  it("环内显示占用百分比，并把精确百分比写进无障碍标签", () => {
    useSteps([
      buildStep({ context_prompt_tokens: 42_000, context_window_tokens: 100_000 }),
    ]);
    renderControl();

    const trigger = query("composer-context-usage-trigger");
    expect(trigger?.textContent).toBe("42%");
    expect(trigger?.getAttribute("aria-label")).toContain("42.0%");
  });

  it("窗口未知时不伪造百分比，环退化为破折号", () => {
    useSteps([buildStep({ prompt_tokens: 5_000 })]);
    renderControl();

    expect(query("composer-context-usage-trigger")?.textContent).toBe("—");
  });

  it("点击后在触发按钮旁展开面板并列出上下文明细", () => {
    useSteps([
      buildStep({
        context_prompt_tokens: 42_000,
        context_window_tokens: 100_000,
        prompt_budget: 60_000,
      }),
    ]);
    renderControl();

    expect(query("composer-context-usage-panel")).toBeNull();
    click(query("composer-context-usage-trigger"));

    const panel = query("composer-context-usage-panel");
    expect(panel).not.toBeNull();
    expect(panel?.textContent).toContain("会话上下文");
    expect(panel?.textContent).toContain("已用上下文");
    expect(panel?.textContent).toContain("42.0%");
  });

  it("响应中禁用压缩并说明原因", () => {
    useSteps([buildStep({ context_prompt_tokens: 1_000, context_window_tokens: 10_000 })]);
    renderControl({ isResponding: true });
    click(query("composer-context-usage-trigger"));

    const action = query("composer-context-compact-action");
    expect((action as HTMLButtonElement).disabled).toBe(true);
    expect(query("composer-context-usage-panel")?.textContent).toContain(
      "响应中不可压缩",
    );
  });

  it("压缩成功后提示结果、重新拉取用量，并立刻用 token_after 覆盖环上的占用", async () => {
    useSteps([
      buildStep({ context_prompt_tokens: 80_000, context_window_tokens: 100_000 }),
    ]);
    mockCompact.mockResolvedValue({
      result: {
        ...compactStatus,
        checkpointIds: ["ckpt-1"],
        compactedMessages: 6,
        tokenAfter: 20_000,
        usageSource: "provider",
      },
      status: compactStatus,
    });
    renderControl();
    click(query("composer-context-usage-trigger"));

    await act(async () => {
      query("composer-context-compact-action")?.dispatchEvent(
        new MouseEvent("click", { bubbles: true }),
      );
    });

    expect(mockCompact).toHaveBeenCalledWith("session-1", { mode: "auto" });
    expect(mocks.usage.refresh).toHaveBeenCalled();
    const panel = query("composer-context-usage-panel");
    expect(panel?.textContent).toContain("已压缩 6 条历史");
    expect(panel?.textContent).toContain("新增 1 个检查点");
    expect(query("composer-context-usage-trigger")?.textContent).toBe("20%");
  });

  it("运行时判定无需压缩（result=null）时给出中性提示，环不被覆盖", async () => {
    useSteps([
      buildStep({ context_prompt_tokens: 30_000, context_window_tokens: 100_000 }),
    ]);
    mockCompact.mockResolvedValue({
      result: null,
      status: { ...compactStatus, reason: "below_threshold", tokenBefore: 30_000 },
    });
    renderControl();
    click(query("composer-context-usage-trigger"));

    await act(async () => {
      query("composer-context-compact-action")?.dispatchEvent(
        new MouseEvent("click", { bubbles: true }),
      );
    });

    expect(query("composer-context-usage-panel")?.textContent).toContain(
      "below_threshold",
    );
    expect(query("composer-context-usage-trigger")?.textContent).toBe("30%");
  });
});
