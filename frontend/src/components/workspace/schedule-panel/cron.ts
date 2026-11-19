// Cron 表达式的解析与反向生成。
//
// 只覆盖 5 段标准写法（分 时 日 月 周）且只解释最常见的可读形态；
// 无法识别时返回 null，由调用方决定是否隐藏预览，绝不臆造描述。

export const CRON_PRESETS = [
  "*/5 * * * *",
  "*/15 * * * *",
  "0 * * * *",
  "0 9 * * *",
  "0 9 * * 1-5",
  "0 9 1 * *",
] as const;

export type CronDescription =
  | { kind: "everyMinutes"; minutes: number }
  | { kind: "everyHours"; hours: number }
  | { kind: "hourly"; minute: number }
  | { kind: "daily"; hour: number; minute: number }
  | { kind: "weekdays"; hour: number; minute: number }
  | { kind: "weekly"; weekday: number; hour: number; minute: number }
  | { kind: "monthly"; day: number; hour: number; minute: number };

function parseNumber(field: string, min: number, max: number): number | null {
  if (!/^\d+$/.test(field)) {
    return null;
  }
  const value = Number(field);
  if (!Number.isInteger(value) || value < min || value > max) {
    return null;
  }
  return value;
}

function parseStep(field: string, unit: "minute" | "hour"): number | null {
  const match = /^\*\/(\d+)$/.exec(field);
  if (!match) {
    return null;
  }
  const step = Number(match[1]);
  if (unit === "minute" ? step < 1 || step > 59 : step < 1 || step > 23) {
    return null;
  }
  return step;
}

/**
 * 把 5 段 cron 解析成结构化描述；不支持的形态返回 null。
 */
export function describeCron(expression: string): CronDescription | null {
  const fields = expression.trim().split(/\s+/);
  if (fields.length !== 5) {
    return null;
  }
  const [minuteField, hourField, dayField, monthField, weekdayField] = fields;

  // 月与日必须为任意值，否则会落到“特定日期 + 特定星期”的复杂语义。
  if (monthField !== "*") {
    return null;
  }

  const minuteStep = parseStep(minuteField, "minute");
  if (minuteStep !== null && hourField === "*" && dayField === "*" && weekdayField === "*") {
    return { kind: "everyMinutes", minutes: minuteStep };
  }

  const hourStep = parseStep(hourField, "hour");
  if (minuteField === "0" && hourStep !== null && dayField === "*" && weekdayField === "*") {
    return { kind: "everyHours", hours: hourStep };
  }

  const minute = parseNumber(minuteField, 0, 59);
  if (minute === null) {
    return null;
  }

  if (hourField === "*" && dayField === "*" && weekdayField === "*") {
    return { kind: "hourly", minute };
  }

  const hour = parseNumber(hourField, 0, 23);
  if (hour === null) {
    return null;
  }

  if (dayField === "*" && weekdayField === "*") {
    return { kind: "daily", hour, minute };
  }

  if (dayField === "*" && weekdayField === "1-5") {
    return { kind: "weekdays", hour, minute };
  }

  if (dayField === "*") {
    const weekday = parseNumber(weekdayField, 0, 6);
    if (weekday === null) {
      return null;
    }
    return { kind: "weekly", weekday, hour, minute };
  }

  if (weekdayField === "*") {
    const day = parseNumber(dayField, 1, 31);
    if (day === null) {
      return null;
    }
    return { kind: "monthly", day, hour, minute };
  }

  return null;
}

export type CronBuilderFrequency = "everyMinutes" | "hourly" | "daily" | "weekly";

export type CronBuilderState = {
  frequency: CronBuilderFrequency;
  /** 每几分钟 / 每周几 / 每月几号，视 frequency 而定。 */
  interval: number;
  hour: number;
  minute: number;
  weekday: number;
};

export const DEFAULT_CRON_BUILDER: CronBuilderState = {
  frequency: "daily",
  interval: 5,
  hour: 9,
  minute: 0,
  weekday: 1,
};

/** 由可视化状态生成 cron 表达式。 */
export function buildCron(state: CronBuilderState): string {
  const minute = Math.min(59, Math.max(0, Math.trunc(state.minute)));
  const hour = Math.min(23, Math.max(0, Math.trunc(state.hour)));

  switch (state.frequency) {
    case "everyMinutes": {
      const interval = Math.min(59, Math.max(1, Math.trunc(state.interval)));
      return `*/${interval} * * * *`;
    }
    case "hourly":
      return `${minute} * * * *`;
    case "weekly": {
      const weekday = Math.min(6, Math.max(0, Math.trunc(state.weekday)));
      return `${minute} ${hour} * * ${weekday}`;
    }
    case "daily":
    default:
      return `${minute} ${hour} * * *`;
  }
}

/** 表达式与可视化状态无法一一对应时，用于回填近似状态。 */
export function invertCron(expression: string): CronBuilderState | null {
  const described = describeCron(expression);
  if (!described) {
    return null;
  }
  switch (described.kind) {
    case "everyMinutes":
      return { ...DEFAULT_CRON_BUILDER, frequency: "everyMinutes", interval: described.minutes };
    case "hourly":
      return { ...DEFAULT_CRON_BUILDER, frequency: "hourly", minute: described.minute };
    case "daily":
      return {
        ...DEFAULT_CRON_BUILDER,
        frequency: "daily",
        hour: described.hour,
        minute: described.minute,
      };
    case "weekly":
      return {
        ...DEFAULT_CRON_BUILDER,
        frequency: "weekly",
        weekday: described.weekday,
        hour: described.hour,
        minute: described.minute,
      };
    default:
      return null;
  }
}
