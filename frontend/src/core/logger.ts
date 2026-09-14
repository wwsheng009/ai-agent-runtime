// P1-10：全局日志出口。错误与告警统一经此收敛（错误边界、chunk 重试、启动失败
// 都从这里出），避免在业务代码里散落 console.* 调用。
//
// 约定：只做「最小可用」的事——按级别转发到 console、附加 scope 前缀与可选结构化
// 上下文；不引入依赖、不做远端上报。后续如需接远端 sink，只需在此模块集中替换。

export type LogLevel = "debug" | "info" | "warn" | "error";

export type LogContext = Record<string, unknown>;

export interface Logger {
  debug(message: string, context?: LogContext): void;
  info(message: string, context?: LogContext): void;
  warn(message: string, context?: LogContext): void;
  error(message: string, error?: unknown, context?: LogContext): void;
}

const consoleByLevel: Record<LogLevel, (prefix: string, extra: readonly unknown[]) => void> = {
  debug: (prefix, extra) => {
    console.debug(prefix, ...extra);
  },
  info: (prefix, extra) => {
    console.info(prefix, ...extra);
  },
  warn: (prefix, extra) => {
    console.warn(prefix, ...extra);
  },
  error: (prefix, extra) => {
    console.error(prefix, ...extra);
  },
};

function writeLog(
  scope: string,
  level: LogLevel,
  message: string,
  extra: readonly unknown[],
): void {
  const prefix = scope ? `[${scope}] ${message}` : message;
  consoleByLevel[level](prefix, extra);
}

export function createLogger(scope: string): Logger {
  return {
    debug(message, context) {
      writeLog(scope, "debug", message, context === undefined ? [] : [context]);
    },
    info(message, context) {
      writeLog(scope, "info", message, context === undefined ? [] : [context]);
    },
    warn(message, context) {
      writeLog(scope, "warn", message, context === undefined ? [] : [context]);
    },
    error(message, error, context) {
      const extra: unknown[] = [];
      if (error !== undefined) {
        extra.push(error);
      }
      if (context !== undefined) {
        extra.push(context);
      }
      writeLog(scope, "error", message, extra);
    },
  };
}

export const logger: Logger = createLogger("app");
