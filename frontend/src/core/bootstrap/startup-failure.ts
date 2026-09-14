// P1-10：React 之前的启动失败面。`#root` 缺失、`bootstrapDocumentSettings` 抛错、
// `createRoot` 抛错等场景都走这里，直接在 DOM 上渲染可见、可操作的提示，避免白屏。
//
// 说明：文案优先走 i18n（resources 静态打包，正常路径同步可用）；若 i18n 本身
// 初始化失败，退回内置的中英双语应急文案，保证任何情况下屏幕上有内容。

import { logger } from "@/core/logger";
import { i18n } from "@/i18n";

const EMERGENCY_TITLE = "应用启动失败 / Application failed to start";
const EMERGENCY_DESCRIPTION =
  "应用初始化时出现问题 / The application failed to initialize.";
const EMERGENCY_RELOAD = "重新加载 / Reload";

type StartupErrorKey =
  | "errors.startup.title"
  | "errors.startup.description"
  | "errors.startup.reload";

function translate(key: StartupErrorKey, fallback: string): string {
  try {
    const value: string = i18n.t(key);
    if (typeof value === "string" && value.length > 0 && value !== key) {
      return value;
    }
  } catch {
    // i18n 尚未就绪或初始化失败：退回内置应急文案。
  }
  return fallback;
}

export interface StartupFailureOptions {
  error?: unknown;
  /** 覆盖描述文案（例如「未找到 #root」）。 */
  description?: string;
  documentRef?: Document;
  root?: HTMLElement | null;
  /** 测试可注入；默认 `window.location.reload()`。 */
  onReload?: () => void;
}

export function resolveRootElement(documentRef: Document): HTMLElement {
  const root = documentRef.getElementById("root");
  if (!root) {
    throw new Error("missing #root mount node");
  }
  return root;
}

// React 错误边界已经渲染出 `role="alert"` 时不再叠加启动失败面，
// 避免两类错误同时出现时互相覆盖。
export function hasVisibleFailureSurface(documentRef?: Document): boolean {
  const doc =
    documentRef ?? (typeof document === "undefined" ? undefined : document);
  if (!doc) {
    return false;
  }
  return doc.querySelector('[role="alert"]') !== null;
}

function describeError(error: unknown): string | undefined {
  if (error instanceof Error && error.message.trim().length > 0) {
    return error.message.trim();
  }
  return undefined;
}

function reloadDefault(): void {
  if (typeof window !== "undefined") {
    window.location.reload();
  }
}

export function renderStartupFailure(
  options: StartupFailureOptions = {},
): HTMLElement | null {
  if (typeof document === "undefined") {
    return null;
  }
  const doc = options.documentRef ?? document;
  const container = options.root ?? doc.getElementById("root") ?? doc.body;
  if (!container) {
    return null;
  }

  const surface = doc.createElement("div");
  surface.setAttribute("role", "alert");
  surface.setAttribute("data-startup-failure", "true");
  // 兜底内联样式：即使样式表也加载失败，错误面仍然可见。
  surface.style.cssText = [
    "min-height:100vh",
    "display:flex",
    "align-items:center",
    "justify-content:center",
    "padding:24px",
    "box-sizing:border-box",
    "font-family:system-ui,-apple-system,'Segoe UI',sans-serif",
    "background:var(--workspace-shell-bg,#0b0d12)",
    "color:var(--foreground,#f5f6f8)",
  ].join(";");

  const card = doc.createElement("div");
  card.style.cssText = [
    "max-width:32rem",
    "width:100%",
    "display:flex",
    "flex-direction:column",
    "gap:12px",
    "border:1px solid rgba(148,163,184,0.35)",
    "border-radius:12px",
    "padding:20px",
  ].join(";");

  const title = doc.createElement("h1");
  title.style.cssText = "margin:0;font-size:18px;line-height:1.4";
  title.textContent = translate("errors.startup.title", EMERGENCY_TITLE);

  const description = doc.createElement("p");
  description.style.cssText =
    "margin:0;font-size:14px;line-height:1.6;opacity:0.85";
  description.textContent =
    options.description ??
    translate("errors.startup.description", EMERGENCY_DESCRIPTION);

  const detail = describeError(options.error);

  const actions = doc.createElement("div");
  actions.style.cssText = "display:flex;gap:8px;flex-wrap:wrap";

  const reload = doc.createElement("button");
  reload.type = "button";
  reload.textContent = translate("errors.startup.reload", EMERGENCY_RELOAD);
  reload.style.cssText =
    "font:inherit;font-size:14px;padding:6px 14px;border-radius:8px;border:1px solid rgba(148,163,184,0.45);background:transparent;color:inherit;cursor:pointer";
  reload.addEventListener("click", () => {
    (options.onReload ?? reloadDefault)();
  });
  actions.appendChild(reload);

  card.appendChild(title);
  card.appendChild(description);
  if (detail) {
    const detailNode = doc.createElement("p");
    detailNode.style.cssText =
      "margin:0;font-size:12px;line-height:1.5;opacity:0.7;word-break:break-word";
    detailNode.textContent = detail;
    card.appendChild(detailNode);
  }
  card.appendChild(actions);
  surface.appendChild(card);

  container.replaceChildren(surface);

  logger.error("startup failure surface rendered", options.error, {
    description: options.description ?? "",
  });

  return surface;
}
