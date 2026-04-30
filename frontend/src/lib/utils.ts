import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

import { formatRelativeTimestamp as formatRelativeTimestampWithLocale } from "@/i18n/format";
import { resolveSystemLocale } from "@/i18n/locale";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatRelativeTimestamp(input: string) {
  const resolvedLocale = (() => {
    if (typeof document !== "undefined") {
      const lang = document.documentElement.lang.trim();
      if (lang === "zh-CN" || lang === "en-US") {
        return lang;
      }
    }

    if (typeof navigator !== "undefined") {
      return resolveSystemLocale(navigator.language);
    }

    return "en-US";
  })();

  return formatRelativeTimestampWithLocale(resolvedLocale, input);
}
