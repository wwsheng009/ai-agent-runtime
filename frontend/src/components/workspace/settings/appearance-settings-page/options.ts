// 由 components/workspace/settings/appearance-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { MonitorSmartphoneIcon, MoonIcon, SunIcon } from "lucide-react";

import { type CodeFontPreset, type FontFamilyPreset } from "@/core/settings";

export function buildAccentOptions(t: TFunction<"settings">) {
  const accentOptions = [
    {
      id: "gold",
      label: t("appearance.accentOptions.gold.label"),
      description: t("appearance.accentOptions.gold.description"),
      previewClassName: "from-[#f0c77b] to-[#d59645]",
    },
    {
      id: "cyan",
      label: t("appearance.accentOptions.cyan.label"),
      description: t("appearance.accentOptions.cyan.description"),
      previewClassName: "from-[#8fd0c6] to-[#51b7c2]",
    },
    {
      id: "violet",
      label: t("appearance.accentOptions.violet.label"),
      description: t("appearance.accentOptions.violet.description"),
      previewClassName: "from-[#9089fc] to-[#6f67f4]",
    },
  ] as const;
  return accentOptions;
}

export function buildThemeOptions(t: TFunction<"settings">) {
  const themeOptions = [
    {
      id: "system",
      label: t("appearance.themeOptions.system.label"),
      description: t("appearance.themeOptions.system.description"),
      icon: MonitorSmartphoneIcon,
    },
    {
      id: "light",
      label: t("appearance.themeOptions.light.label"),
      description: t("appearance.themeOptions.light.description"),
      icon: SunIcon,
    },
    {
      id: "dark",
      label: t("appearance.themeOptions.dark.label"),
      description: t("appearance.themeOptions.dark.description"),
      icon: MoonIcon,
    },
  ] as const;
  return themeOptions;
}

export function buildFontFamilyOptions(t: TFunction<"settings">) {
  const fontFamilyOptions: Array<{
    description: string;
    id: FontFamilyPreset;
    label: string;
    sample: string;
  }> = [
    {
      id: "system",
      label: t("appearance.fontFamilyOptions.system.label"),
      description: t("appearance.fontFamilyOptions.system.description"),
      sample: t("appearance.fontFamilyOptions.system.sample"),
    },
    {
      id: "humanist",
      label: t("appearance.fontFamilyOptions.humanist.label"),
      description: t("appearance.fontFamilyOptions.humanist.description"),
      sample: t("appearance.fontFamilyOptions.humanist.sample"),
    },
    {
      id: "editorial",
      label: t("appearance.fontFamilyOptions.editorial.label"),
      description: t("appearance.fontFamilyOptions.editorial.description"),
      sample: t("appearance.fontFamilyOptions.editorial.sample"),
    },
  ];
  return fontFamilyOptions;
}

export function buildCodeFontOptions(t: TFunction<"settings">) {
  const codeFontOptions: Array<{
    description: string;
    id: CodeFontPreset;
    label: string;
    sample: string;
  }> = [
    {
      id: "jetbrains",
      label: t("appearance.codeFontOptions.jetbrains.label"),
      description: t("appearance.codeFontOptions.jetbrains.description"),
      sample: t("appearance.codeFontOptions.jetbrains.sample"),
    },
    {
      id: "cascadia",
      label: t("appearance.codeFontOptions.cascadia.label"),
      description: t("appearance.codeFontOptions.cascadia.description"),
      sample: t("appearance.codeFontOptions.cascadia.sample"),
    },
    {
      id: "classic",
      label: t("appearance.codeFontOptions.classic.label"),
      description: t("appearance.codeFontOptions.classic.description"),
      sample: t("appearance.codeFontOptions.classic.sample"),
    },
  ];
  return codeFontOptions;
}

export type AccentOption = ReturnType<typeof buildAccentOptions>[number];
export type ThemeOption = ReturnType<typeof buildThemeOptions>[number];
export type FontFamilyOption = ReturnType<typeof buildFontFamilyOptions>[number];
export type CodeFontOption = ReturnType<typeof buildCodeFontOptions>[number];
