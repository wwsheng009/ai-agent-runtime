// @vitest-environment node

// 连接状态徽标的对比度回归（P1-8 补丁）：
// 历史缺陷——色调写死 `bg-*-500/12 + text-*-200`，只在暗色主题成立；亮色主题下
// 「连接中… 重试」的浅色前景压在近乎同色的浅色底上，肉眼无法区分。
// 本测试直接解析真实的 token 样式表（tokens.css 暗色基准 / themes.css 亮色覆盖 /
// theme.css Tailwind 映射），按 WCAG 相对亮度公式断言「状态前景 vs 合成背景」
// 在两套主题、各承接表面上均 ≥ 4.5:1，避免同类回归再次静默进入。

/// <reference types="node" />

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

function readGlobalsFile(name: string) {
  return readFileSync(
    fileURLToPath(new URL(`../styles/globals/${name}`, import.meta.url)),
    "utf8",
  );
}

const CONNECTION_STATES = [
  "online",
  "connecting",
  "reconnecting",
  "offline",
] as const;

/** 徽标可能落座的表面：亮色下 `--surface-strong` / 顶栏即接近纯白的最坏情况。 */
const SURFACES = ["background", "surface-strong", "workspace-topbar-bg"] as const;

const MIN_CONTRAST = 4.5;

type Rgba = { a: number; b: number; g: number; r: number };

const COLOR_PATTERNS = {
  hex: /^#([0-9a-f]{3}|[0-9a-f]{6})$/i,
  rgba:
    /^rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)(?:\s*,\s*([\d.]+))?\s*\)$/i,
} as const;

/** 提取 `selector { ... }` 块内所有 `--name: value` 声明（后出现的同名声明覆盖先出现的）。 */
function declarationsIn(css: string, selector: string): Map<string, string> {
  const source = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const blockPattern = new RegExp(
    `${selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*\\{([^}]*)\\}`,
    "g",
  );
  const declarations = new Map<string, string>();

  for (const block of source.matchAll(blockPattern)) {
    for (const declaration of block[1].matchAll(/(--[\w-]+)\s*:\s*([^;]+)/g)) {
      declarations.set(declaration[1], declaration[2].trim());
    }
  }

  return declarations;
}

function parseColor(value: string): Rgba | null {
  const hex = COLOR_PATTERNS.hex.exec(value);
  if (hex) {
    const digits = hex[1];
    const expanded =
      digits.length === 3
        ? digits
            .split("")
            .map((digit) => digit + digit)
            .join("")
        : digits;

    return {
      a: 1,
      b: Number.parseInt(expanded.slice(4, 6), 16),
      g: Number.parseInt(expanded.slice(2, 4), 16),
      r: Number.parseInt(expanded.slice(0, 2), 16),
    };
  }

  const rgba = COLOR_PATTERNS.rgba.exec(value);
  if (!rgba) {
    return null;
  }

  return {
    a: rgba[4] === undefined ? 1 : Number.parseFloat(rgba[4]),
    b: Number.parseFloat(rgba[3]),
    g: Number.parseFloat(rgba[2]),
    r: Number.parseFloat(rgba[1]),
  };
}

function createResolver(vars: Map<string, string>) {
  return function resolve(name: string, trail: string[] = []): Rgba {
    if (trail.includes(name)) {
      throw new Error(`循环变量引用：${[...trail, name].join(" → ")}`);
    }

    const raw = vars.get(`--${name}`);
    if (raw === undefined) {
      throw new Error(`token 缺失：--${name}`);
    }

    const reference = /^var\(\s*--([\w-]+)\s*\)$/.exec(raw);
    if (reference) {
      return resolve(reference[1], [...trail, name]);
    }

    const color = parseColor(raw);
    if (!color) {
      throw new Error(`--${name} 不是颜色：${raw}`);
    }

    return color;
  };
}

function composite(foreground: Rgba, background: Rgba): Rgba {
  return {
    a: 1,
    b: foreground.b * foreground.a + background.b * (1 - foreground.a),
    g: foreground.g * foreground.a + background.g * (1 - foreground.a),
    r: foreground.r * foreground.a + background.r * (1 - foreground.a),
  };
}

function relativeLuminance({ b, g, r }: Rgba) {
  const channel = (value: number) => {
    const scaled = value / 255;
    return scaled <= 0.04045
      ? scaled / 12.92
      : ((scaled + 0.055) / 1.055) ** 2.4;
  };

  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

function contrastRatio(first: Rgba, second: Rgba) {
  const [lighter, darker] = [
    relativeLuminance(first),
    relativeLuminance(second),
  ].sort((a, b) => b - a);

  return (lighter + 0.05) / (darker + 0.05);
}

function format({ b, g, r }: Rgba) {
  return `rgb(${Math.round(r)} ${Math.round(g)} ${Math.round(b)})`;
}

const tokensCss = readGlobalsFile("tokens.css");
const themesCss = readGlobalsFile("themes.css");
const themeCss = readGlobalsFile("theme.css");

const darkVars = declarationsIn(tokensCss, ":root");
const lightOverrideVars = declarationsIn(themesCss, 'html[data-theme="light"]');
const tailwindVars = declarationsIn(themeCss, "@theme inline");

describe("connection status token wiring", () => {
  it("maps every connection token into Tailwind (@theme inline)", () => {
    for (const state of CONNECTION_STATES) {
      for (const suffix of ["", "-soft", "-border"]) {
        const name = `--connection-${state}${suffix}`;

        expect(darkVars.has(name), `${name} 缺少暗色基准定义`).toBe(true);
        expect(lightOverrideVars.has(name), `${name} 缺少亮色覆盖`).toBe(true);
        expect(tailwindVars.get(`--color-${name.slice(2)}`)).toBe(`var(${name})`);
      }
    }
  });
});

describe.each([
  { overrides: new Map<string, string>(), theme: "dark" },
  { overrides: lightOverrideVars, theme: "light" },
])("connection status contrast in the $theme theme", ({ overrides, theme }) => {
  const vars = new Map([...darkVars, ...overrides]);
  const resolve = createResolver(vars);
  const resolveOrNull = (name: string) => {
    try {
      return resolve(name);
    } catch {
      return null;
    }
  };

  it.each(CONNECTION_STATES)("%s stays readable on every surface", (state) => {
    const foreground = resolve(`connection-${state}`);
    const soft = resolve(`connection-${state}-soft`);
    const border = resolve(`connection-${state}-border`);

    // 底色与描边必须真的带透明度，否则会盖掉表面层级（历史样式为 /12 与 /30）。
    expect(soft.a).toBeGreaterThan(0);
    expect(soft.a).toBeLessThan(0.5);
    expect(border.a).toBeGreaterThan(soft.a);

    for (const surface of SURFACES) {
      const base = resolveOrNull(surface);
      const page = resolveOrNull("background");
      if (!base || !page) {
        continue;
      }

      // 半透明表面（顶栏 / surface-strong）先落在页面底色上，再叠状态色。
      const background = composite(soft, composite(base, page));
      const ratio = contrastRatio(foreground, background);

      expect(
        ratio,
        `${theme}/${state} 在 --${surface} 上对比度 ${ratio.toFixed(2)}:1`,
      ).toBeGreaterThanOrEqual(MIN_CONTRAST);
      expect(format(foreground)).not.toBe(format(background));
    }
  });
});
