// P1-10：三层错误边界的对外入口（全局 / 路由级 / 面板级）。
// 文案统一走 common 命名空间的 errors.*（zh-CN 为真源，en-US satisfies 对齐）；
// 恢复动作默认是「重试（reset）」+「返回（首页）」/「重新加载页面」。

import { useCallback, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { isChunkLoadError, isChunkLoadFailure } from "@/lib/lazy-retry";

import { ErrorBoundary } from "./error-boundary";
import { ErrorSurface } from "./error-surface";

function describeError(error: unknown): string | undefined {
  if (error instanceof Error && error.message.trim().length > 0) {
    return error.message.trim();
  }
  return undefined;
}

function navigateHome(): void {
  if (typeof window !== "undefined") {
    window.location.assign("/");
  }
}

function reloadPage(): void {
  if (typeof window !== "undefined") {
    window.location.reload();
  }
}

export interface GlobalErrorBoundaryProps {
  children: ReactNode;
  /** 测试或自定义壳层可注入；默认跳转首页。 */
  onHome?: () => void;
  onError?: (error: unknown) => void;
}

export function GlobalErrorBoundary({
  children,
  onHome = navigateHome,
  onError,
}: GlobalErrorBoundaryProps) {
  const { t } = useTranslation("common");
  const handleHome = useCallback(() => {
    onHome();
  }, [onHome]);

  return (
    <ErrorBoundary
      scope="global"
      onError={(error) => {
        onError?.(error);
      }}
      renderFallback={({ error, reset }) => (
        <ErrorSurface
          scope="global"
          title={t("errors.boundary.globalTitle")}
          description={t("errors.boundary.globalDescription")}
          detail={describeError(error)}
          actions={[
            {
              key: "retry",
              label: t("errors.boundary.retry"),
              onClick: reset,
              variant: "primary",
              autoFocus: true,
            },
            {
              key: "home",
              label: t("errors.boundary.home"),
              onClick: handleHome,
            },
          ]}
        />
      )}
    >
      {children}
    </ErrorBoundary>
  );
}

export interface RouteErrorBoundaryProps {
  children: ReactNode;
  /** chunk 等懒加载失败时的手动重试入口（通常重建 lazy 组件）。 */
  onRetry?: () => void;
  /** 测试或自定义壳层可注入；默认跳转首页。 */
  onHome?: () => void;
}

export function RouteErrorBoundary({
  children,
  onRetry,
  onHome = navigateHome,
}: RouteErrorBoundaryProps) {
  const { t } = useTranslation("common");
  const handleHome = useCallback(() => {
    onHome();
  }, [onHome]);

  return (
    <ErrorBoundary
      scope="route"
      renderFallback={({ error, reset }) => {
        const chunkFailure = isChunkLoadFailure(error);
        const attempts = isChunkLoadError(error) ? error.attempts : 0;
        const handleRetry = () => {
          onRetry?.();
          reset();
        };

        return (
          <ErrorSurface
            scope="route"
            title={
              chunkFailure
                ? t("errors.chunk.title")
                : t("errors.boundary.routeTitle")
            }
            description={
              chunkFailure
                ? t("errors.chunk.description", { attempts: String(attempts) })
                : t("errors.boundary.routeDescription")
            }
            detail={describeError(error)}
            hint={chunkFailure ? t("errors.chunk.hint") : undefined}
            actions={[
              {
                key: "retry",
                label: chunkFailure
                  ? t("errors.boundary.reload")
                  : t("errors.boundary.retry"),
                onClick: handleRetry,
                variant: "primary",
                autoFocus: true,
              },
              {
                key: "home",
                label: t("errors.boundary.home"),
                onClick: handleHome,
              },
            ]}
          />
        );
      }}
    >
      {children}
    </ErrorBoundary>
  );
}

export interface PanelErrorBoundaryProps {
  children: ReactNode;
  /** 面板名（已翻译）；缺省用通用「面板加载失败」。 */
  title?: string;
  onRetry?: () => void;
  onReload?: () => void;
}

export function PanelErrorBoundary({
  children,
  title,
  onRetry,
  onReload = reloadPage,
}: PanelErrorBoundaryProps) {
  const { t } = useTranslation("common");
  const handleReload = useCallback(() => {
    onReload();
  }, [onReload]);

  return (
    <ErrorBoundary
      scope="panel"
      renderFallback={({ error, reset }) => (
        <ErrorSurface
          scope="panel"
          title={title ?? t("errors.boundary.panelTitle")}
          description={t("errors.boundary.panelDescription")}
          detail={describeError(error)}
          actions={[
            {
              key: "retry",
              label: t("errors.boundary.retry"),
              onClick: () => {
                onRetry?.();
                reset();
              },
              variant: "primary",
              autoFocus: true,
            },
            {
              key: "reload",
              label: t("errors.boundary.reload"),
              onClick: handleReload,
            },
          ]}
        />
      )}
    >
      {children}
    </ErrorBoundary>
  );
}
