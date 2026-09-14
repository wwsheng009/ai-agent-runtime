// P1-10：类组件错误边界基座。三层边界（全局/路由/面板）共用这一实现：
// - getDerivedStateFromError：进入错误态；
// - componentDidCatch：错误统一走 logger 出口（scope + componentStack），
//   并回调 onError 供上层埋点；
// - renderFallback：错误面由上层注入（便于 i18n 与不同 scope 的恢复动作）。

import { Component, type ErrorInfo, type ReactNode } from "react";

import { logger } from "@/core/logger";

export type ErrorBoundaryScope = "global" | "route" | "panel";

export interface ErrorBoundaryFallbackRenderProps {
  error: unknown;
  reset: () => void;
}

export interface ErrorBoundaryProps {
  children: ReactNode;
  scope: ErrorBoundaryScope;
  renderFallback: (props: ErrorBoundaryFallbackRenderProps) => ReactNode;
  onError?: (error: unknown, info: ErrorInfo) => void;
}

interface ErrorBoundaryState {
  error: unknown;
  failed: boolean;
}

export class ErrorBoundary extends Component<
  ErrorBoundaryProps,
  ErrorBoundaryState
> {
  state: ErrorBoundaryState = { error: undefined, failed: false };

  static getDerivedStateFromError(error: unknown): ErrorBoundaryState {
    return { error, failed: true };
  }

  componentDidCatch(error: unknown, info: ErrorInfo): void {
    logger.error("error boundary caught a render error", error, {
      scope: this.props.scope,
      componentStack: info.componentStack ?? "",
    });
    this.props.onError?.(error, info);
  }

  reset = (): void => {
    this.setState({ error: undefined, failed: false });
  };

  render(): ReactNode {
    if (this.state.failed) {
      return this.props.renderFallback({
        error: this.state.error,
        reset: this.reset,
      });
    }

    return this.props.children;
  }
}
