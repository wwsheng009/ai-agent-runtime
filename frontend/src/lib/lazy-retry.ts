// P1-10：lazy 路由 chunk 加载失败的「有上限 + 递增退避」重试。
//
// 设计要点：
// - 自动重试次数有硬上限（默认 = 退避表长度 + 1），失败后抛出 ChunkLoadError，
//   由路由级错误边界显示可手动重试的提示；不做无限循环。
// - 退避延迟显式配置（默认 400ms / 1200ms），超出退避表长度的重试沿用最后一个值。
// - sleep / 退避表可注入，便于测试用假 loader 断言「调用次数上限 + 退避递增」。

import { lazy, type ComponentType, type LazyExoticComponent } from "react";

import { logger } from "@/core/logger";

export const CHUNK_LOAD_ERROR_CODE = "chunk_load_failed";

export const DEFAULT_CHUNK_RETRY_DELAYS_MS: readonly number[] = [400, 1200];

const CHUNK_LOAD_ERROR_PATTERN =
  /dynamically imported module|importing a module script|loading chunk|chunk load failed|failed to fetch/i;

export class ChunkLoadError extends Error {
  readonly code: typeof CHUNK_LOAD_ERROR_CODE = CHUNK_LOAD_ERROR_CODE;

  readonly attempts: number;

  readonly lastError: unknown;

  constructor(attempts: number, lastError: unknown) {
    super(`chunk load failed after ${attempts} attempt(s)`);
    this.name = "ChunkLoadError";
    this.attempts = attempts;
    this.lastError = lastError;
  }
}

// 既认自己抛的 ChunkLoadError，也认浏览器/Vite 的原生 dynamic import 失败文案，
// 保证直接调用 loader（未经过 loadWithRetry）时的错误面仍然是「资源加载失败」。
export function isChunkLoadError(error: unknown): error is ChunkLoadError {
  return error instanceof ChunkLoadError;
}

export function isChunkLoadFailure(error: unknown): boolean {
  if (isChunkLoadError(error)) {
    return true;
  }
  if (error instanceof Error) {
    return CHUNK_LOAD_ERROR_PATTERN.test(error.message);
  }
  return false;
}

export type SleepFn = (delayMs: number) => Promise<void>;

export interface LoadWithRetryOptions {
  /** 总尝试次数（含首次）；默认 = 退避表长度 + 1。 */
  attempts?: number;
  /** 递增退避表；重试次数超出表长时沿用最后一个值。 */
  delaysMs?: readonly number[];
  /** 可注入，以便测试不真实等待。 */
  sleep?: SleepFn;
  onRetry?: (info: { attempt: number; delayMs: number; error: unknown }) => void;
}

const defaultSleep: SleepFn = (delayMs) =>
  new Promise((resolve) => {
    setTimeout(resolve, delayMs);
  });

function resolveRetryDelays(delaysMs?: readonly number[]): readonly number[] {
  const usable = (delaysMs ?? DEFAULT_CHUNK_RETRY_DELAYS_MS).filter(
    (delay) => Number.isFinite(delay) && delay > 0,
  );
  return usable.length > 0 ? usable : DEFAULT_CHUNK_RETRY_DELAYS_MS;
}

function resolveRetryAttempts(
  attempts: number | undefined,
  delays: readonly number[],
): number {
  if (attempts === undefined || !Number.isFinite(attempts)) {
    return delays.length + 1;
  }
  return Math.max(1, Math.floor(attempts));
}

export async function loadWithRetry<T>(
  loader: () => Promise<T>,
  options: LoadWithRetryOptions = {},
): Promise<T> {
  const delays = resolveRetryDelays(options.delaysMs);
  const attempts = resolveRetryAttempts(options.attempts, delays);
  const sleep = options.sleep ?? defaultSleep;
  let lastError: unknown;

  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    try {
      return await loader();
    } catch (error) {
      lastError = error;
      if (attempt >= attempts) {
        break;
      }
      const delayMs = delays[Math.min(attempt - 1, delays.length - 1)];
      options.onRetry?.({ attempt, delayMs, error });
      await sleep(delayMs);
    }
  }

  const failure = new ChunkLoadError(attempts, lastError);
  logger.error("lazy surface load failed", failure, {
    attempts,
    delaysMs: delays,
  });
  throw failure;
}

export type LazySurfaceLoader = () => Promise<{ default: ComponentType }>;

export function lazyWithRetry(
  loader: LazySurfaceLoader,
  options: LoadWithRetryOptions = {},
): LazyExoticComponent<ComponentType> {
  return lazy(() => loadWithRetry(loader, options));
}
