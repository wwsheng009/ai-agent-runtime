import { expect, type Page, test } from "@playwright/test";

// P0-4 三层设计 token 验收：主题（亮 / 暗 / 跟随系统）× 强调色（cyan / violet）
// 的运行时装配，断言链路为「L2 语义变量 → L3 工具类 → 真实元素计算样式」，
// 不做像素比对（避免渲染抖动误报）。
//
// 断言不变量：
//  - html.dataset.theme 与 prefers-color-scheme / themeMode 一致；
//  - .app-chat-input（text-foreground）计算色 === var(--foreground) 解析色；
//  - 同一主题下切换强调色不得改变中性色（background/foreground/border/...）；
//  - 同一强调色不得随主题变化（强调色板主题无关）；
//  - 字号设置驱动角色 token（app-text / app-chat / app-code）缩放。

const SETTINGS_KEY = "ai-agent-runtime.workspace.settings";

const composer = (page: Page) => page.locator(".app-chat-input");

type ThemeMode = "light" | "dark" | "system";
type AccentTone = "cyan" | "violet";
type ResolvedTheme = "light" | "dark";

type Combo = {
  accentTone: AccentTone;
  colorScheme: "light" | "dark";
  expectTheme: ResolvedTheme;
  themeMode: ThemeMode;
};

// 三档主题 × 两种强调色。system 档显式模拟 prefers-color-scheme: dark，
// 与显式 dark 档的区别由 dataset.themeMode 断言兜住（证明 media 解析路径生效）。
const MATRIX: Combo[] = [
  { accentTone: "cyan", colorScheme: "light", expectTheme: "light", themeMode: "light" },
  { accentTone: "violet", colorScheme: "light", expectTheme: "light", themeMode: "light" },
  { accentTone: "cyan", colorScheme: "light", expectTheme: "dark", themeMode: "dark" },
  { accentTone: "violet", colorScheme: "light", expectTheme: "dark", themeMode: "dark" },
  { accentTone: "cyan", colorScheme: "dark", expectTheme: "dark", themeMode: "system" },
  { accentTone: "violet", colorScheme: "dark", expectTheme: "dark", themeMode: "system" },
];

async function seedAppearance(
  page: Page,
  appearance: Record<string, unknown>,
) {
  await page.evaluate(
    ({ key, patch }) => {
      const raw = window.localStorage.getItem(key);
      const current = raw ? (JSON.parse(raw) as Record<string, unknown>) : {};
      const merged = {
        ...current,
        appearance: {
          ...((current.appearance as Record<string, unknown> | undefined) ?? {}),
          ...patch,
        },
      };
      window.localStorage.setItem(key, JSON.stringify(merged));
    },
    { key: SETTINGS_KEY, patch: appearance },
  );
}

async function resetAppearance(page: Page) {
  await page.evaluate((key) => {
    window.localStorage.removeItem(key);
  }, SETTINGS_KEY);
}

async function loadWithSettings(
  page: Page,
  {
    colorScheme,
    appearance,
  }: { appearance?: Record<string, unknown>; colorScheme?: "light" | "dark" },
) {
  await page.emulateMedia({ colorScheme: colorScheme ?? null });
  if (appearance) {
    await seedAppearance(page, appearance);
  } else {
    await resetAppearance(page);
  }
  await page.reload();
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
}

type Snapshot = Awaited<ReturnType<typeof snapshot>>;

