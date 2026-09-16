// @vitest-environment jsdom
// P0-2：右栏宽度在容器上的落地（CSS 变量 + 覆盖层降级 + 未接线兜底）。
//
// 设计规则（§4.2）：
// - 容器只注入 `--right-rail-width`（网格第三列读同一变量），组件内不做宽度算术；
// - `<xl`（或主区放不下最小右栏）且当前面是宽内容面 → 覆盖层抽屉 + 遮罩 + Esc 关闭，
//   关闭后整列不渲染（回归红线 ① 由 workspace-shell 的网格共同保证）；
// - 激活面上抛契约：`onActiveSurfaceChange`；父代理未接线时按内容型面兜底，UI 不白屏。
//
// 回归红线：内容型面（288px）与关闭右栏（`rightRailOpen=false`）行为不变。

import { type TFunction } from "i18next";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  vi,
  type Mock,
} from "vitest";

import type { Thread } from "@/data/mock";
import { i18n } from "@/i18n";

import { WorkspaceRightRailSection } from "./right-rail-section";
import { type RightRailWidthController } from "@/hooks/workspace/use-right-rail-width";

// 激活面由父代理按契约上抛（artifact-panel 侧的受控 prop）；测试用它模拟接线/未接线两种状态。
const mockState = vi.hoisted(() => ({ surface: null as string | null }));

vi.mock("./lazy-surfaces", async () => {
  const { useEffect: usePanelEffect } = await import("react");
  return {
    ArtifactPanel: ({
      onActiveSurfaceChange,
    }: {
      onActiveSurfaceChange?: (surface: string) => void;
    }) => {
      usePanelEffect(() => {
        if (mockState.surface && onActiveSurfaceChange) {
          onActiveSurfaceChange(mockState.surface);
        }
      }, [onActiveSurfaceChange]);
      return <div data-testid="artifact-panel-stub" />;
    },
    ArtifactPanelFallback: () => <div data-testid="artifact-panel-fallback" />,
  };
});

const thread: Thread = {
  id: "thread-1",
  title: "Review runtime changes",
  summary: "",
  updatedAt: "2026-07-27T00:00:00Z",
  status: "active",
  sessionId: "session-1",
  tags: [],
  prompts: [],
  messages: [],
  artifacts: [],
};

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function controller(
  overrides: Partial<RightRailWidthController> = {},
): RightRailWidthController {
  return {
    mode: "auto",
    widthPx: 288,
    minWidthPx: 320,
    maxWidthPx: 672,
    viewportWidth: 1440,
    widthClass: "content",
    overlay: false,
    commitWidth: vi.fn<(px: number) => void>(),
    resetToAuto: vi.fn<() => void>(),
    ...overrides,
  };
}

describe("WorkspaceRightRailSection（P0-2 宽度）", () => {
  let container: HTMLDivElement;
  let root: Root;
  let onCloseRightRail: Mock<() => void>;

  beforeEach(() => {
    mockState.surface = null;
    onCloseRightRail = vi.fn<() => void>();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
    vi.restoreAllMocks();
  });

  function render(
    railWidth: RightRailWidthController,
    options: { rightRailOpen?: boolean } = {},
  ) {
    const t = i18n.getFixedT(null, "workspace") as unknown as TFunction<"workspace">;

    act(() => {
      root.render(
        <WorkspaceRightRailSection
          handleOpenArtifact={vi.fn()}
          isNewThread={false}
          isResponding={false}
          onCloseRightRail={onCloseRightRail}
          railWidth={railWidth}
          rightRailOpen={options.rightRailOpen ?? true}
          selectedArtifactId={null}
          selectedThread={thread}
          t={t}
        />,
      );
    });

    return container.firstElementChild as HTMLElement | null;
  }

  it("容器注入 --right-rail-width，手柄与该值同口径（组件内零算术）", () => {
    const rail = render(controller({ widthPx: 288 }));

    expect(rail?.style.getPropertyValue("--right-rail-width")).toBe("288px");
    const handle = container.querySelector(
      '[data-testid="right-rail-resize-handle"]',
    );
    expect(handle?.getAttribute("aria-valuenow")).toBe("288");
    expect(handle?.getAttribute("aria-valuemin")).toBe("320");
    expect(handle?.getAttribute("aria-valuemax")).toBe("672");
  });

  it("auto + 内容型面（288px < 手动下界）：变窄按键按边界处理，变宽按键落到 320px", () => {
    const railWidth = controller({ widthPx: 288 });
    render(railWidth);
    const handle = container.querySelector<HTMLElement>(
      '[data-testid="right-rail-resize-handle"]',
    );

    act(() => {
      handle?.dispatchEvent(
        new KeyboardEvent("keydown", { bubbles: true, key: "ArrowRight" }),
      );
    });
    // 288 − 16 会被 clamp 抬回 320（反而变宽）→ 视为边界：不提交、不退出 auto。
    expect(railWidth.commitWidth).not.toHaveBeenCalled();

    act(() => {
      handle?.dispatchEvent(
        new KeyboardEvent("keydown", { bubbles: true, key: "ArrowLeft" }),
      );
    });
    expect(railWidth.commitWidth).toHaveBeenCalledWith(320);
  });

  it("宽内容面按控制器宽度落地（461px = auto 的 0.32 × 1440 收口）", () => {
    mockState.surface = "files";
    const rail = render(controller({ widthClass: "wide", widthPx: 461 }));

    expect(rail?.style.getPropertyValue("--right-rail-width")).toBe("461px");
    expect(container.querySelector('[role="dialog"]')).toBeNull();
  });

  it("窄视口 + 宽内容面 → 覆盖层抽屉 + 遮罩 + Esc 关闭", () => {
    mockState.surface = "git";
    render(
      controller({
        widthClass: "wide",
        widthPx: 461,
        overlay: true,
        viewportWidth: 1100,
      }),
    );

    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog).not.toBeNull();
    expect((dialog as HTMLElement).style.getPropertyValue("--right-rail-width")).toBe(
      "461px",
    );
    expect(
      document.querySelector('[data-testid="right-rail-overlay-backdrop"]'),
    ).not.toBeNull();

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    });
    expect(onCloseRightRail).toHaveBeenCalledTimes(1);
  });

  it("窄视口但激活面未接线（兜底 content）→ 不进覆盖层，仍渲染容器与手柄", () => {
    const rail = render(
      controller({ widthPx: 288, overlay: true, viewportWidth: 1100 }),
    );

    expect(rail).not.toBeNull();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(
      container.querySelector('[data-testid="right-rail-resize-handle"]'),
    ).not.toBeNull();
  });

  it("关闭右栏仍整列不渲染（回归红线 ①）", () => {
    const rail = render(controller(), { rightRailOpen: false });

    expect(rail).toBeNull();
    expect(container.querySelector('[role="separator"]')).toBeNull();
  });
});
