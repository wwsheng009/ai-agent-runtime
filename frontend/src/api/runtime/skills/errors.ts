// P0-2 拆分（原 skills.ts 错误分类区）：端点不可用 / 写操作被拒的判据。

import { RuntimeApiError } from "../shared";

/**
 * 端点不可用的降级判据：路由缺失（404/405）、未实现（501）、服务未配置（503）。
 * 403 属于「策略拒绝」而非不可用，由 UI 单独呈现（绝不与未配置混为一谈）。
 */
export function isSkillsUnavailable(error: unknown): boolean {
  if (!(error instanceof RuntimeApiError)) {
    return false;
  }
  return (
    error.status === 404 ||
    error.status === 405 ||
    error.status === 501 ||
    error.status === 503
  );
}

/** 写操作被策略 / 鉴权拒绝（403）。 */
export function isSkillsForbidden(error: unknown): boolean {
  return error instanceof RuntimeApiError && error.status === 403;
}