async function snapshot(page: Page) {
  return page.evaluate(() => {
    const root = document.documentElement;
    const rootStyle = getComputedStyle(root);

    // 用探针元素把 var(--x) 解析为浏览器的规范化色值（rgb/rgba）。
    const colorProbe = document.createElement("div");
    colorProbe.style.cssText =
      "position:absolute;visibility:hidden;pointer-events:none";
    document.body.appendChild(colorProbe);
    const resolveColor = (value: string) => {
      colorProbe.style.color = value;
      return getComputedStyle(colorProbe).color;
    };

    const tokens = {
      accentPrimary: resolveColor("var(--accent-primary)"),
      accentSecondary: resolveColor("var(--accent-secondary)"),
      background: resolveColor("var(--background)"),
      border: resolveColor("var(--border)"),
      foreground: resolveColor("var(--foreground)"),
      mutedForeground: resolveColor("var(--muted-foreground)"),
      surfaceSolid: resolveColor("var(--surface-solid)"),
    };
    const utilityAlias = {
      foreground: resolveColor("var(--color-foreground)"),
      accentPrimary: resolveColor("var(--color-accent-primary)"),
    };

    const appText13 = document.createElement("div");
    appText13.className = "app-text-13";
    const chatCopy = document.createElement("div");
    chatCopy.className = "app-chat-copy";
    const chatText13 = document.createElement("div");
    chatText13.className = "app-text-13";
    chatCopy.appendChild(chatText13);
    const codeSurface = document.createElement("div");
    codeSurface.className = "app-code-surface";
    const codeText12 = document.createElement("div");
    codeText12.className = "app-text-12";
    codeSurface.appendChild(codeText12);
    document.body.append(appText13, chatCopy, codeSurface);

    const input = document.querySelector(".app-chat-input");
    const inputStyle = input ? getComputedStyle(input) : null;

    const result = {
      accentTone: root.dataset.accentTone ?? null,
      computed: {
        appText13: getComputedStyle(appText13).fontSize,
        chatCopyText13: getComputedStyle(chatText13).fontSize,
        chatInputColor: inputStyle?.color ?? null,
        chatInputFontSize: inputStyle?.fontSize ?? null,
        codeText12: getComputedStyle(codeText12).fontSize,
      },
      fontVars: {
        chat: rootStyle.getPropertyValue("--app-chat-font-size").trim(),
        code: rootStyle.getPropertyValue("--app-code-font-size").trim(),
        codeLineNumber: rootStyle
          .getPropertyValue("--app-code-line-number-size")
          .trim(),
        root: rootStyle.getPropertyValue("--app-root-font-size").trim(),
      },
      rootColorScheme: rootStyle.colorScheme,
      theme: root.dataset.theme ?? null,
      themeMode: root.dataset.themeMode ?? null,
      tokens,
      utilityAlias,
    };

    colorProbe.remove();
    appText13.remove();
    chatCopy.remove();
    codeSurface.remove();
    return result;
  });
}

function parseColor(value: string) {
  const match = value.match(
    /rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:[,\s/]+([\d.]+))?\s*\)/,
  );
  if (!match) return null;
  return {
    alpha: match[4] === undefined ? 1 : Number.parseFloat(match[4]),
    b: Number.parseFloat(match[3]),
    g: Number.parseFloat(match[2]),
    r: Number.parseFloat(match[1]),
  };
}

function luminance(value: string) {
  const color = parseColor(value);
  if (!color) return null;
  const channel = (raw: number) => {
    const v = raw / 255;
    return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  };
  return (
    0.2126 * channel(color.r) +
    0.7152 * channel(color.g) +
    0.0722 * channel(color.b)
  );
}

