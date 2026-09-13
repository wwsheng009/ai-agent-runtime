// 管理令牌读取（原 pages/usage-analytics/format.ts 内的实现上移到 lib，
// 供 /usage 页面与工作台 usage 面板等非页面模块共用同一存储键）。

export const adminTokenStorageKey = "runtime.logs.adminToken";

export function readAdminToken() {
  return typeof window === "undefined"
    ? ""
    : window.localStorage.getItem(adminTokenStorageKey)?.trim() ?? "";
}
