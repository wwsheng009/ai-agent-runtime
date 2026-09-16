// @vitest-environment node

// 差异行文字对比度回归（「绿字压浅绿底」修复）：
// 历史缺陷——新增 / 删除行的**代码文本**直接复用半透明 `-accent`（左轨 / 边框色）当文字色，
// 与同色系的 `-bg` 行底色亮度接近，肉眼看不出差异；亮色主题下 accent 只有 24% alpha，几乎糊在色底里。
//
// 本测试解析真实 token 样式表（tokens.css 暗色基准 / themes.css 亮色覆盖 / theme.css Tailwind 映射），
// 按 WCAG 相对亮度断言两套主题下「差异文字色 vs 合成后的行底色」≥ 4.5:1，并钉住
// 「文字色必须是实体色，不得复用半透明 accent」这条口径，避免同类回归静默进入。

/// <reference types="node" />

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

function readGlobalsFile(name: string) {
  return readFileSync(
    fileURLToPath(new URL(`../../styles/globals/${name}`, import.meta.url)),
    "utf8",
  );
}

const KINDS = ["inserted", "deleted"] as const;
/** 差异正文可能落座的表面：深色面板 / 近白浮层两端都要成立。 */
const SURFACES = ["background", "surface-strong", "panel-strong-bg"] as const;
const MIN_CONTRAST = 4.5;

type Rgba = { a: number; b: number; g: number; r: number };

const COLOR_PATTERNS = {
  hex: /^#([0-9a-f]{3}|[0-9a-f]{6})$/i,
  rgba: /^rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)(?:\s*,\s*([\d.]+))?\s*\)$/i,
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
    return scaled <= 0.04045 ? scaled / 12.92 : ((scaled + 0.055) / 1.055) ** 2.4;
  };

  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

function contrastRatio(first: Rgba, second: Rgba) {
  const [lighter, darker] = [relativeLuminance(first), relativeLuminance(second)].sort(
    (a, b) => b - a,
  );

  return (lighter + 0.05) / (darker + 0.05);
}

function format({ b, g, r }: Rgba) {
  return `rgb(${Math.round(r)} ${Math.round(g)} ${Math.round(b)})`;
}

const darkVars = declarationsIn(readGlobalsFile("tokens.css"), ":root");
const lightVars = declarationsIn(readGlobalsFile("themes.css"), 'html[data-theme="light"]');
const tailwindVars = declarationsIn(readGlobalsFile("theme.css"), "@theme inline");

describe("差异行文字 token 接线", () => {
  it("两套主题都定义 fg，并映射进 Tailwind", () => {
    for (const kind of KINDS) {
      const name = `--code-line-${kind}-fg`;

      expect(darkVars.has(name), `${name} 缺少暗色基准定义`).toBe(true);
      expect(lightVars.has(name), `${name} 缺少亮色覆盖`).toBe(true);
      expect(tailwindVars.get(`--color-code-line-${kind}-fg`)).toBe(`var(${name})`);
    }
  });
});

describe.each([
  { overrides: new Map<string, string>(), theme: "dark" },
  { overrides: lightVars, theme: "light" },
])("差异行文字对比度（$theme）", ({ overrides, theme }) => {
  const vars = new Map([...darkVars, ...overrides]);
  const resolve = createResolver(vars);

  it.each(KINDS)("%s 行的文字压在行底色上仍然可读", (kind) => {
    const foreground = resolve(`code-line-${kind}-fg`);
    const rowTint = resolve(`code-line-${kind}-bg`);
    const page = resolve("background");
    // 行内代码文本的口径是 `--foreground`（底色只表达增删，文字不跟着变色）。
    const codeText = resolve("foreground");

    // 文字色必须是实体色：半透明 accent 一旦被当文字色，同色系的 `-bg` 就会透上来（历史缺陷根因）。
    expect(foreground.a, `${theme}/${kind} 文字色不得带透明度`).toBe(1);
    // 行底色必须是低透明度着色，否则会盖掉表面层级。
    expect(rowTint.a).toBeGreaterThan(0);
    expect(rowTint.a).toBeLessThan(0.5);

    for (const surface of SURFACES) {
      const background = composite(rowTint, composite(resolve(surface), page));
      const markerRatio = contrastRatio(foreground, background);
      const codeRatio = contrastRatio(codeText, background);

      expect(
        markerRatio,
        `${theme}/${kind} 标记色在 --${surface} 上对比度 ${markerRatio.toFixed(2)}:1`,
      ).toBeGreaterThanOrEqual(MIN_CONTRAST);
      expect(
        codeRatio,
        `${theme}/${kind} 代码文本在 --${surface} 上对比度 ${codeRatio.toFixed(2)}:1`,
      ).toBeGreaterThanOrEqual(MIN_CONTRAST);
      expect(format(foreground)).not.toBe(format(background));
    }
  });
});