function expectColor(value: string) {
  expect(value, `期望解析出颜色，实际为 ${value}`).toMatch(/^rgba?\(/);
}

async function expectTokenWiring(page: Page, snap: Snapshot) {
  await expect(composer(page)).toBeVisible();
  expect(snap.computed.chatInputColor).not.toBeNull();
  // text-foreground → --foreground，且 @theme 别名 --color-foreground 与之一致。
  expect(snap.computed.chatInputColor).toBe(snap.tokens.foreground);
  expect(snap.utilityAlias.foreground).toBe(snap.tokens.foreground);
  expect(snap.utilityAlias.accentPrimary).toBe(snap.tokens.accentPrimary);
}

test.describe("P0-4 三层设计 token：主题 × 强调色 × 字号", () => {
  test.beforeEach(async ({ page }) => {
    await page.request.post("/api/_test/reset");
    await page.goto("/workspace");
    await expect(composer(page)).toBeVisible({ timeout: 30_000 });
  });

  test("三档主题 × 两种强调色切换后 token 装配无颜色回归", async ({ page }) => {
    const results: Array<{ combo: Combo; snap: Snapshot }> = [];

    for (const combo of MATRIX) {
      await loadWithSettings(page, {
        appearance: {
          accentTone: combo.accentTone,
          themeMode: combo.themeMode,
        },
        colorScheme: combo.colorScheme,
      });
      const snap = await snapshot(page);

      expect(snap.theme, `${combo.themeMode}/${combo.accentTone} data-theme`).toBe(
        combo.expectTheme,
      );
      expect(snap.themeMode).toBe(combo.themeMode);
      expect(snap.accentTone).toBe(combo.accentTone);
      expect(snap.rootColorScheme).toBe(combo.expectTheme);

      for (const [name, value] of Object.entries(snap.tokens)) {
        expectColor(value);
        expect(value, `${name} 不应为空`).not.toBe("");
      }

      const bg = luminance(snap.tokens.background);
      const fg = luminance(snap.tokens.foreground);
      expect(bg, "background 需可解析为颜色").not.toBeNull();
      expect(fg, "foreground 需可解析为颜色").not.toBeNull();
      if (combo.expectTheme === "dark") {
        expect(fg! - bg!, "暗色主题前景应亮于背景").toBeGreaterThan(0.15);
      } else {
        expect(bg! - fg!, "亮色主题前景应暗于背景").toBeGreaterThan(0.15);
      }

      await expectTokenWiring(page, snap);
      results.push({ combo, snap });
    }

    const snapAt = (themeMode: ThemeMode, accentTone: AccentTone) => {
      const hit = results.find(
        (item) =>
          item.combo.themeMode === themeMode &&
          item.combo.accentTone === accentTone,
      );
      expect(hit, `矩阵缺少 ${themeMode}/${accentTone}`).toBeTruthy();
      return hit!.snap;
    };

    const neutralKeys: Array<keyof Snapshot["tokens"]> = [
      "background",
      "foreground",
      "mutedForeground",
      "border",
      "surfaceSolid",
    ];

    // 1) 主题切换必须改变中性色基色。
    expect(snapAt("light", "cyan").tokens.background).not.toBe(
      snapAt("dark", "cyan").tokens.background,
    );
    expect(snapAt("light", "cyan").tokens.foreground).not.toBe(
      snapAt("dark", "cyan").tokens.foreground,
    );

    // 2) 同主题内切换强调色不得扰动任何中性色。
    for (const themeMode of ["light", "dark", "system"] as const) {
      const cyan = snapAt(themeMode, "cyan");
      const violet = snapAt(themeMode, "violet");
      for (const key of neutralKeys) {
        expect(
          cyan.tokens[key],
          `${themeMode} 下 ${key} 不应随强调色变化`,
        ).toBe(violet.tokens[key]);
      }
    }

    // 3) 强调色板与主题无关；cyan / violet 的主次色互不相同。
    for (const accentTone of ["cyan", "violet"] as const) {
      const light = snapAt("light", accentTone).tokens;
      const dark = snapAt("dark", accentTone).tokens;
      const system = snapAt("system", accentTone).tokens;
      expect(light.accentPrimary).toBe(dark.accentPrimary);
      expect(light.accentPrimary).toBe(system.accentPrimary);
      expect(light.accentSecondary).toBe(dark.accentSecondary);
    }
    expect(snapAt("light", "cyan").tokens.accentPrimary).not.toBe(
      snapAt("light", "violet").tokens.accentPrimary,
    );
    expect(snapAt("light", "cyan").tokens.accentSecondary).not.toBe(
      snapAt("light", "violet").tokens.accentSecondary,
    );

    // 4) 默认强调色（gold）作为第四条基线存在且不与 cyan / violet 混淆。
    await loadWithSettings(page, { colorScheme: "dark" });
    const defaultSnap = await snapshot(page);
    expect(defaultSnap.accentTone).toBe("gold");
    expect(defaultSnap.tokens.accentPrimary).not.toBe(
      snapAt("system", "cyan").tokens.accentPrimary,
    );
    expect(defaultSnap.tokens.accentPrimary).not.toBe(
      snapAt("system", "violet").tokens.accentPrimary,
    );
    expect(defaultSnap.tokens.background).toBe(
      snapAt("system", "cyan").tokens.background,
    );
    await expectTokenWiring(page, defaultSnap);
  });

  test("跟随系统主题按 prefers-color-scheme 解析 data-theme", async ({ page }) => {
    await loadWithSettings(page, {
      appearance: { accentTone: "violet", themeMode: "system" },
      colorScheme: "dark",
    });
    const dark = await snapshot(page);
    expect(dark.theme).toBe("dark");
    expect(dark.themeMode).toBe("system");
    expect(dark.rootColorScheme).toBe("dark");

    await loadWithSettings(page, {
      appearance: { accentTone: "violet", themeMode: "system" },
      colorScheme: "light",
    });
    const light = await snapshot(page);
    expect(light.theme).toBe("light");
    expect(light.themeMode).toBe("system");
    expect(light.rootColorScheme).toBe("light");
    expect(light.tokens.background).not.toBe(dark.tokens.background);
    expect(light.tokens.foreground).not.toBe(dark.tokens.foreground);

    // 强调色不随系统主题解析结果变化。
    expect(light.tokens.accentPrimary).toBe(dark.tokens.accentPrimary);
    await expectTokenWiring(page, light);
  });

  test("字号设置驱动 app-text / app-chat / app-code 角色 token 缩放", async ({
    page,
  }) => {
    const baseAppearance = { accentTone: "cyan", themeMode: "dark" } as const;

    await loadWithSettings(page, {
      appearance: {
        ...baseAppearance,
        chatTextSize: 17,
        codeTextSize: 14,
        textSize: 18,
      },
      colorScheme: "light",
    });
    const large = await snapshot(page);

    expect(large.fontVars.root).toBe("18px");
    expect(large.fontVars.chat).toBe("17px");
    expect(large.fontVars.code).toBe("14px");
    // getCodeLineNumberFontSize = max(10, codeTextSize - 1)
    expect(large.fontVars.codeLineNumber).toBe("13px");
    // L3 角色字号 = 基准 × 比例（app-text-13 = 0.8125）。
    expect(Number.parseFloat(large.computed.appText13)).toBeCloseTo(
      18 * 0.8125,
      2,
    );
    expect(Number.parseFloat(large.computed.codeText12)).toBeCloseTo(
      14 * 0.9230769,
      2,
    );
    // 聊天角色字号：消息正文按 --app-chat-font-size 缩放（0.8666667 = app-text-13）。
    expect(Number.parseFloat(large.computed.chatCopyText13)).toBeCloseTo(
      17 * 0.8666667,
      2,
    );
    // 既有行为（自初始提交 aa42c0ff 起，非 P0-4 引入）：未分层规则
    // `button, input, textarea, select { font: inherit }` 优先于 @layer base 内的
    // .app-chat-input 字号规则，故输入框字号跟随 --app-root-font-size，
    // 而非 --app-chat-font-size。此处固化现状：级联关系一旦变化即失败。
    expect(large.computed.chatInputFontSize).not.toBeNull();
    expect(Number.parseFloat(large.computed.chatInputFontSize!)).toBeCloseTo(
      18,
      2,
    );

    await loadWithSettings(page, {
      appearance: {
        ...baseAppearance,
        chatTextSize: 15,
        codeTextSize: 13,
        textSize: 16,
      },
      colorScheme: "light",
    });
    const base = await snapshot(page);

    expect(base.fontVars.root).toBe("16px");
    expect(base.fontVars.chat).toBe("15px");
    expect(base.fontVars.code).toBe("13px");
    expect(base.fontVars.codeLineNumber).toBe("12px");
    expect(Number.parseFloat(base.computed.appText13)).toBeCloseTo(13, 2);
    expect(Number.parseFloat(base.computed.codeText12)).toBeCloseTo(12, 2);
    expect(Number.parseFloat(base.computed.chatCopyText13)).toBeCloseTo(
      15 * 0.8666667,
      2,
    );
    expect(Number.parseFloat(base.computed.chatInputFontSize!)).toBeCloseTo(
      16,
      2,
    );
    // 缩放单调性：调大基准字号后角色字号必须变大（防「变量未接线」回归）。
    expect(Number.parseFloat(large.computed.appText13)).toBeGreaterThan(
      Number.parseFloat(base.computed.appText13),
    );
    expect(Number.parseFloat(large.computed.codeText12)).toBeGreaterThan(
      Number.parseFloat(base.computed.codeText12),
    );
    expect(base.computed.chatInputColor).toBe(base.tokens.foreground);
  });
});
