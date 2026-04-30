import type { ResolvedLocale } from "./locale";

export function formatRelativeTimestamp(
  locale: ResolvedLocale,
  input: string,
) {
  const now = Date.now();
  const value = new Date(input).getTime();

  if (!Number.isFinite(value)) {
    return input;
  }

  const diff = value - now;
  const absMinutes = Math.abs(Math.round(diff / 60000));
  const relativeTime = new Intl.RelativeTimeFormat(locale, {
    numeric: "auto",
    style: "short",
  });

  if (absMinutes < 1) {
    return locale === "zh-CN" ? "刚刚" : "just now";
  }

  if (absMinutes < 60) {
    return relativeTime.format(Math.round(diff / 60000), "minute");
  }

  const hours = Math.round(diff / 3600000);
  if (Math.abs(hours) < 24) {
    return relativeTime.format(hours, "hour");
  }

  const days = Math.round(diff / 86400000);
  if (Math.abs(days) < 7) {
    return relativeTime.format(days, "day");
  }

  return new Intl.DateTimeFormat(locale, {
    month: "short",
    day: "numeric",
  }).format(new Date(input));
}

export function formatLogTimestamp(locale: ResolvedLocale, value?: string) {
  if (!value) {
    return locale === "zh-CN" ? "无时间戳" : "No timestamp";
  }

  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return value;
  }

  return new Intl.DateTimeFormat(locale, {
    hour12: false,
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(parsed);
}
