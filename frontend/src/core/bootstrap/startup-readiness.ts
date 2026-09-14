// P1-10：启动完整性检查。React 挂载后确认「根节点已有内容 + 应用壳已就绪 +
// i18n 已初始化」，未就绪时由 main.tsx 转入可见错误面，而不是停留在半加载状态。
//
// 就绪标记由 `<StartupReadySignal />`（位于 SettingsProvider + BrowserRouter 内部）
// 在 effect 中写入 `data-app-ready`，因此标记存在即证明关键 provider 与路由已挂载。

import { logger } from "@/core/logger";
import { i18n } from "@/i18n";

export const STARTUP_READY_ATTRIBUTE = "data-app-ready";
export const DEFAULT_STARTUP_READY_TIMEOUT_MS = 5000;
export const DEFAULT_STARTUP_READY_INTERVAL_MS = 50;

export class StartupReadinessError extends Error {
  readonly missing: readonly string[];

  constructor(missing: readonly string[]) {
    super(`startup readiness check failed: ${missing.join(", ")}`);
    this.name = "StartupReadinessError";
    this.missing = missing;
  }
}

export function markStartupReady(documentRef?: Document): void {
  const doc =
    documentRef ?? (typeof document === "undefined" ? undefined : document);
  doc?.documentElement.setAttribute(STARTUP_READY_ATTRIBUTE, "1");
}

export function isStartupReady(documentRef?: Document): boolean {
  const doc =
    documentRef ?? (typeof document === "undefined" ? undefined : document);
  if (!doc) {
    return false;
  }
  return doc.documentElement.getAttribute(STARTUP_READY_ATTRIBUTE) === "1";
}

export function collectStartupReadinessIssues(
  documentRef: Document,
  isI18nReady: () => boolean = () => i18n.isInitialized,
): string[] {
  const issues: string[] = [];
  const root = documentRef.getElementById("root");
  if (!root || root.firstElementChild === null) {
    issues.push("root-content");
  }
  if (!isStartupReady(documentRef)) {
    issues.push("app-shell");
  }
  if (!isI18nReady()) {
    issues.push("i18n");
  }
  return issues;
}

export interface StartupReadinessOptions {
  timeoutMs?: number;
  intervalMs?: number;
  documentRef?: Document;
  isI18nReady?: () => boolean;
}

export function waitForStartupReadiness(
  options: StartupReadinessOptions = {},
): Promise<void> {
  const documentRef = options.documentRef ?? document;
  const timeoutMs = options.timeoutMs ?? DEFAULT_STARTUP_READY_TIMEOUT_MS;
  const intervalMs = options.intervalMs ?? DEFAULT_STARTUP_READY_INTERVAL_MS;

  return new Promise((resolve, reject) => {
    const startedAt = Date.now();

    const check = (): void => {
      const issues = collectStartupReadinessIssues(
        documentRef,
        options.isI18nReady,
      );
      if (issues.length === 0) {
        resolve();
        return;
      }
      if (Date.now() - startedAt >= timeoutMs) {
        const failure = new StartupReadinessError(issues);
        logger.error("startup readiness check timed out", failure, {
          issues,
        });
        reject(failure);
        return;
      }
      setTimeout(check, intervalMs);
    };

    check();
  });
}
