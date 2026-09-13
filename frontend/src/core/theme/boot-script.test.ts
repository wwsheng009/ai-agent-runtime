import { afterEach, describe, expect, it } from "vitest";

import indexHtml from "../../../index.html?raw";
import { getSystemTheme } from "@/core/settings/document";
import { APP_SETTINGS_STORAGE_KEY, getStoredAppSettings } from "@/core/settings/local";
import {
  buildThemeBootScript,
  createThemePresenter,
  resolveThemeSnapshot,
  THEME_BOOT_ATTRIBUTE,
  THEME_BOOT_PLACEHOLDER,
} from "@/core/theme";
import { resolveLocalePreference } from "@/i18n/locale";

// 执行 index.html 中注入的同一份启动脚本源码（而不是复刻实现），用于等价性验证。
const runBootScript = new Function(buildThemeBootScript()) as () => void;

interface BootCase {
  language?: string;
  name: string;
  rawStored?: string;
  stored?: unknown;
  systemDark: boolean;
}

const BOOT_CASES: BootCase[] = [
  { name: "no stored settings (dark system)", systemDark: true },
  { name: "no stored settings (light system)", systemDark: false },
  {
    name: "fully customized dark settings",
    stored: {
      appearance: {
        accentTone: "violet",
        chatTextSize: 16,
        codeFontFamily: "cascadia",
        codeTextSize: 15,
        fontFamily: "editorial",
        reducedMotion: true,
        textSize: 18,
        themeMode: "dark",
      },
      localization: { locale: "en-US" },
      workspace: { density: "comfortable" },
    },
    systemDark: false,
  },
  {
    name: "system theme with a light OS preference",
    stored: { appearance: { accentTone: "cyan", themeMode: "system" } },
    systemDark: false,
  },
  {
    name: "legacy string presets",
    stored: {
      appearance: { chatTextSize: "sm", codeTextSize: "md", textSize: "lg" },
    },
    systemDark: true,
  },
  {
    name: "out-of-range sizes are clamped",
    stored: {
      appearance: { chatTextSize: 3, codeTextSize: 11, textSize: 40 },
    },
    systemDark: true,
  },
  {
    name: "invalid values fall back to defaults",
    stored: {
      appearance: {
        accentTone: "red",
        chatTextSize: "abc",
        codeFontFamily: "nope",
        codeTextSize: 17.6,
        fontFamily: "nope",
        reducedMotion: "yes",
        themeMode: "blue",
      },
      localization: { locale: "fr" },
      workspace: { density: "dense" },
    },
    systemDark: false,
  },
  {
    name: "prototype-chain keys are not treated as presets",
    stored: { appearance: { chatTextSize: "constructor", textSize: "toString" } },
    systemDark: true,
  },
  {
    name: "corrupt stored JSON falls back to defaults",
    rawStored: "{not-json",
    systemDark: true,
  },
  {
    name: "system locale follows a Chinese browser language",
    language: "zh-CN",
    stored: { appearance: { themeMode: "light" } },
    systemDark: true,
  },
  {
    name: "system locale follows a non-Chinese browser language",
    language: "de-DE",
    stored: {},
    systemDark: true,
  },
];

const originalLanguageDescriptor = Object.getOwnPropertyDescriptor(
  window.navigator,
  "language",
);
const originalMatchMedia = (window as { matchMedia?: typeof window.matchMedia })
  .matchMedia;

function stubMatchMedia(matches: boolean) {
  (window as { matchMedia?: typeof window.matchMedia }).matchMedia = ((
    query: string,
  ) => ({
    addEventListener: () => {},
    addListener: () => {},
    dispatchEvent: () => false,
    matches,
    media: query,
    onchange: null,
    removeEventListener: () => {},
    removeListener: () => {},
  })) as unknown as typeof window.matchMedia;
}

function stubLanguage(language?: string) {
  if (language === undefined) {
    if (originalLanguageDescriptor) {
      Object.defineProperty(
        window.navigator,
        "language",
        originalLanguageDescriptor,
      );
    } else {
      delete (window.navigator as unknown as Record<string, unknown>).language;
    }
    return;
  }

  Object.defineProperty(window.navigator, "language", {
    configurable: true,
    get: () => language,
  });
}

// 只比较启动脚本/present 会写的表面：data-*、style、lang。
// 其中 style 用整段字符串比较，多写一个属性也会被发现。
function readSurface(root: HTMLElement) {
  const attributes: Record<string, string> = {};
  for (const { name, value } of Array.from(root.attributes)) {
    if (name.startsWith("data-") || name === "style" || name === "lang") {
      attributes[name] = value;
    }
  }
  return attributes;
}

function resetDocumentSurface() {
  const root = document.documentElement;
  for (const { name } of Array.from(root.attributes)) {
    if (name.startsWith("data-") || name === "style" || name === "lang") {
      root.removeAttribute(name);
    }
  }
}

function readReferenceSurface() {
  const settings = getStoredAppSettings(window.localStorage);
  const root = document.createElement("div");

  createThemePresenter(root).present(
    resolveThemeSnapshot(
      settings,
      getSystemTheme(),
      resolveLocalePreference(settings.localization.locale),
    ),
  );

  return readSurface(root);
}

function seedStorage(bootCase: BootCase) {
  window.localStorage.clear();
  if (bootCase.rawStored !== undefined) {
    window.localStorage.setItem(APP_SETTINGS_STORAGE_KEY, bootCase.rawStored);
    return;
  }
  if (bootCase.stored !== undefined) {
    window.localStorage.setItem(
      APP_SETTINGS_STORAGE_KEY,
      JSON.stringify(bootCase.stored),
    );
  }
}

afterEach(() => {
  resetDocumentSurface();
  window.localStorage.clear();
  stubLanguage();
  (window as { matchMedia?: typeof window.matchMedia }).matchMedia =
    originalMatchMedia;
});

describe("theme boot script", () => {
  it("is wired into index.html through a single placeholder", () => {
    expect(indexHtml.split(THEME_BOOT_PLACEHOLDER)).toHaveLength(2);
    expect(indexHtml).not.toContain(`<script ${THEME_BOOT_ATTRIBUTE}`);
  });

  it("cannot break out of the inline script element", () => {
    expect(buildThemeBootScript().toLowerCase()).not.toContain("</script");
  });

  it.each(BOOT_CASES)(
    "matches resolve + present for: $name",
    (bootCase) => {
      seedStorage(bootCase);
      stubMatchMedia(bootCase.systemDark);
      stubLanguage(bootCase.language);

      runBootScript();

      expect(readSurface(document.documentElement)).toEqual(
        readReferenceSurface(),
      );
    },
  );

  it("applies defaults when storage access throws", () => {
    stubMatchMedia(true);
    stubLanguage("en-US");

    const getItem = window.localStorage.getItem.bind(window.localStorage);
    window.localStorage.getItem = () => {
      throw new Error("storage blocked");
    };

    try {
      runBootScript();
    } finally {
      window.localStorage.getItem = getItem;
    }

    expect(readSurface(document.documentElement)).toEqual(
      readReferenceSurface(),
    );
  });
});
