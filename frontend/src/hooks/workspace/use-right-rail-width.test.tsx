// @vitest-environment jsdom
// P0-2：右栏宽度状态源（useRightRailWidth）的行为测试。
//
// 设计规则（§4.2）：
// - auto：内容型面（artifacts/plan/checkpoints/usage）= 288px（与改造前逐像素一致，回归红线 ④）；
//   宽内容面（files/git）= clamp(0.32 × vw, 416, 672)。
// - manual：持久化值再过 clampRailWidth()；视口变小只做「显示收窄」，**不回写设置**。
// - commitWidth / resetToAuto 是唯一写入口：commit 落 manual，reset 落 auto（保留宽度意图）。
//
// 回归红线：未接线激活面（null / 未注册 id）按内容型面兜底 288px，UI 不白屏。

import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { type WorkspacePanelSurfaceId } from "@/components/workspace/panel-registry";
import { SettingsProvider } from "@/core/settings";
import {
  APP_SETTINGS_STORAGE_KEY,
  mergeAppSettings,
} from "@/core/settings/local";

import {
  resolveRailSurfaceWidthClass,
  useRightRailWidth,
  type RightRailWidthController,
} from "./use-right-rail-width";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const WIDE_VIEWPORT = 1440;

describe("useRightRailWidth", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let controller: RightRailWidthController | null;

  function Probe({
    onReady,
    surface,
    viewportWidth,
  }: {
    onReady: (next: RightRailWidthController) => void;
    surface: WorkspacePanelSurfaceId | null;
    viewportWidth: number;
  }) {
    const current = useRightRailWidth({ surface, viewportWidth });
    useEffect(() => {
      onReady(current);
    }, [current, onReady]);
    return null;
  }

  function render(
    surface: WorkspacePanelSurfaceId | null,
    viewportWidth = WIDE_VIEWPORT,
  ) {
    act(() => {
      root?.render(
        <SettingsProvider>
          <Probe
            onReady={(next) => {
              controller = next;
            }}
            surface={surface}
            viewportWidth={viewportWidth}
          />
        </SettingsProvider>,
      );
    });
    return controller as unknown as RightRailWidthController;
  }

  function storedWorkspace() {
    const raw = window.localStorage.getItem(APP_SETTINGS_STORAGE_KEY);
    return (
      JSON.parse(raw ?? "{}") as {
        workspace?: { rightRailWidthMode?: string; rightRailWidthPx?: number };
      }
    ).workspace;
  }

  beforeEach(() => {
    window.localStorage.clear();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    controller = null;
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("auto + 内容型面恒为 288px（回归红线 ④）", () => {
    for (const surface of ["artifacts", "plan", "checkpoints", "usage"] as const) {
      const current = render(surface);

      expect(current.mode).toBe("auto");
      expect(current.widthClass).toBe("content");
      expect(current.widthPx).toBe(288);
      expect(current.minWidthPx).toBe(320);
      expect(current.maxWidthPx).toBe(672);
      expect(current.overlay).toBe(false);
    }
  });

  it("auto + 宽内容面按视口：clamp(0.32 × vw, 416, 672)", () => {
    const wide = render("files");
    // 0.32 × 1440 = 460.8 → 461（未触及上下界）。
    expect(wide.widthClass).toBe("wide");
    expect(wide.widthPx).toBe(461);

    const git = render("git", 1920);
    // 0.32 × 1920 = 614.4 → 614；主区保护上界 min(832, 1152) = 832。
    expect(git.widthPx).toBe(614);

    const narrow = render("files", 1280);
    // 0.32 × 1280 = 409.6 → 410 → 抬到 416；上界 min(832, 512) = 512。
    expect(narrow.widthPx).toBe(416);
  });

  it("manual 用持久化值并过 clamp；视口变小只显示收窄，不回写设置", () => {
    window.localStorage.setItem(
      APP_SETTINGS_STORAGE_KEY,
      JSON.stringify(
        mergeAppSettings({
          workspace: { rightRailWidthMode: "manual", rightRailWidthPx: 500 },
        }),
      ),
    );

    const wide = render(null, WIDE_VIEWPORT);
    expect(wide.mode).toBe("manual");
    expect(wide.widthPx).toBe(500);

    // 视口收窄到 1000：上界 = max(320, min(832, 1000 − 768)) = 320。
    const narrow = render(null, 1000);
    expect(narrow.widthPx).toBe(320);
    expect(narrow.overlay).toBe(true);
    // 关键：持久化意图仍是 500（视口恢复后宽度回来），设置没有被拖拽以外的路径改写。
    expect(storedWorkspace()?.rightRailWidthPx).toBe(500);
    expect(storedWorkspace()?.rightRailWidthMode).toBe("manual");

    const restored = render(null, WIDE_VIEWPORT);
    expect(restored.widthPx).toBe(500);
  });

  it("commitWidth 落 manual 并 clamp 到主区保护上界", () => {
    const current = render("git");
    expect(current.mode).toBe("auto");

    act(() => current.commitWidth(900));

    expect(controller?.mode).toBe("manual");
    // 900 越界 → min(832, 1440 − 768) = 672。
    expect(controller?.widthPx).toBe(672);
    expect(storedWorkspace()).toMatchObject({
      rightRailWidthMode: "manual",
      rightRailWidthPx: 672,
    });
  });

  it("resetToAuto 回到自适应宽度，并保留宽度意图", () => {
    window.localStorage.setItem(
      APP_SETTINGS_STORAGE_KEY,
      JSON.stringify(
        mergeAppSettings({
          workspace: { rightRailWidthMode: "manual", rightRailWidthPx: 512 },
        }),
      ),
    );

    const manual = render("usage");
    expect(manual.widthPx).toBe(512);

    act(() => manual.resetToAuto());

    expect(controller?.mode).toBe("auto");
    expect(controller?.widthPx).toBe(288);
    expect(storedWorkspace()?.rightRailWidthPx).toBe(512);
  });

  it("激活面未接线 / 未注册时按内容型面兜底（不白屏）", () => {
    expect(resolveRailSurfaceWidthClass(null)).toBe("content");
    expect(resolveRailSurfaceWidthClass(undefined)).toBe("content");
    expect(resolveRailSurfaceWidthClass("files")).toBe("wide");
    expect(resolveRailSurfaceWidthClass("git")).toBe("wide");
    expect(
      resolveRailSurfaceWidthClass("unknown-surface" as WorkspacePanelSurfaceId),
    ).toBe("content");

    const current = render(null);
    expect(current.widthPx).toBe(288);
  });

  it("低于 xl 断点或主区放不下时标记 overlay（由调用方降级为抽屉）", () => {
    expect(render("artifacts", 1279).overlay).toBe(true);
    expect(render("artifacts", 1280).overlay).toBe(false);
    // 主区保护：1088 − 768 = 320 = min → 仍可进网格；1087 → 319 < 320 → 覆盖层。
    expect(render("files", 1087).overlay).toBe(true);
  });
});
