import { describe, expect, it } from "vitest";

import {
  CODE_FONT_FAMILY_STACKS,
  FONT_FAMILY_STACKS,
  mergeAppSettings,
} from "@/core/settings/local";
import { resolveThemeSnapshot } from "@/core/theme";

describe("resolveThemeSnapshot", () => {
  it("resolves the system theme against the OS preference", () => {
    const settings = mergeAppSettings({ appearance: { themeMode: "system" } });

    expect(resolveThemeSnapshot(settings, "dark", "zh-CN").theme).toBe("dark");
    expect(resolveThemeSnapshot(settings, "light", "zh-CN").theme).toBe(
      "light",
    );
  });

  it("keeps an explicit theme regardless of the OS preference", () => {
    for (const themeMode of ["light", "dark"] as const) {
      const settings = mergeAppSettings({ appearance: { themeMode } });

      for (const systemTheme of ["light", "dark"] as const) {
        const snapshot = resolveThemeSnapshot(settings, systemTheme, "en-US");

        expect(snapshot.theme).toBe(themeMode);
        expect(snapshot.themeMode).toBe(themeMode);
      }
    }
  });

  it("maps appearance and workspace settings to DOM-ready values", () => {
    const settings = mergeAppSettings({
      appearance: {
        accentTone: "violet",
        chatTextSize: 17,
        codeFontFamily: "cascadia",
        codeTextSize: 14,
        fontFamily: "editorial",
        reducedMotion: true,
        textSize: 18,
      },
      workspace: { density: "comfortable" },
    });

    const snapshot = resolveThemeSnapshot(settings, "light", "en-US");

    expect(snapshot).toEqual({
      accentTone: "violet",
      density: "comfortable",
      fontFamilies: {
        mono: CODE_FONT_FAMILY_STACKS.cascadia,
        sans: FONT_FAMILY_STACKS.editorial.sans,
        serif: FONT_FAMILY_STACKS.editorial.serif,
      },
      fontSizes: {
        chat: "17px",
        code: "14px",
        codeLineNumber: "13px",
        root: "18px",
      },
      locale: "en-US",
      reducedMotion: true,
      theme: "light",
      themeMode: "system",
    });
  });

  it("clamps font sizes and derives the line-number size", () => {
    const settings = mergeAppSettings({
      appearance: { codeTextSize: 11, textSize: 40 },
    });

    const snapshot = resolveThemeSnapshot(settings, "dark", "zh-CN");

    expect(snapshot.fontSizes.root).toBe("24px");
    expect(snapshot.fontSizes.code).toBe("11px");
    expect(snapshot.fontSizes.codeLineNumber).toBe("10px");
  });

  it("produces a detached, immutable-by-convention snapshot", () => {
    const settings = mergeAppSettings({ appearance: { accentTone: "cyan" } });
    const snapshot = resolveThemeSnapshot(settings, "dark", "zh-CN");

    expect(snapshot.accentTone).toBe("cyan");
    expect(Object.keys(snapshot).sort()).toEqual([
      "accentTone",
      "density",
      "fontFamilies",
      "fontSizes",
      "locale",
      "reducedMotion",
      "theme",
      "themeMode",
    ]);
  });
});
