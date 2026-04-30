import i18n from "i18next";
import { initReactI18next } from "react-i18next";

import { defaultNS, resources } from "./resources";
import type { ResolvedLocale } from "./locale";

export function initI18n(initialLocale: ResolvedLocale) {
  if (i18n.isInitialized) {
    if (i18n.language !== initialLocale) {
      void i18n.changeLanguage(initialLocale);
    }
    return i18n;
  }

  void i18n.use(initReactI18next).init({
    lng: initialLocale,
    fallbackLng: "zh-CN",
    supportedLngs: ["zh-CN", "en-US"],
    defaultNS,
    ns: Object.keys(resources["zh-CN"]),
    resources,
    interpolation: {
      escapeValue: false,
    },
    react: {
      useSuspense: false,
    },
  });

  return i18n;
}

export { i18n };
