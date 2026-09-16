// @vitest-environment jsdom
// P0-2：设置页「右侧栏宽度」区块（第三个宽度入口：滑块 + 当前值 + 恢复自适应）。
//
// 设计规则（§4.2.2）：
// - 区块只调用同一条出口（`lib/layout/rail-width.ts`）：滑块上下界随视口收窄，
//   写入值先过 `clampRailWidth()` 再落盘；
// - 拖动滑块即转 `manual`；「恢复自适应」回到 `auto` 且不动 `rightRailWidthPx`；
// - auto 模式下显示模式标签（实际宽度由当前面决定），manual 显示「手动 · N px」。
//
// 回归红线：区块不改右栏开合语义，只是 settings.workspace 的另一个写入口。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { SettingsProvider } from "@/core/settings";
import { APP_SETTINGS_STORAGE_KEY, mergeAppSettings } from "@/core/settings/local";
import { i18n } from "@/i18n";

import { RailWidthSection } from "./rail-width-section";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const VIEWPORT_WIDTH = 1600;

// i18next 的强类型 key 联合不适合测试里的动态取值；测试只断言「渲染值 == i18n 真源」。
const i18nT = i18n.t.bind(i18n) as unknown as (
  key: string,
  options?: Record<string, unknown>,
) => string;

function t(key: string, options?: Record<string, unknown>) {
  return i18nT(key, { ns: "workspace", ...options });
}

/** React 受控 input：走原生 setter + input 事件，绕过 value tracker。 */
function setRangeValue(input: HTMLInputElement, value: number) {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    "value",
  )?.set;
  setter?.call(input, String(value));
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("RailWidthSection", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  function render() {
    act(() => {
      root?.render(
        <SettingsProvider>
          <RailWidthSection />
        </SettingsProvider>,
      );
    });

    return {
      slider: container.querySelector<HTMLInputElement>('input[type="range"]'),
      reset: container.querySelector<HTMLButtonElement>(
        '[data-testid="rail-width-reset-auto"]',
      ),
      badge: container.querySelector<HTMLElement>(
        '[data-testid="rail-width-value"]',
      ),
    };
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
    Object.defineProperty(window, "innerWidth", {
      configurable: true,
      value: VIEWPORT_WIDTH,
    });
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
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

  it("默认 auto：显示「自适应」且「恢复自适应」不可用；滑块范围随视口收窄", () => {
    const { slider, reset, badge } = render();

    expect(badge?.textContent).toContain(
      t("panels.shell.rightRail.modeAuto"),
    );
    expect(reset?.disabled).toBe(true);
    // 1600 视口：上界 = min(832, 1600 − 256 − 512) = 832。
    expect(slider?.min).toBe("320");
    expect(slider?.max).toBe("832");
  });

  it("拖动滑块 → 落 manual，并写入过了 clamp 的宽度", () => {
    const { slider, badge } = render();

    act(() => {
      if (slider) {
        setRangeValue(slider, 600);
      }
    });

    expect(storedWorkspace()).toMatchObject({
      rightRailWidthMode: "manual",
      rightRailWidthPx: 600,
    });
    expect(badge?.textContent).toContain(
      t("panels.shell.rightRail.modeManual"),
    );
  });

  it("manual 状态点击「恢复自适应」→ 回 auto，保留宽度意图", () => {
    window.localStorage.setItem(
      APP_SETTINGS_STORAGE_KEY,
      JSON.stringify(
        mergeAppSettings({
          workspace: { rightRailWidthMode: "manual", rightRailWidthPx: 512 },
        }),
      ),
    );

    const { reset, badge } = render();
    expect(reset?.disabled).toBe(false);
    expect(badge?.textContent).toContain(
      t("panels.shell.rightRail.widthValue", { px: 512 }),
    );

    act(() => {
      reset?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(storedWorkspace()).toMatchObject({
      rightRailWidthMode: "auto",
      rightRailWidthPx: 512,
    });
    expect(
      container.querySelector('[data-testid="rail-width-reset-auto"]')
        ?.hasAttribute("disabled"),
    ).toBe(true);
  });
});
